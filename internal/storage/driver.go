package storage

import (
	"context"
	"fmt"
	"io"
)

// StorageDriver is the interface all storage backends implement.
type StorageDriver interface {
	Put(ctx context.Context, path string, reader io.Reader, size int64) error
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	Exists(ctx context.Context, path string) (bool, error)
}

// NewDriver creates a StorageDriver for the given driver name.
// localBasePath is used only by the "local" driver (STORAGE_PATH/tenant-id).
func NewDriver(driverName string, cfg map[string]any, localBasePath string) (StorageDriver, error) {
	switch driverName {
	case "local", "":
		return NewLocalDriver(localBasePath), nil
	case "s3":
		return NewS3Driver(cfg)
	case "ftp":
		return NewFTPDriver(cfg)
	case "sftp":
		return NewSFTPDriver(cfg)
	case "gcs":
		return NewGCSDriver(cfg)
	case "azure":
		return NewAzureDriver(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", driverName)
	}
}

// configString extracts a string from a config map, returning fallback if missing.
func configString(cfg map[string]any, key, fallback string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return fallback
}

// configInt extracts an int from a config map, returning fallback if missing.
func configInt(cfg map[string]any, key string, fallback int) int {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return fallback
}

// configBool extracts a bool from a config map, returning fallback if missing.
func configBool(cfg map[string]any, key string, fallback bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return fallback
}

// requireString extracts a required string from config, returning an error if missing.
func requireString(cfg map[string]any, key string) (string, error) {
	s := configString(cfg, key, "")
	if s == "" {
		return "", fmt.Errorf("missing required config key: %s", key)
	}
	return s, nil
}
