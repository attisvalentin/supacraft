package tenant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A tenant is disabled by a marker file next to its config, not by editing
// the config, so a later apply or deploy doesn't silently re-enable it.
func disabledPath(dataDir, id string) string {
	return filepath.Join(dataDir, "tenants", id+".disabled")
}

// SetDisabled turns a tenant off (503 for every request, no cron) or on.
func SetDisabled(dataDir, id string, disabled bool) error {
	if _, err := os.Stat(filepath.Join(dataDir, "tenants", id+".json")); err != nil {
		return ErrTenantNotFound
	}
	p := disabledPath(dataDir, id)
	if !disabled {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, nil, 0o600)
}

func isDisabled(dataDir, id string) bool {
	_, err := os.Stat(disabledPath(dataDir, id))
	return err == nil
}

// TenantInfo summarizes one tenant in a data directory.
type TenantInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Disabled  bool   `json:"disabled"`
	Tables    int    `json:"tables"`
	Functions int    `json:"functions"`
	Cronjobs  int    `json:"cronjobs"`
	DBBytes   int64  `json:"db_bytes"`
	UpdatedAt string `json:"updated_at"`
}

// ListTenants describes every tenant in dataDir, sorted by id.
func ListTenants(ctx context.Context, dataDir string) ([]TenantInfo, error) {
	store := &FileStore{dir: filepath.Join(dataDir, "tenants")}
	ids, err := store.ScanTenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	out := make([]TenantInfo, 0, len(ids))
	for _, id := range ids {
		info := TenantInfo{ID: id, Disabled: isDisabled(dataDir, id)}
		if cfg, err := store.GetTenantConfig(ctx, id); err == nil {
			info.Name = cfg.DisplayName
			info.Tables = len(cfg.Schema.Tables)
			info.Functions = len(cfg.Functions)
			info.Cronjobs = len(cfg.Cronjobs)
			info.UpdatedAt = cfg.UpdatedAt
			if st, err := os.Stat(filepath.Join(dataDir, cfg.DBName+".db")); err == nil {
				info.DBBytes = st.Size()
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// RemoveTenant stops serving a tenant by deleting its config. With purge it
// also deletes the tenant's database and files under storagePath; without
// it the data stays and a later apply brings the tenant back.
func RemoveTenant(ctx context.Context, dataDir, storagePath, id string, purge bool) error {
	store := &FileStore{dir: filepath.Join(dataDir, "tenants")}
	cfg, err := store.GetTenantConfig(ctx, id)
	if err != nil {
		return err
	}
	if err := os.Remove(store.tenantPath(id)); err != nil {
		return err
	}
	os.Remove(disabledPath(dataDir, id))
	if !purge {
		return nil
	}
	dbName := cfg.DBName
	if dbName == "" || strings.ContainsAny(dbName, `/\.`) {
		dbName = id
	}
	for _, suffix := range []string{".db", ".db-wal", ".db-shm"} {
		if err := os.Remove(filepath.Join(dataDir, dbName+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete database: %w", err)
		}
	}
	if storagePath != "" {
		if err := os.RemoveAll(filepath.Join(storagePath, id)); err != nil {
			return fmt.Errorf("delete files: %w", err)
		}
	}
	return nil
}
