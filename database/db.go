package database

import (
	"database/sql"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	*sql.DB
}

type Image struct {
	ID        int64     `json:"id"`
	Filename  string    `json:"filename"`
	WebpPath  string    `json:"webp_path"`
	OrigPath  string    `json:"orig_path"`
	IsGif     bool      `json:"is_gif"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Size      int64     `json:"size"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

type Tag struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func Init(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL,
		webp_path TEXT NOT NULL,
		orig_path TEXT NOT NULL,
		is_gif BOOLEAN DEFAULT 0,
		width INTEGER DEFAULT 0,
		height INTEGER DEFAULT 0,
		size INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS tags (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL
	);
	CREATE TABLE IF NOT EXISTS image_tags (
		image_id INTEGER NOT NULL,
		tag_id INTEGER NOT NULL,
		PRIMARY KEY (image_id, tag_id),
		FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE,
		FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_image_tags_image ON image_tags(image_id);
	CREATE INDEX IF NOT EXISTS idx_image_tags_tag ON image_tags(tag_id);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &DB{db}, nil
}

func (db *DB) InsertImage(img *Image) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO images (filename, webp_path, orig_path, is_gif, width, height, size) VALUES (?,?,?,?,?,?,?)`,
		img.Filename, img.WebpPath, img.OrigPath, img.IsGif, img.Width, img.Height, img.Size,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) GetOrCreateTag(name string) (int64, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
	if err == sql.ErrNoRows {
		res, e := db.Exec(`INSERT INTO tags (name) VALUES (?)`, name)
		if e != nil {
			return 0, e
		}
		return res.LastInsertId()
	}
	return id, err
}

func (db *DB) AddTagToImage(imageID, tagID int64) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO image_tags (image_id, tag_id) VALUES (?,?)`, imageID, tagID)
	return err
}

func (db *DB) SetImageTags(imageID int64, tags []string) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM images WHERE id = ?`, imageID).Scan(&exists); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`DELETE FROM image_tags WHERE image_id = ?`, imageID); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(tags))
	cleaned := make([]string, 0, len(tags))
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags (name) VALUES (?)`, name); err != nil {
			return nil, err
		}
		var tagID int64
		if err := tx.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&tagID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO image_tags (image_id, tag_id) VALUES (?,?)`, imageID, tagID); err != nil {
			return nil, err
		}
		cleaned = append(cleaned, name)
	}

	if _, err := tx.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM image_tags)`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cleaned, nil
}

