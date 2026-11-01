// Package sqlitedb provisions per-tenant SQLite databases.
// Each tenant gets a file at {DataDir}/{db_name}.db. "Provisioning" reduces to:
//  1. Open (= create) the SQLite file with our PRAGMA defaults.
//  2. Run the api_config + users migrations when auth is enabled.
//  3. Insert a JWT secret if api_config is empty.
package sqlitedb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/tenant"
	_ "modernc.org/sqlite"
)

// Provisioner manages tenant SQLite files under cfg.DataDir.
type Provisioner struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Provisioner {
	return &Provisioner{cfg: cfg}
}

func (p *Provisioner) tenantDBPath(dbName string) string {
	return filepath.Join(p.cfg.DataDir, dbName+".db")
}

// Ping verifies that DataDir exists and is writable.
func (p *Provisioner) Ping(ctx context.Context) error {
	if p.cfg.DataDir == "" {
		return errors.New("data_dir is not configured")
	}
	if err := os.MkdirAll(p.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("data_dir %s not writable: %w", p.cfg.DataDir, err)
	}
	return nil
}

func (p *Provisioner) openTenantDB(dbName string) (*sql.DB, error) {
	if err := os.MkdirAll(p.cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data_dir: %w", err)
	}
	dsn := p.tenantDBPath(dbName) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// EnsureTenant creates the SQLite file if it does not exist. Returns true if
// the file is newly created (so callers can run first-publish hooks).
func (p *Provisioner) EnsureTenant(ctx context.Context, dbName string) (bool, error) {
	dbPath := p.tenantDBPath(dbName)
	_, statErr := os.Stat(dbPath)
	created := errors.Is(statErr, os.ErrNotExist)

	db, err := p.openTenantDB(dbName)
	if err != nil {
		return false, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return false, fmt.Errorf("ping new tenant db: %w", err)
	}
	return created, nil
}

// RunTenantMigrations creates the api_config and users tables used by auth.
func (p *Provisioner) RunTenantMigrations(ctx context.Context, dbName string) error {
	db, err := p.openTenantDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS api_config (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			jwt_secret      TEXT NOT NULL,
			token_expiry    INTEGER NOT NULL DEFAULT 3600,
			refresh_enabled INTEGER NOT NULL DEFAULT 1,
			refresh_expiry  INTEGER NOT NULL DEFAULT 604800,
			created_at      TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id          TEXT PRIMARY KEY,
			email       TEXT NOT NULL UNIQUE,
			password    TEXT NOT NULL,
			created_at  TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	migrations = append(migrations, tenant.AuthTokensDDL...)
	for _, sqlText := range migrations {
		if _, err := db.ExecContext(ctx, sqlText); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

// EnsureJWTSecret inserts a jwt_secret row into api_config if empty. Idempotent.
func (p *Provisioner) EnsureJWTSecret(ctx context.Context, dbName string) error {
	db, err := p.openTenantDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return fmt.Errorf("generate jwt_secret: %w", err)
	}
	jwtSecret := hex.EncodeToString(secretBytes)

	_, err = db.ExecContext(ctx,
		`INSERT INTO api_config (jwt_secret)
		 SELECT ? WHERE NOT EXISTS (SELECT 1 FROM api_config LIMIT 1)`,
		jwtSecret,
	)
	if err != nil {
		return fmt.Errorf("insert jwt_secret: %w", err)
	}
	return nil
}

// DropDatabase removes the SQLite file and its WAL/SHM siblings.
func (p *Provisioner) DropDatabase(ctx context.Context, dbName string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := p.tenantDBPath(dbName) + suffix
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}

// OpenTenant is a small public entry point used by the DDL package.
func (p *Provisioner) OpenTenant(ctx context.Context, dbName string) (*sql.DB, error) {
	db, err := p.openTenantDB(dbName)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
