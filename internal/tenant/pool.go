package tenant

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB with pgx-style method names for call-site brevity.
// Query/Exec/QueryRow accept context.Context as the first
// argument and delegate to the *Context variants on *sql.DB.
type DB struct {
	*sql.DB
}

func (d *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, query, args...)
}

func (d *DB) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, query, args...)
}

func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, query, args...)
}

// Querier is what pipeline steps run SQL through: a *DB, or a *Tx inside a
// transaction step.
type Querier interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Tx wraps *sql.Tx with the same method names as DB.
type Tx struct {
	*sql.Tx
}

func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, query, args...)
}

func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, query, args...)
}

func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, query, args...)
}

// Begin starts a transaction. Each tenant pool has a single connection, so
// nothing else can use the database until the transaction ends.
func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx}, nil
}

type poolEntry struct {
	db *DB
	// lastAccessed is unix-nano, accessed from both request goroutines and the
	// evictor goroutine, so it must be touched atomically.
	lastAccessed atomic.Int64
}

func (e *poolEntry) touch() { e.lastAccessed.Store(time.Now().UnixNano()) }

// PoolManager maintains one *DB per physical database file, keyed by db_name.
// Each tenant's data lives in {DataDir}/{dbName}.db. SQLite serializes writers so
// MaxOpenConns is capped at 1 to avoid SQLITE_BUSY noise.
type PoolManager struct {
	pools   sync.Map
	dataDir string
}

func NewPoolManager(cfg *config.Config) *PoolManager {
	return &PoolManager{dataDir: cfg.DataDir}
}

// GetPool returns (and lazily opens) the *DB for the given tenant.
//
// The pool is keyed by the physical database file (db_name), NOT by
// cfg.Schema.TenantID: compiled configs leave schema.tenant_id empty, so
// keying on it collapsed every tenant onto a single shared connection (the ""
// key) — a cross-tenant data-isolation bug. db_name is unique per tenant file.
func (pm *PoolManager) GetPool(ctx context.Context, cfg *TenantConfig) (*DB, error) {
	dbName := cfg.DBName
	if dbName == "" {
		dbName = cfg.Schema.TenantID
	}
	if dbName == "" {
		// Never fall back to the "" catch-all key — that is the very bug this
		// keying fixes. A config with neither db_name nor tenant_id is invalid.
		return nil, fmt.Errorf("tenant config has no db_name")
	}

	if val, ok := pm.pools.Load(dbName); ok {
		entry := val.(*poolEntry)
		entry.touch()
		return entry.db, nil
	}

	dsn := filepath.Join(pm.dataDir, dbName+".db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=synchronous(normal)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxIdleTime(30 * time.Minute)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to ping sqlite: %w", err)
	}

	entry := &poolEntry{db: &DB{DB: sqlDB}}
	entry.touch()
	// Another goroutine may have opened the same db concurrently; if so, keep the
	// existing pool and discard ours to avoid leaking a connection.
	if actual, loaded := pm.pools.LoadOrStore(dbName, entry); loaded {
		sqlDB.Close()
		existing := actual.(*poolEntry)
		existing.touch()
		return existing.db, nil
	}
	return entry.db, nil
}

// Retain closes the pools of databases not in keep, e.g. tenants removed
// from a central data directory, so a purged database file isn't kept open.
func (pm *PoolManager) Retain(keep map[string]bool) {
	pm.pools.Range(func(key, value any) bool {
		// Delete first, so GetPool can't hand out the entry being closed.
		if !keep[key.(string)] && pm.pools.CompareAndDelete(key, value) {
			if err := value.(*poolEntry).db.Close(); err != nil {
				slog.Warn("tenant pool: close failed", "db", key, "err", err)
			}
		}
		return true
	})
}

// Drop closes the pool of one database, e.g. before its file is deleted.
func (pm *PoolManager) Drop(dbName string) {
	if val, ok := pm.pools.LoadAndDelete(dbName); ok {
		if err := val.(*poolEntry).db.Close(); err != nil {
			slog.Warn("tenant pool: close failed", "db", dbName, "err", err)
		}
	}
}

// StartEvictor closes pools that have been idle longer than 30 minutes.
func (pm *PoolManager) StartEvictor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pm.pools.Range(func(key, value any) bool {
					entry := value.(*poolEntry)
					last := time.Unix(0, entry.lastAccessed.Load())
					if time.Since(last) > 30*time.Minute && pm.pools.CompareAndDelete(key, value) {
						if err := entry.db.Close(); err != nil {
							slog.Warn("tenant pool: close failed", "db", key, "err", err)
						}
					}
					return true
				})
			}
		}
	}()
}
