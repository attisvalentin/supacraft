package tenant

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEmitWebhooks(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := &DB{DB: sqlDB}
	no := false
	cfg := &TenantConfig{
		Schema: Schema{Tables: []SchemaTable{{Name: "users", Columns: []SchemaColumn{
			{Name: "id"}, {Name: "email"}, {Name: "password", Readable: &no}}}}},
		APISettings: APISettings{Webhooks: map[string]Webhook{
			"crm":   {URL: "https://crm.example.com", Events: []string{"users.insert"}},
			"audit": {URL: "https://audit.example.com", Events: []string{"users.*"}},
			"other": {URL: "https://x.example.com", Events: []string{"orders.insert"}},
		}},
	}
	if !cfg.WantsWebhook("users", OpDelete) || cfg.WantsWebhook("orders", OpDelete) {
		t.Fatal("WantsWebhook")
	}

	rows := make([]map[string]any, 150)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprint(i), "email": "a@x.co", "password": "$2a$hash"}
	}
	if err := EmitWebhooks(ctx, db, cfg, "users", OpInsert, rows); err != nil {
		t.Fatal(err)
	}
	jobs, err := ListJobs(ctx, sqlDB, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	// crm and audit, 150 rows in deliveries of 100 → 4; other doesn't match.
	if len(jobs) != 4 {
		t.Fatalf("%d deliveries, want 4", len(jobs))
	}
	for _, j := range jobs {
		name, ok := WebhookName(j.Function)
		if !ok || (name != "crm" && name != "audit") || j.Args["event"] != "users.insert" {
			t.Fatalf("delivery: %+v", j)
		}
		for _, r := range j.Args["rows"].([]any) {
			if _, leaked := r.(map[string]any)["password"]; leaked {
				t.Fatal("a non-readable column was sent")
			}
		}
	}
	if err := EmitWebhooks(ctx, db, cfg, "users", OpUpdate, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSetUserRole_EmitsUpdate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := writeTestTenant(dir, "shop", `{"db_name": "shop", "auth_enabled": true, "enabled": true,
	  "schema": {"tables": [{"name": "users", "columns": [{"name": "id"}, {"name": "email"}, {"name": "role"}, {"name": "password", "readable": false}]}]},
	  "api_settings": {"auth": {"roles": ["user", "admin"]},
	    "webhooks": {"crm": {"url": "https://crm.example.com", "events": ["users.update"]}}}}`); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, password TEXT, role TEXT, is_active INTEGER, email_verified INTEGER, session_version INTEGER, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, password) VALUES ('u1', 'a@x.co', 'hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := SetUserRole(ctx, dir, "shop", "a@x.co", "admin"); err != nil {
		t.Fatal(err)
	}
	jobs, err := ListJobs(ctx, db, "", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("deliveries: %+v %v", jobs, err)
	}
	row := jobs[0].Args["rows"].([]any)[0].(map[string]any)
	if row["role"] != "admin" || row["password"] != nil {
		t.Fatalf("payload: %+v", row)
	}
}

func writeTestTenant(dataDir, id, cfg string) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "tenants"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "tenants", id+".json"), []byte(cfg), 0o644)
}

// A change whose delivery can't be queued is rolled back with it.
func TestWriteWithWebhooks_Atomic(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	db := &DB{DB: sqlDB}
	if _, err := sqlDB.Exec(`CREATE TABLE "orders" ("id" TEXT PRIMARY KEY, "x" TEXT)`); err != nil {
		t.Fatal(err)
	}
	cfg := &TenantConfig{
		Schema:      Schema{Tables: []SchemaTable{{Name: "orders", Columns: []SchemaColumn{{Name: "id"}, {Name: "x"}}}}},
		APISettings: APISettings{Webhooks: map[string]Webhook{"erp": {URL: "https://erp.example.com", Events: []string{"orders.insert"}}}},
	}
	write := func(payload any) func(Querier) ([]map[string]any, error) {
		return func(q Querier) ([]map[string]any, error) {
			if _, err := q.Exec(ctx, `INSERT INTO "orders" ("id") VALUES ('o1')`); err != nil {
				return nil, err
			}
			return []map[string]any{{"id": "o1", "x": payload}}, nil
		}
	}
	// A value JSON can't encode makes queueing fail.
	if _, err := WriteWithWebhooks(ctx, db, cfg, "orders", OpInsert, write(make(chan int))); err == nil {
		t.Fatal("expected the enqueue to fail")
	}
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM "orders"`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the insert survived a failed enqueue: %d %v", n, err)
	}
	if _, err := WriteWithWebhooks(ctx, db, cfg, "orders", OpInsert, write("ok")); err != nil {
		t.Fatal(err)
	}
	jobs, _ := ListJobs(ctx, sqlDB, "", 10)
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM "orders"`).Scan(&n); err != nil || n != 1 || len(jobs) != 1 {
		t.Fatalf("committed write: %d rows, %d deliveries", n, len(jobs))
	}
}
