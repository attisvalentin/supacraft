package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
)

var ErrTenantNotFound = errors.New("tenant not found")

// Store is the interface consumed by the cache, cronjob manager, and middleware.
// It is implemented by FileStore.
type Store interface {
	GetTenantConfig(ctx context.Context, tenantID string) (*TenantConfig, error)
	ScanTenantIDs(ctx context.Context) ([]string, error)
}

// FileStore reads tenant configs from {DataDir}/tenants/{tenantID}.json.
type FileStore struct {
	dir string
}

func NewFileStore(cfg *config.Config) (*FileStore, error) {
	dir := filepath.Join(cfg.DataDir, "tenants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create tenants dir %s: %w", dir, err)
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) tenantPath(tenantID string) string {
	return filepath.Join(s.dir, tenantID+".json")
}

// GetTenantConfig reads {DataDir}/tenants/{tenantID}.json from disk.
func (s *FileStore) GetTenantConfig(ctx context.Context, tenantID string) (*TenantConfig, error) {
	if tenantID == "" || strings.ContainsAny(tenantID, `/\.`) {
		return nil, ErrTenantNotFound
	}
	data, err := os.ReadFile(s.tenantPath(tenantID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read tenant file: %w", err)
	}

	var cfg TenantConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse tenant config: %w", err)
	}
	// Compiled configs don't carry schema.tenant_id; backfill it from the
	// resolved tenant id so any code reading Schema.TenantID is correct. The pool
	// keys on db_name regardless (see PoolManager.GetPool).
	if cfg.Schema.TenantID == "" {
		cfg.Schema.TenantID = tenantID
	}
	if isDisabled(filepath.Dir(s.dir), tenantID) {
		cfg.Enabled = false
	}
	return &cfg, nil
}

// ScanTenantIDs lists all tenants by scanning the tenants directory.
func (s *FileStore) ScanTenantIDs(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tenants dir: %w", err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	return ids, nil
}
