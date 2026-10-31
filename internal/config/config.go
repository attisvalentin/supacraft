package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv string
	// Host is the interface the API listens on ("" = all interfaces).
	Host        string
	Port        string
	DataDir     string
	ErrorLogDB  string
	StoragePath string
	MaxFileSize int64
	// MaxBodySize caps JSON request bodies (file uploads use MaxFileSize).
	MaxBodySize  int64
	StudioSecret string
	// AdminSecret signs remote management requests (secrets, deploys) to a
	// central server. Admin endpoints are off when it is empty.
	AdminSecret string
	// Central is set by serve --data-dir: the data directory holds many
	// projects' tenants and their server-side secrets.
	Central     bool
	DisableCron bool
	// TrustProxy makes rate limits key on X-Real-IP / X-Forwarded-For.
	TrustProxy bool
	// DefaultTenant is used when a request has no X-Tenant-ID header, so a
	// single-project server can be called without it.
	DefaultTenant string
}

func Load() (*Config, error) {
	godotenv.Load()

	cfg := &Config{
		AppEnv:  os.Getenv("APP_ENV"),
		Host:    os.Getenv("HOST"),
		Port:    os.Getenv("PORT"),
		DataDir: os.Getenv("DATA_DIR"),
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	if abs, err := filepath.Abs(cfg.DataDir); err == nil {
		cfg.DataDir = abs
	}

	cfg.StudioSecret = os.Getenv("STUDIO_SECRET")
	cfg.AdminSecret = os.Getenv("SUPACRAFT_ADMIN_SECRET")

	// ErrorLogDB and StoragePath default to DataDir-anchored locations so all
	// server state lives under one root regardless of the process's CWD.
	cfg.ErrorLogDB = os.Getenv("ERROR_LOG_DB")
	if cfg.ErrorLogDB == "" {
		cfg.ErrorLogDB = filepath.Join(cfg.DataDir, "errors.db")
	}
	if abs, err := filepath.Abs(cfg.ErrorLogDB); err == nil {
		cfg.ErrorLogDB = abs
	}

	cfg.StoragePath = os.Getenv("STORAGE_PATH")
	if cfg.StoragePath == "" {
		cfg.StoragePath = filepath.Join(cfg.DataDir, "storage")
	}
	if abs, err := filepath.Abs(cfg.StoragePath); err == nil {
		cfg.StoragePath = abs
	}

	cfg.MaxBodySize = 1 << 20
	if v := os.Getenv("MAX_BODY_SIZE"); v != "" {
		size, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MAX_BODY_SIZE must be a number: %w", err)
		}
		cfg.MaxBodySize = size
	}

	cfg.MaxFileSize = 100 * 1024 * 1024
	if maxSize := os.Getenv("MAX_FILE_SIZE"); maxSize != "" {
		size, err := strconv.ParseInt(maxSize, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MAX_FILE_SIZE must be a number: %w", err)
		}
		cfg.MaxFileSize = size
	}

	cfg.DefaultTenant = os.Getenv("DEFAULT_TENANT")

	// Only trust X-Forwarded-For / X-Real-IP when a reverse proxy sets them.
	cfg.TrustProxy = isTrue(os.Getenv("TRUST_PROXY"))

	cfg.DisableCron = false
	if v := os.Getenv("DISABLE_CRON"); v != "" {
		cfg.DisableCron = isTrue(v)
	}

	return cfg, nil
}

// defaultDataDir returns a stable, CWD-independent location for tenant configs
// and SQLite files (~/.supacraft/data) when DATA_DIR is unset. The CLI always
// sets it to the project's .supacraft directory. A
// relative default like "./data" resolves against the launching process's
// working directory, so a server started without DATA_DIR (orphaned process,
// manual run, direct systemd ExecStart) would silently create and serve a
// second, empty data dir. Falls back to "./data" only if the home directory
// cannot be determined.
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".supacraft", "data")
}

func isTrue(v string) bool {
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}
