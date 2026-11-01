package tenant

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/config"
)

// TestGetPool_IsolatesByDBName guards the cross-tenant isolation fix: two tenant
// configs with EMPTY Schema.TenantID (as compiled configs always have) but distinct
// db_name must get separate pools / separate database files. Before the fix both
// collapsed onto the "" pool key and shared one connection.
func TestGetPool_IsolatesByDBName(t *testing.T) {
	dir := t.TempDir()
	pm := NewPoolManager(&config.Config{DataDir: dir})
	ctx := context.Background()

	cfgA := &TenantConfig{DBName: "tenant_a"} // Schema.TenantID is "" (zero value)
	cfgB := &TenantConfig{DBName: "tenant_b"}

	dbA, err := pm.GetPool(ctx, cfgA)
	if err != nil {
		t.Fatalf("GetPool A: %v", err)
	}
	dbB, err := pm.GetPool(ctx, cfgB)
	if err != nil {
		t.Fatalf("GetPool B: %v", err)
	}

	if dbA == dbB {
		t.Fatal("tenant A and B share the same *DB — cross-tenant isolation broken")
	}

	// Write into A's database; B must not see it (separate files).
	if _, err := dbA.Exec(ctx, `CREATE TABLE t (id INTEGER); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatalf("write to A: %v", err)
	}
	var n int
	err = dbB.QueryRow(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='t'`).Scan(&n)
	if err != nil {
		t.Fatalf("query B: %v", err)
	}
	if n != 0 {
		t.Fatalf("tenant B sees tenant A's table — data leaked across tenants")
	}

	// Re-fetching A returns the same cached pool (keyed by db_name).
	dbA2, err := pm.GetPool(ctx, cfgA)
	if err != nil {
		t.Fatalf("GetPool A again: %v", err)
	}
	if dbA2 != dbA {
		t.Fatal("expected cached pool for the same db_name")
	}
}

// TestGetPool_RejectsEmptyDBName ensures we never regress to the shared "" key.
func TestGetPool_RejectsEmptyDBName(t *testing.T) {
	pm := NewPoolManager(&config.Config{DataDir: t.TempDir()})
	if _, err := pm.GetPool(context.Background(), &TenantConfig{}); err == nil {
		t.Fatal("expected error for config with no db_name / tenant_id")
	}
}

// A database applied by an older version gets the newer auth columns and
// token table when it is opened, so an upgraded server works before the
// next deploy.
func TestGetPool_MigratesOldAuthSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	old, err := sql.Open("sqlite", filepath.Join(dir, "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, password TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	pm := NewPoolManager(&config.Config{DataDir: dir})
	db, err := pm.GetPool(ctx, &TenantConfig{DBName: "shop", AuthEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var active, verified, version int
	if err := db.QueryRow(ctx, `SELECT COALESCE("is_active", 1), COALESCE("email_verified", 0), COALESCE("session_version", 0) FROM users`).Scan(&active, &verified, &version); err != nil && err != sql.ErrNoRows {
		t.Fatalf("auth columns missing: %v", err)
	}
	if _, err := db.Exec(ctx, `SELECT COUNT(*) FROM "_supacraft_auth_tokens"`); err != nil {
		t.Fatalf("token table missing: %v", err)
	}
}
