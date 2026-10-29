package studio

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/tenantfile"
)

const (
	testTenantID = "019dce48-0000-0000-0000-000000000001"
	testDBName   = "test_db"
)

func newTestSource(t *testing.T) *SQLiteSource {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}

	seedDB(t, filepath.Join(dir, testDBName+".db"))

	tc, err := tenantfile.New(cfg)
	if err != nil {
		t.Fatalf("tenantfile.New: %v", err)
	}
	readable := true
	payload := map[string]any{
		"db_name":      testDBName,
		"display_name": "Test Tenant",
		"schema": map[string]any{
			"tables": []any{
				map[string]any{
					"name": "widgets",
					"columns": []any{
						map[string]any{"name": "id", "type": "primary", "readable": true},
						map[string]any{"name": "name", "type": "varchar", "readable": readable},
						map[string]any{"name": "qty", "type": "integer", "readable": false},
					},
				},
			},
			"relations": []any{},
		},
	}
	if err := tc.WriteTenant(context.Background(), testTenantID, payload); err != nil {
		t.Fatalf("WriteTenant: %v", err)
	}

	src, err := NewSQLiteSource(cfg)
	if err != nil {
		t.Fatalf("NewSQLiteSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

func seedDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL, qty INTEGER)`,
		`INSERT INTO widgets (name, qty) VALUES ('alpha', 5)`,
		`INSERT INTO widgets (name, qty) VALUES ('beta', 10)`,
		`INSERT INTO widgets (name, qty) VALUES ('gamma', 2)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed exec %q: %v", s, err)
		}
	}
}

func TestListTenants(t *testing.T) {
	src := newTestSource(t)
	tenants, err := src.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("want 1 tenant, got %d", len(tenants))
	}
	got := tenants[0]
	if got.ID != testTenantID || got.DBName != testDBName || got.DisplayName != "Test Tenant" {
		t.Fatalf("unexpected tenant: %+v", got)
	}
	if got.TableCount != 1 {
		t.Fatalf("want table_count 1, got %d", got.TableCount)
	}
}

func TestGetSchema(t *testing.T) {
	src := newTestSource(t)
	schema, err := src.GetSchema(context.Background(), testTenantID)
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}
	if len(schema.Tables) != 1 || schema.Tables[0].Name != "widgets" {
		t.Fatalf("unexpected tables: %+v", schema.Tables)
	}
	cols := schema.Tables[0].Columns
	if len(cols) != 3 {
		t.Fatalf("want 3 columns, got %d", len(cols))
	}
	byName := map[string]Column{}
	for _, c := range cols {
		byName[c.Name] = c
	}
	if !byName["id"].PK {
		t.Errorf("id should be a primary key")
	}
	if byName["name"].Nullable {
		t.Errorf("name should be NOT NULL")
	}
	if byName["qty"].Readable == nil || *byName["qty"].Readable {
		t.Errorf("qty readable should be enriched to false, got %v", byName["qty"].Readable)
	}
}

func TestQueryRowsPaginationAndOrder(t *testing.T) {
	src := newTestSource(t)
	page, err := src.QueryRows(context.Background(), testTenantID, "widgets", QueryOptions{
		Limit: 2, OrderBy: "qty", Desc: true,
	})
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("want total 3, got %d", page.Total)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(page.Rows))
	}
	if page.Rows[0]["name"] != "beta" {
		t.Fatalf("want beta first (qty desc), got %v", page.Rows[0]["name"])
	}
}

func TestQueryRowsFilter(t *testing.T) {
	src := newTestSource(t)
	page, err := src.QueryRows(context.Background(), testTenantID, "widgets", QueryOptions{
		Filters: []Filter{{Column: "name", Op: "like", Value: "amm"}},
	})
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	if page.Total != 1 || page.Rows[0]["name"] != "gamma" {
		t.Fatalf("like filter should match gamma only, got total=%d rows=%+v", page.Total, page.Rows)
	}
}

func TestQueryRowsRejectsUnknownTableAndColumn(t *testing.T) {
	src := newTestSource(t)
	ctx := context.Background()

	if _, err := src.QueryRows(ctx, testTenantID, "secrets; DROP TABLE widgets", QueryOptions{}); err == nil {
		t.Error("expected error for unknown table")
	}
	if _, err := src.QueryRows(ctx, testTenantID, "widgets", QueryOptions{OrderBy: "bogus"}); err == nil {
		t.Error("expected error for unknown order column")
	}
	if _, err := src.QueryRows(ctx, testTenantID, "widgets",
		QueryOptions{Filters: []Filter{{Column: "name", Op: "; DROP", Value: "x"}}}); err == nil {
		t.Error("expected error for bad filter operator")
	}
	if _, err := src.QueryRows(ctx, testTenantID, "widgets",
		QueryOptions{Filters: []Filter{{Column: "nope", Op: "eq", Value: "x"}}}); err == nil {
		t.Error("expected error for unknown filter column")
	}
}

func TestQueryRowsReadOnly(t *testing.T) {
	src := newTestSource(t)
	db, err := src.openDB(testDBName)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO widgets (name, qty) VALUES ('delta', 1)`); err == nil {
		t.Fatal("expected write to be rejected on read-only connection")
	}
}

func TestQuoteIdent(t *testing.T) {
	if _, err := quoteIdent(`a"b`); err == nil {
		t.Error("quoteIdent should reject embedded quote")
	}
	if _, err := quoteIdent(""); err == nil {
		t.Error("quoteIdent should reject empty name")
	}
	got, err := quoteIdent("widgets")
	if err != nil || got != `"widgets"` {
		t.Errorf("quoteIdent(widgets) = %q, %v", got, err)
	}
}
