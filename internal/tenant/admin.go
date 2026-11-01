package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/attisvalentin/supacraft/internal/storage"
)

// A tenant is disabled by a marker file next to its config, not by editing
// the config, so a later apply or deploy doesn't silently re-enable it.
func disabledPath(dataDir, id string) string {
	return filepath.Join(dataDir, "tenants", id+".disabled")
}

// SetDisabled turns a tenant off (503 for every request, no cron) or on.
func SetDisabled(dataDir, id string, disabled bool) error {
	if !ValidTenantID(id) {
		return ErrTenantNotFound
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tenants", id+".json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrTenantNotFound
		}
		return err
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

// Serving reports whether a tenant has a config and is not disabled.
func Serving(dataDir, id string) bool {
	if !ValidTenantID(id) || isDisabled(dataDir, id) {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, "tenants", id+".json"))
	return err == nil
}

// isDisabled fails closed: a marker that can't be checked counts as set.
func isDisabled(dataDir, id string) bool {
	_, err := os.Stat(disabledPath(dataDir, id))
	return !errors.Is(err, os.ErrNotExist)
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

// RemoveResult reports what RemoveTenant deleted.
type RemoveResult struct {
	ID     string `json:"id"`
	Purged bool   `json:"purged"`
	// FilesDriver is the tenant's remote storage driver, if any ("" for
	// local disk); FilesDeleted counts the uploads deleted there.
	FilesDriver  string `json:"files_driver,omitempty"`
	FilesDeleted int    `json:"files_deleted"`
}

// RemoveTenant stops serving a tenant by deleting its config. With purge it
// also deletes the tenant's uploads (local, or the ones recorded in
// storage.FilesTable on a remote driver), database and secrets; without it
// the data stays and a later apply brings the tenant back. During a purge
// the tenant is disabled and its config goes last, so a failed purge serves
// nothing and can be retried.
func RemoveTenant(ctx context.Context, dataDir, storagePath, id string, purge bool) (*RemoveResult, error) {
	store := &FileStore{dir: filepath.Join(dataDir, "tenants")}
	cfg, err := store.GetTenantConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	res := &RemoveResult{ID: id, Purged: purge}
	if purge {
		if err := os.WriteFile(disabledPath(dataDir, id), nil, 0o600); err != nil {
			return nil, fmt.Errorf("disable tenant: %w", err)
		}
		base := filepath.Join(dataDir, cfg.DBName)
		if st := cfg.APISettings.Storage; st != nil && st.Driver != "" && st.Driver != "local" {
			res.FilesDriver = st.Driver
			if res.FilesDeleted, err = purgeRemoteFiles(ctx, base+".db", st.Driver, st.Config); err != nil {
				return nil, fmt.Errorf("delete files in %s storage (nothing else was deleted, retry): %w", st.Driver, err)
			}
		}
		for _, suffix := range []string{".db", ".db-wal", ".db-shm"} {
			if err := os.Remove(base + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("delete database: %w", err)
			}
		}
		if storagePath != "" {
			if err := os.RemoveAll(filepath.Join(storagePath, id)); err != nil {
				return nil, fmt.Errorf("delete files: %w", err)
			}
		}
		if err := secrets.New(dataDir).Remove(id); err != nil {
			return nil, fmt.Errorf("delete secrets: %w", err)
		}
	}
	if err := os.Remove(store.tenantPath(id)); err != nil {
		return nil, err
	}
	if err := os.Remove(disabledPath(dataDir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove disabled marker: %w", err)
	}
	return res, nil
}

// newStorageDriver is replaced in tests.
var newStorageDriver = storage.NewDriver

// purgeRemoteFiles deletes the uploads recorded in the tenant database from
// its remote storage. Files already gone count as deleted, so a retry after
// a partial failure finishes the job.
func purgeRemoteFiles(ctx context.Context, dbPath, driverName string, driverCfg map[string]any) (int, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	paths, err := storage.TrackedUploads(ctx, db)
	if err != nil || len(paths) == 0 {
		return 0, err
	}
	driver, err := newStorageDriver(driverName, driverCfg, "")
	if err != nil {
		return 0, err
	}
	for i, p := range paths {
		if err := driver.Delete(ctx, p); err != nil {
			if exists, xerr := driver.Exists(ctx, p); xerr != nil || exists {
				return i, fmt.Errorf("%s: %w", p, err)
			}
		}
	}
	return len(paths), nil
}