func (db *DB) GetImages(tags []string, page, limit int) ([]*Image, int, error) {
	offset := (page - 1) * limit
	var query string
	var args []interface{}
	var countArgs []interface{}

	if len(tags) > 0 {
		placeholders := ""
		for i, t := range tags {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, t)
			countArgs = append(countArgs, t)
		}
		nTags := len(tags)
		baseWhere := `
			FROM images i
			WHERE (
				SELECT COUNT(DISTINCT t.name) FROM image_tags it
				JOIN tags t ON t.id = it.tag_id
				WHERE it.image_id = i.id AND t.name IN (` + placeholders + `)
			) = ?`
		args = append(args, nTags)
		countArgs = append(countArgs, nTags)
		query = `SELECT i.id, i.filename, i.webp_path, i.orig_path, i.is_gif, i.width, i.height, i.size, i.created_at ` +
			baseWhere + ` ORDER BY i.created_at DESC, i.id DESC LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
		countQuery := `SELECT COUNT(*) ` + baseWhere
		var total int
		if err := db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
			return nil, 0, err
		}
		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, 0, err
		}
		defer rows.Close()
		images, err := scanImages(db, rows)
		return images, total, err
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM images`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(`SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at FROM images ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	images, err := scanImages(db, rows)
	return images, total, err
}

func scanImages(db *DB, rows *sql.Rows) ([]*Image, error) {
	var images []*Image
	for rows.Next() {
		img := &Image{}
		if err := rows.Scan(&img.ID, &img.Filename, &img.WebpPath, &img.OrigPath, &img.IsGif, &img.Width, &img.Height, &img.Size, &img.CreatedAt); err != nil {
			return nil, err
		}
		tags, err := db.GetImageTags(img.ID)
		if err != nil {
			return nil, err
		}
		img.Tags = tags
		images = append(images, img)
	}
	return images, rows.Err()
}

func (db *DB) GetImageTags(imageID int64) ([]string, error) {
	rows, err := db.Query(`SELECT t.name FROM tags t JOIN image_tags it ON t.id = it.tag_id WHERE it.image_id = ?`, imageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, nil
}

// GetRandomImage selects a random image excluding a specific ID to prevent
// getting the same image twice in a row. excludeID=0 means no exclusion.
func (db *DB) GetRandomImage(tags []string, excludeID int64) (*Image, error) {
	var query string
	var args []interface{}

	if len(tags) > 0 {
		placeholders := ""
		for i, t := range tags {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, t)
		}
		nTags := len(tags)
		args = append(args, nTags)

		excludeClause := ""
		if excludeID > 0 {
			excludeClause = " AND i.id != ?"
			args = append(args, excludeID)
		}

		query = `SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at
			FROM images i
			WHERE (
				SELECT COUNT(DISTINCT t.name) FROM image_tags it JOIN tags t ON t.id = it.tag_id
				WHERE it.image_id = i.id AND t.name IN (` + placeholders + `)
			) = ?` + excludeClause + `
			ORDER BY RANDOM() LIMIT 1`
	} else {
		excludeClause := ""
		if excludeID > 0 {
			excludeClause = "WHERE id != ? "
			args = append(args, excludeID)
		}
		query = `SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at
			FROM images ` + excludeClause + `ORDER BY RANDOM() LIMIT 1`
	}

	img := &Image{}
	err := db.QueryRow(query, args...).Scan(
		&img.ID, &img.Filename, &img.WebpPath, &img.OrigPath,
		&img.IsGif, &img.Width, &img.Height, &img.Size, &img.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	tags2, _ := db.GetImageTags(img.ID)
	img.Tags = tags2
	return img, nil
}

func (db *DB) GetAllTags() ([]*Tag, error) {
	rows, err := db.Query(`
		SELECT t.id, t.name, COUNT(it.image_id) as cnt
		FROM tags t
		INNER JOIN image_tags it ON t.id = it.tag_id
		GROUP BY t.id
		HAVING cnt > 0
		ORDER BY cnt DESC, t.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []*Tag
	for rows.Next() {
		tag := &Tag{}
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Count); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func (db *DB) DeleteImage(id int64) (*Image, error) {
	img := &Image{}
	err := db.QueryRow(`SELECT id, filename, webp_path, orig_path FROM images WHERE id = ?`, id).
		Scan(&img.ID, &img.Filename, &img.WebpPath, &img.OrigPath)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`DELETE FROM images WHERE id = ?`, id); err != nil {
		return nil, err
	}
	// Purge orphan tags
	_, _ = db.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM image_tags)`)
	return img, nil
}

// GetRandomImageExcluding selects a random image that is NOT in the excluded set.
// Used by the JSON multi-image endpoint to avoid duplicates within a single response.
func (db *DB) GetRandomImageExcluding(tags []string, excluded map[int64]bool) (*Image, error) {
	// Build exclusion list
	excIDs := make([]int64, 0, len(excluded))
	for id := range excluded {
		excIDs = append(excIDs, id)
	}

	var excClause string
	var excArgs []interface{}
	if len(excIDs) > 0 {
		ph := make([]string, len(excIDs))
		for i, id := range excIDs {
			ph[i] = "?"
			excArgs = append(excArgs, id)
		}
		excClause = " AND id NOT IN (" + strings.Join(ph, ",") + ")"
	}

	var query string
	var args []interface{}

	if len(tags) > 0 {
		placeholders := ""
		for i, t := range tags {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, t)
		}
		nTags := len(tags)
		args = append(args, nTags)
		args = append(args, excArgs...)

		query = `SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at
			FROM images i
			WHERE (SELECT COUNT(DISTINCT t.name) FROM image_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.image_id = i.id AND t.name IN (` + placeholders + `)) = ?` +
			excClause + ` ORDER BY RANDOM() LIMIT 1`
	} else {
		args = excArgs
		query = `SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at
			FROM images WHERE 1=1` + excClause + ` ORDER BY RANDOM() LIMIT 1`
	}

	img := &Image{}
	err := db.QueryRow(query, args...).Scan(
		&img.ID, &img.Filename, &img.WebpPath, &img.OrigPath,
		&img.IsGif, &img.Width, &img.Height, &img.Size, &img.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	t2, _ := db.GetImageTags(img.ID)
	img.Tags = t2
	return img, nil
}

// GetImageByID returns a single image record by its primary key.
func (db *DB) GetImageByID(id int64) (*Image, error) {
	img := &Image{}
	err := db.QueryRow(
		`SELECT id, filename, webp_path, orig_path, is_gif, width, height, size, created_at FROM images WHERE id = ?`, id,
	).Scan(&img.ID, &img.Filename, &img.WebpPath, &img.OrigPath, &img.IsGif, &img.Width, &img.Height, &img.Size, &img.CreatedAt)
	if err != nil {
		return nil, err
	}
	tags, _ := db.GetImageTags(img.ID)
	img.Tags = tags
	return img, nil
}
