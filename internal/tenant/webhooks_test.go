package tenant

import (
	"context"
	"database/sql"
	"fmt"
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
