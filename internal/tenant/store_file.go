package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/envref"
	"github.com/attisvalentin/supacraft/internal/secrets"
)

var ErrTenantNotFound = errors.New("tenant not found")

// Store is the interface consumed by the cache, cronjob manager, and middleware.
// It is implemented by FileStore.
type Store interface {
	GetTenantConfig(ctx context.Context, tenantID string) (*TenantConfig, error)
	ScanTenantIDs(ctx context.Context) ([]string, error)
}

var tenantIDRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// ValidTenantID reports whether id is a tenant id the CLI can produce
// (project.Slug); only such ids reach the filesystem.
func ValidTenantID(id string) bool { return tenantIDRe.MatchString(id) }

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
	if !ValidTenantID(tenantID) {
		return nil, ErrTenantNotFound
	}
	data, err := os.ReadFile(s.tenantPath(tenantID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read tenant file: %w", err)
	}

	if bytes.Contains(data, []byte("${")) {
		if data, err = s.resolveSecrets(tenantID, data); err != nil {
			return nil, err
		}
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
	// The database file is named after the tenant. Requiring that keeps two
	// tenants from sharing one database (and a purge from deleting another
	// tenant's data) and keeps path-like names out of the filesystem.
	switch cfg.DBName {
	case "":
		cfg.DBName = tenantID
	case tenantID:
	default:
		return nil, fmt.Errorf("tenant %s: db_name %q must equal the tenant id", tenantID, cfg.DBName)
	}
	if isDisabled(filepath.Dir(s.dir), tenantID) {
		cfg.Enabled = false
	}
	return &cfg, nil
}

// resolveSecrets replaces "${NAME}" references in api_settings with the
// tenant's server-side secrets (configs applied to a central data directory
// keep references instead of values). Unknown names become "".
func (s *FileStore) resolveSecrets(tenantID string, data []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse tenant config: %w", err)
	}
	vals, err := secrets.New(filepath.Dir(s.dir)).Values(tenantID)
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	if settings, ok := raw["api_settings"]; ok {
		envref.Resolve(settings, func(name string) (string, bool) {
			v, ok := vals[name]
			return v, ok
		}, nil)
	}
	return json.Marshal(raw)
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
