package storage

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chai2010/webp"
)

type Storage struct {
	BaseDir string
}

type SaveResult struct {
	OrigPath string
	WebpPath string
	IsGif    bool
	Width    int
	Height   int
	Size     int64
	Filename string
}

func New(baseDir string) *Storage {
	return &Storage{BaseDir: baseDir}
}

func (s *Storage) Save(src io.Reader, originalName string, progressCh chan<- string) (*SaveResult, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	timestamp := time.Now().UnixNano()
	baseName := fmt.Sprintf("%d", timestamp)

	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	result := &SaveResult{}
	result.Size = int64(len(data))

	sendProgress := func(msg string) {
		if progressCh != nil {
			select {
			case progressCh <- msg:
			default:
			}
		}
	}

	if ext == ".gif" {
		result.IsGif = true
		gifPath := filepath.Join(s.BaseDir, "gif", baseName+".gif")
		if err := os.WriteFile(gifPath, data, 0644); err != nil {
			return nil, err
		}
		result.OrigPath = gifPath
		result.WebpPath = gifPath
		result.Filename = baseName + ".gif"
		sendProgress(`{"stage":"done","progress":100}`)
		return result, nil
	}

	sendProgress(`{"stage":"saving_original","progress":20}`)
	origExt := ext
	if origExt == "" {
		origExt = ".jpg"
	}
	origPath := filepath.Join(s.BaseDir, "original", baseName+origExt)
	if err := os.WriteFile(origPath, data, 0644); err != nil {
		return nil, err
	}
	result.OrigPath = origPath
	result.Filename = baseName + origExt

	sendProgress(`{"stage":"decoding","progress":40}`)
	img, err := decodeImage(data, ext)
	if err != nil {
		result.WebpPath = origPath
		sendProgress(`{"stage":"done","progress":100}`)
		return result, nil
	}

	bounds := img.Bounds()
	result.Width = bounds.Max.X
	result.Height = bounds.Max.Y

	sendProgress(`{"stage":"converting","progress":60}`)
	webpPath := filepath.Join(s.BaseDir, "webp", baseName+".webp")
	wf, err := os.Create(webpPath)
	if err != nil {
		result.WebpPath = origPath
	} else {
		opts := &webp.Options{Lossless: false, Quality: 82}
		if encErr := webp.Encode(wf, img, opts); encErr != nil {
			wf.Close()
			os.Remove(webpPath)
			result.WebpPath = origPath
		} else {
			wf.Close()
			result.WebpPath = webpPath
		}
	}

	sendProgress(`{"stage":"done","progress":100}`)
	return result, nil
}

func decodeImage(data []byte, ext string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch ext {
	case ".jpg", ".jpeg":
		return jpeg.Decode(r)
	case ".png":
		return png.Decode(r)
	default:
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}
}

func (s *Storage) Delete(origPath, webpPath string) {
	os.Remove(origPath)
	if webpPath != origPath {
		os.Remove(webpPath)
	}
}
