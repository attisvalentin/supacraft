package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_DefaultDataDirIsCWDIndependent guards against the "two databases"
// bug: with no DATA_DIR set, the default must be an absolute, home-anchored
// path (~/.supacraft/data) — not ./data resolved against whatever CWD the process
// happens to start in (e.g. / or /root under systemd).
func TestLoad_DefaultDataDirIsCWDIndependent(t *testing.T) {
	t.Setenv("DATA_DIR", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available in this environment")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := filepath.Join(home, ".supacraft", "data")
	if cfg.DataDir != want {
		t.Fatalf("expected default data dir %q, got %q", want, cfg.DataDir)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("default data dir must be absolute, got %q", cfg.DataDir)
	}
}

func TestLoad_UsesExplicitDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", "/var/lib/supacraft")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DataDir != "/var/lib/supacraft" {
		t.Fatalf("expected explicit data dir, got %q", cfg.DataDir)
	}
}

// TestLoad_ErrorLogAndStorageAnchoredToDataDir verifies that when ERROR_LOG_DB
// and STORAGE_PATH are not set, they default to DataDir-anchored paths instead
// of CWD-relative ones — keeping all server state under one root.
func TestLoad_ErrorLogAndStorageAnchoredToDataDir(t *testing.T) {
	t.Setenv("DATA_DIR", "/var/lib/supacraft")
	t.Setenv("ERROR_LOG_DB", "")
	t.Setenv("STORAGE_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if want := "/var/lib/supacraft/errors.db"; cfg.ErrorLogDB != want {
		t.Fatalf("expected error log db %q, got %q", want, cfg.ErrorLogDB)
	}
	if want := "/var/lib/supacraft/storage"; cfg.StoragePath != want {
		t.Fatalf("expected storage path %q, got %q", want, cfg.StoragePath)
	}
}

// TestLoad_ExplicitErrorLogAndStorageWin verifies explicit env values are
// respected (and made absolute).
func TestLoad_ExplicitErrorLogAndStorageWin(t *testing.T) {
	t.Setenv("DATA_DIR", "/var/lib/supacraft")
	t.Setenv("ERROR_LOG_DB", "/var/log/supacraft/errors.db")
	t.Setenv("STORAGE_PATH", "/srv/uploads")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ErrorLogDB != "/var/log/supacraft/errors.db" {
		t.Fatalf("expected explicit error log db, got %q", cfg.ErrorLogDB)
	}
	if cfg.StoragePath != "/srv/uploads" {
		t.Fatalf("expected explicit storage path, got %q", cfg.StoragePath)
	}
}
