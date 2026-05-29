package handlers

import (
	"database/sql"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagehost/config"
	"imagehost/database"
	"imagehost/middleware"
	"imagehost/storage"

	"github.com/gin-gonic/gin"
)

// ── Handler ───────────────────────────────────────────────────────────────────

type Handler struct {
	db    *database.DB
	store *storage.Storage
}

func New(db *database.DB, store *storage.Storage) *Handler {
	return &Handler{db: db, store: store}
}

// ── Auth middleware ───────────────────────────────────────────────────────────

// AuthMiddleware validates Bearer token or ?password= query param.
// It applies brute-force protection via the middleware package.
func AuthMiddleware() gin.HandlerFunc {
	bf := middleware.AuthBruteForce(func() int {
		return config.Get().AuthMaxAttempts
	})
	return func(c *gin.Context) {
		// Apply brute-force gate first
		bf(c)
		if c.IsAborted() {
			return
		}

		auth := c.GetHeader("Authorization")
		if auth == "" {
			auth = c.Query("password")
		}
		password := strings.TrimPrefix(auth, "Bearer ")

		if password != config.Get().UploadPassword {
			// Do NOT reset counter — let failed attempts accumulate
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		// Successful auth — reset this IP's failure counter
		middleware.RecordAuthSuccess(middleware.ClientIP(c))
		c.Next()
	}
}

// ── Progress SSE store ────────────────────────────────────────────────────────

var (
	progressMu      sync.RWMutex
	progressStreams = make(map[string]chan string)
)

// ── Public: list images ───────────────────────────────────────────────────────

func (h *Handler) ListImages(c *gin.Context) {
	tags := parseTags(c)
	page, limit := parsePage(c)

	images, total, err := h.db.GetImages(tags, page, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := make([]imageResp, 0, len(images))
	for _, img := range images {
		result = append(result, toResp(img))
	}
	c.JSON(http.StatusOK, gin.H{"images": result, "total": total, "page": page, "limit": limit})
}

// ── Public: list tags ─────────────────────────────────────────────────────────

func (h *Handler) ListTags(c *gin.Context) {
	tags, err := h.db.GetAllTags()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if tags == nil {
		tags = []*database.Tag{}
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// ── Public: random image ──────────────────────────────────────────────────────
//
// Default behaviour: serve the image file directly (no caching).
// Pass ?json=1 (or ?format=json) to get JSON instead.
// Pass ?count=N with ?json=1 to get N random images (max 50).
// Pass ?exclude=<id> to avoid the last-seen image.

func (h *Handler) RandomImage(c *gin.Context) {
	tags := parseTags(c)
	format := c.DefaultQuery("format", "webp")
	excludeID, _ := strconv.ParseInt(c.Query("exclude"), 10, 64)
	wantJSON := c.Query("json") == "1" || format == "json"

	// ── JSON multi-image mode ──────────────────────────────────────────────
	if wantJSON {
		count, _ := strconv.Atoi(c.DefaultQuery("count", "1"))
		if count < 1 {
			count = 1
		}
		if count > 50 {
			count = 50
		}

		seen := make(map[int64]bool)
		if excludeID > 0 {
			seen[excludeID] = true
		}
		results := make([]imageResp, 0, count)
		for i := 0; i < count; i++ {
			img, err := h.db.GetRandomImageExcluding(tags, seen)
			if err == sql.ErrNoRows {
				// pool exhausted — retry without exclusions
				img, err = h.db.GetRandomImage(tags, 0)
			}
			if err != nil {
				break
			}
			seen[img.ID] = true
			results = append(results, toResp(img))
		}
		if len(results) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no images found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"images": results, "count": len(results)})
		return
	}

	// ── File mode (default) ────────────────────────────────────────────────
	img, err := h.db.GetRandomImage(tags, excludeID)
	if err == sql.ErrNoRows {
		img, err = h.db.GetRandomImage(tags, 0)
	}
	if err == sql.ErrNoRows {
		http.Error(c.Writer, "no images found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(c.Writer, "internal error", http.StatusInternalServerError)
		return
	}

	c.Header("X-Image-Id", strconv.FormatInt(img.ID, 10))
	c.Header("X-Image-Tags", strings.Join(img.Tags, ","))
	c.Header("Access-Control-Expose-Headers", "X-Image-Id, X-Image-Tags")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")

	filePath := img.WebpPath
	if format == "original" {
		filePath = img.OrigPath
	}
	c.File(filePath)
}

// ── Auth-required: upload (frontend form) ────────────────────────────────────

func (h *Handler) Upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadRequestBytes())
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form"})
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files"})
		return
	}
	if err := validateUploadBatch(files); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tags := splitTags(c.PostForm("tags"))
	progressID := c.PostForm("progress_id")

	var results []UploadResult
	for i, fh := range files {
		progressKey := fmt.Sprintf("%s_%d", progressID, i)
		progressCh := make(chan string, 20)
		if progressID != "" {
			progressMu.Lock()
			progressStreams[progressKey] = progressCh
			progressMu.Unlock()
		}
		res := h.processFile(fh, tags, progressCh, i, len(files))
		results = append(results, res)
		if progressID != "" {
			go func(key string) {
				time.Sleep(5 * time.Second)
				progressMu.Lock()
				delete(progressStreams, key)
				progressMu.Unlock()
			}(progressKey)
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// ── Auth-required: API upload (JSON body or multipart) ────────────────────────

func (h *Handler) APIUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadRequestBytes())
	// tags from query or form
	tagsRaw := c.Query("tags")
	if tagsRaw == "" {
		tagsRaw = c.PostForm("tags")
	}
	tags := splitTags(tagsRaw)

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid multipart form"})
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		// also try single "file" field
		files = form.File["file"]
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files provided (use field name 'files' or 'file')"})
		return
	}
	if err := validateUploadBatch(files); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var results []UploadResult
	for i, fh := range files {
		res := h.processFile(fh, tags, nil, i, len(files))
		results = append(results, res)
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// ── Auth-required: delete ─────────────────────────────────────────────────────

func (h *Handler) DeleteImage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	img, err := h.db.DeleteImage(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.store.Delete(img.OrigPath, img.WebpPath)
	c.JSON(http.StatusOK, gin.H{"success": true, "id": id})
}

func (h *Handler) UpdateImageTags(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req struct {
		Tags []string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tags"})
		return
	}

	tags, err := h.db.SetImageTags(id, req.Tags)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	img, err := h.db.GetImageByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	img.Tags = tags
	c.JSON(http.StatusOK, toResp(img))
}

// ── SSE progress stream ───────────────────────────────────────────────────────

func (h *Handler) ProgressStream(c *gin.Context) {
	progressKey := c.Param("id")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(60 * time.Second)

	for {
		select {
		case <-timeout:
			return
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			progressMu.RLock()
			ch, ok := progressStreams[progressKey]
			progressMu.RUnlock()
			if !ok {
				c.SSEvent("message", `{"stage":"waiting"}`)
				c.Writer.Flush()
				continue
			}
			select {
			case msg, open := <-ch:
				if !open {
					c.SSEvent("message", `{"stage":"complete"}`)
					c.Writer.Flush()
					return
				}
				c.SSEvent("message", msg)
				c.Writer.Flush()
			default:
			}
		}
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────────

type UploadResult struct {
	Success  bool     `json:"success"`
	ID       int64    `json:"id,omitempty"`
	Filename string   `json:"filename,omitempty"`
	WebpURL  string   `json:"webp_url,omitempty"`
	OrigURL  string   `json:"orig_url,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type imageResp struct {
	ID        int64     `json:"id"`
	WebpURL   string    `json:"webp_url"`
	OrigURL   string    `json:"orig_url"`
	IsGif     bool      `json:"is_gif"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

func toResp(img *database.Image) imageResp {
	return imageResp{
		ID:        img.ID,
		WebpURL:   "/" + img.WebpPath,
		OrigURL:   "/" + img.OrigPath,
		IsGif:     img.IsGif,
		Width:     img.Width,
		Height:    img.Height,
		Tags:      img.Tags,
		CreatedAt: img.CreatedAt,
	}
}

func parseTags(c *gin.Context) []string {
	var tags []string
	if t := c.Query("tag"); t != "" {
		tags = append(tags, t)
	}
	if t := c.Query("tags"); t != "" {
		tags = append(tags, splitTags(t)...)
	}
	return tags
}

func splitTags(raw string) []string {
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parsePage(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return page, limit
}

func validateUploadBatch(files []*multipart.FileHeader) error {
	cfg := config.Get()
	if cfg.MaxUploadCount > 0 && len(files) > cfg.MaxUploadCount {
		return fmt.Errorf("too many files: max %d per upload", cfg.MaxUploadCount)
	}
	maxBytes := cfg.MaxUploadMB * 1024 * 1024
	for _, fh := range files {
		if maxBytes > 0 && fh.Size > maxBytes {
			return fmt.Errorf("file %s exceeds %dMB limit", fh.Filename, cfg.MaxUploadMB)
		}
	}
	return nil
}

func maxUploadRequestBytes() int64 {
	cfg := config.Get()
	maxMB := cfg.MaxUploadMB
	if maxMB <= 0 {
		maxMB = 50
	}
	maxCount := cfg.MaxUploadCount
	if maxCount <= 0 {
		maxCount = 50
	}
	return maxMB*1024*1024*int64(maxCount) + 10*1024*1024
}

func (h *Handler) processFile(fh *multipart.FileHeader, tags []string, progressCh chan string, idx, total int) UploadResult {
	if progressCh != nil {
		defer close(progressCh)
	}
	send := func(msg string) {
		if progressCh != nil {
			select {
			case progressCh <- msg:
			default:
			}
		}
	}

	send(fmt.Sprintf(`{"stage":"uploading","progress":10,"file":%d,"total":%d}`, idx+1, total))

	f, err := fh.Open()
	if err != nil {
		send(`{"stage":"error","progress":0}`)
		return UploadResult{Error: err.Error()}
	}
	defer f.Close()

	result, err := h.store.Save(f.(io.Reader), fh.Filename, progressCh)
	if err != nil {
		return UploadResult{Error: err.Error()}
	}

	img := &database.Image{
		Filename: fh.Filename,
		WebpPath: result.WebpPath,
		OrigPath: result.OrigPath,
		IsGif:    result.IsGif,
		Width:    result.Width,
		Height:   result.Height,
		Size:     result.Size,
	}

	id, err := h.db.InsertImage(img)
	if err != nil {
		return UploadResult{Error: err.Error()}
	}

	for _, tagName := range tags {
		tagID, err := h.db.GetOrCreateTag(tagName)
		if err != nil {
			continue
		}
		h.db.AddTagToImage(id, tagID)
	}

	return UploadResult{
		Success:  true,
		ID:       id,
		Filename: fh.Filename,
		WebpURL:  "/" + result.WebpPath,
		OrigURL:  "/" + result.OrigPath,
		Tags:     tags,
	}
}

// GetImage returns metadata for a single image by ID.
func (h *Handler) GetImage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	img, err := h.db.GetImageByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, toResp(img))
}
