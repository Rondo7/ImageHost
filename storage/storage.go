package storage

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
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
	BaseDir         string
	ResizeMaxPixels int
	RejectMaxPixels int
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

func (s *Storage) SetLimits(resizeMaxPixels, rejectMaxPixels int) {
	s.ResizeMaxPixels = resizeMaxPixels
	s.RejectMaxPixels = rejectMaxPixels
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
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode gif dimensions: %w", err)
		}
		result.Width = cfg.Width
		result.Height = cfg.Height
		if err := s.checkDimensions(result.Width, result.Height); err != nil {
			return nil, err
		}

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
		os.Remove(origPath)
		return nil, fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	result.Width = bounds.Dx()
	result.Height = bounds.Dy()
	if err := s.checkDimensions(result.Width, result.Height); err != nil {
		os.Remove(origPath)
		return nil, err
	}
	encodeImg := img
	if s.ResizeMaxPixels > 0 && (result.Width > s.ResizeMaxPixels || result.Height > s.ResizeMaxPixels) {
		encodeImg = resizeImage(img, s.ResizeMaxPixels)
	}

	sendProgress(`{"stage":"converting","progress":60}`)
	webpPath := filepath.Join(s.BaseDir, "webp", baseName+".webp")
	wf, err := os.Create(webpPath)
	if err != nil {
		result.WebpPath = origPath
	} else {
		opts := &webp.Options{Lossless: false, Quality: 82}
		if encErr := webp.Encode(wf, encodeImg, opts); encErr != nil {
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

func (s *Storage) checkDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid image dimensions")
	}
	if s.RejectMaxPixels > 0 && (width > s.RejectMaxPixels || height > s.RejectMaxPixels) {
		return fmt.Errorf("image dimensions exceed %dpx limit", s.RejectMaxPixels)
	}
	return nil
}

func resizeImage(src image.Image, maxPixels int) image.Image {
	bounds := src.Bounds()
	sw := bounds.Dx()
	sh := bounds.Dy()
	if maxPixels <= 0 || (sw <= maxPixels && sh <= maxPixels) {
		return src
	}

	dw, dh := scaledSize(sw, sh, maxPixels)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := bounds.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			sx := bounds.Min.X + x*sw/dw
			dst.Set(x, y, color.RGBAModel.Convert(src.At(sx, sy)))
		}
	}
	return dst
}

func scaledSize(width, height, maxPixels int) (int, int) {
	if width >= height {
		newHeight := height * maxPixels / width
		if newHeight < 1 {
			newHeight = 1
		}
		return maxPixels, newHeight
	}
	newWidth := width * maxPixels / height
	if newWidth < 1 {
		newWidth = 1
	}
	return newWidth, maxPixels
}

func decodeImage(data []byte, ext string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch ext {
	case ".jpg", ".jpeg":
		return jpeg.Decode(r)
	case ".png":
		return png.Decode(r)
	case ".webp":
		return webp.Decode(r)
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
