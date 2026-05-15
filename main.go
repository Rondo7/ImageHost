package main

import (
	"log"
	"os"

	"imagehost/config"
	"imagehost/database"
	"imagehost/handlers"
	"imagehost/middleware"
	"imagehost/storage"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	// ── Bootstrap data dir first so config can be written there ──────────
	dataDir := "data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal("cannot create data dir:", err)
	}

	// ── Load config ───────────────────────────────────────────────────────
	cfg := config.Load(dataDir)

	// ── Create required directories ───────────────────────────────────────
	dirs := []string{
		cfg.StorageDir + "/original",
		cfg.StorageDir + "/webp",
		cfg.StorageDir + "/gif",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			log.Fatal("cannot create storage dir:", err)
		}
	}

	// ── Init database ─────────────────────────────────────────────────────
	db, err := database.Init(cfg.DataDir + "/imagehost.db")
	if err != nil {
		log.Fatal("DB init failed:", err)
	}
	defer db.Close()

	// ── Init storage ──────────────────────────────────────────────────────
	store := storage.New(cfg.StorageDir)

	// ── Init handlers ─────────────────────────────────────────────────────
	h := handlers.New(db, store)

	// ── Router ────────────────────────────────────────────────────────────
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length", "X-Image-Id", "X-Image-Tags"},
		AllowCredentials: true,
	}))

	// Static files (serve uploads from the configured storage dir)
	r.Static("/uploads", cfg.StorageDir)
	r.StaticFile("/", "./frontend/index.html")
	r.Static("/frontend", "./frontend")

	// ── Public API ────────────────────────────────────────────────────────
	randomRL := middleware.RandomRateLimit(func() int {
		return config.Get().RandomRateLimit
	})
	r.GET("/api/random", randomRL, h.RandomImage)
	r.GET("/api/images", h.ListImages)
	r.GET("/api/tags", h.ListTags)

	// ── Auth-required API ─────────────────────────────────────────────────
	authMW := handlers.AuthMiddleware()

	// Frontend upload (multipart + SSE progress)
	r.POST("/api/upload", authMW, h.Upload)
	r.DELETE("/api/images/:id", authMW, h.DeleteImage)

	// Management API (same auth, clean REST paths)
	mgmt := r.Group("/api/v1")
	mgmt.Use(authMW)
	{
		mgmt.POST("/images", h.APIUpload)         // upload one or more images
		mgmt.DELETE("/images/:id", h.DeleteImage) // delete by id
		mgmt.GET("/images", h.ListImages)         // list with pagination
		mgmt.GET("/images/:id", h.GetImage)       // get single image info
	}

	// SSE progress
	r.GET("/api/progress/:id", h.ProgressStream)

	log.Printf("ImageHost starting on :%s", cfg.Port)
	log.Printf("Storage: %s | DB: %s/imagehost.db", cfg.StorageDir, cfg.DataDir)
	log.Printf("Random rate limit: %d req/min/ip | Auth max attempts: %d/min/ip",
		cfg.RandomRateLimit, cfg.AuthMaxAttempts)
	r.Run(":" + cfg.Port)
}
