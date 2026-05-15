package config

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// Config holds all runtime-configurable parameters.
type Config struct {
	UploadPassword  string `json:"upload_password"`
	StorageDir      string `json:"storage_dir"`
	DataDir         string `json:"data_dir"`
	Port            string `json:"port"`
	RandomRateLimit int    `json:"random_rate_limit"` // max requests per IP per minute (0=disabled)
	AuthMaxAttempts int    `json:"auth_max_attempts"` // max failed auth per IP per minute (0=disabled)
}

var (
	mu      sync.RWMutex
	current *Config
	cfgPath string
)

func defaults() *Config {
	return &Config{
		UploadPassword:  "admin123",
		StorageDir:      "uploads",
		DataDir:         "data",
		Port:            "8080",
		RandomRateLimit: 100,
		AuthMaxAttempts: 10,
	}
}

// Load reads or creates config.json in dataDir.
func Load(dataDir string) *Config {
	cfgPath = dataDir + "/config.json"
	cfg := defaults()

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			if werr := writeFile(cfg); werr != nil {
				log.Printf("config: could not write default config: %v", werr)
			} else {
				log.Printf("config: created default config at %s", cfgPath)
			}
		} else {
			log.Printf("config: read error (%v), using defaults", err)
		}
	} else {
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Printf("config: parse error (%v), using defaults", err)
		}
	}

	// Env overrides keep backward compat
	if v := os.Getenv("UPLOAD_PASSWORD"); v != "" {
		cfg.UploadPassword = v
	}
	if v := os.Getenv("PORT"); v != "" {
		cfg.Port = v
	}

	// Sanity clamps
	if cfg.UploadPassword == "" {
		cfg.UploadPassword = "admin123"
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = "uploads"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = dataDir
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.RandomRateLimit < 0 {
		cfg.RandomRateLimit = 0
	}
	if cfg.AuthMaxAttempts < 0 {
		cfg.AuthMaxAttempts = 0
	}

	mu.Lock()
	current = cfg
	mu.Unlock()

	log.Printf("config: loaded (port=%s, storage=%s, random_limit=%d/min, auth_max=%d/min)",
		cfg.Port, cfg.StorageDir, cfg.RandomRateLimit, cfg.AuthMaxAttempts)
	return cfg
}

// Get returns the current config (thread-safe).
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

func writeFile(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0644)
}
