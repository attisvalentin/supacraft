package testrun

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
)

// shopProject copies testdata/shop.json (a checkout function that charges
// through http.request, records the order and mails the buyer, plus seed
// data and tests) into a fresh project directory.
func shopProject(t *testing.T) *project.Project {
	t.Helper()
	data, err := os.ReadFile("testdata/shop.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PAY_KEY=test\nSMTP_PASSWORD=x\nADMIN_PASSWORD=Admin123!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	p := shopProject(t)
	results, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Result{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if len(byName) != 6 {
		t.Fatalf("%d results: %+v", len(results), results)
	}
	for name, r := range byName {
		if name == "deliberately wrong" {
			continue
		}
		if !r.Passed {
			t.Errorf("%s failed: %v %v %s", name, r.Failures, r.ServerErrors, r.Body)
		}
	}
	// The unmocked network call fails the test and says why.
	wrong := byName["deliberately wrong"]
	if wrong.Passed || !strings.Contains(strings.Join(wrong.Failures, "\n"), "has no http_mocks entry") || len(wrong.ServerErrors) == 0 {
		t.Errorf("deliberately wrong: %+v", wrong)
	}
	// Tests never touch the project's own database.
	if _, err := os.Stat(filepath.Join(p.DataDir(), p.TenantID()+".db")); !os.IsNotExist(err) {
		t.Errorf("the local database was created: %v", err)
	}

	// Picking tests by name.
	results, err = Run(context.Background(), p, "checkout needs sign-in")
	if err != nil || len(results) != 1 || !results[0].Passed {
		t.Fatalf("one test: %+v %v", results, err)
	}
}

func TestSeedLocal(t *testing.T) {
	p := shopProject(t)
	ctx := context.Background()
	res, err := SeedLocal(ctx, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Users != 2 || res.Rows["products"] != 2 {
		t.Fatalf("first seed: %+v", res)
	}
	// Again: nothing is duplicated.
	if res, err = SeedLocal(ctx, p, false); err != nil || res.Users != 0 || res.Rows["products"] != 0 {
		t.Fatalf("second seed: %+v %v", res, err)
	}

	db, err := sql.Open("sqlite", filepath.Join(p.DataDir(), p.TenantID()+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func(table string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var role string
	if err := db.QueryRow(`SELECT "role" FROM "users" WHERE "email" = 'alice@example.com'`).Scan(&role); err != nil || role != "user" {
		t.Fatalf("alice's role: %q %v", role, err)
	}
	if _, err := db.Exec(`INSERT INTO "orders" ("id") VALUES ('o1')`); err != nil {
		t.Fatal(err)
	}

	// Reset leaves exactly the seed.
	if _, err := SeedLocal(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	if count("orders") != 0 || count("products") != 2 || count("users") != 2 {
		t.Fatalf("after reset: %d orders, %d products, %d users", count("orders"), count("products"), count("users"))
	}
}

func TestMatch(t *testing.T) {
	got := map[string]any{"ok": true, "items": []any{map[string]any{"id": 1.0, "x": "y"}}, "extra": 1.0}
	if d := match("body", map[string]any{"ok": true, "items": []any{map[string]any{"id": 1.0}}}, got); len(d) != 0 {
		t.Errorf("subset should match: %v", d)
	}
	if d := match("body", map[string]any{"ok": false, "missing": 1.0, "items": []any{}}, got); len(d) != 3 {
		t.Errorf("want 3 differences, got %v", d)
	}
}

func writeProject(t *testing.T, cfg, env string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Tests use local storage whatever the project configures, and results
// never show .env values.
func TestRun_Isolation(t *testing.T) {
	p := writeProject(t, `{
  "name": "files",
  "api_settings": {
    "env": {"HOOK": "${HOOK_URL}"},
    "storage": {"driver": "s3", "config": {"key": "${S3_KEY}", "secret": "${S3_SECRET}", "region": "eu-central-1", "bucket": "b"}}
  },
  "functions": {
    "hook": {"route": {"path": "/fn/hook", "method": "POST"},
             "steps": [{"type": "http.request", "url": "{{env.HOOK}}", "method": "POST", "assign": "r"}, {"type": "response", "data": {}}]}
  },
  "tests": {
    "download through local storage": {"call": "GET /files/nothing.png", "expect": {"status": 404}},
    "unmocked hook": {"call": "POST /fn/hook", "expect": {"status": 200}}
  }
}`, "HOOK_URL=https://hooks.example.com/s3cr3t-token\nS3_KEY=AKIAEXAMPLEKEY\nS3_SECRET=supersecretvalue\n")
	results, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Name == "download through local storage" && !r.Passed {
			t.Errorf("storage was not local: %+v", r)
		}
		if r.Name == "unmocked hook" {
			all := strings.Join(append(append([]string{r.Body}, r.Failures...), r.ServerErrors...), "\n")
			if r.Passed || strings.Contains(all, "s3cr3t-token") || !strings.Contains(all, "[secret HOOK_URL]") {
				t.Errorf("hook result leaks or misses the mask: %+v", r)
			}
		}
	}

	if _, err := Run(context.Background(), p, "no such test"); err == nil {
		t.Error("an unknown test name was accepted")
	}
}

func TestSeed_ReportsInvalidRows(t *testing.T) {
	p := writeProject(t, `{
  "name": "strict",
  "schema": {"tables": [{"name": "items", "columns": [
    {"name": "id", "type": "uuid", "primary": true}, {"name": "title", "type": "varchar", "nullable": false}]}]},
  "seed": {"tables": {"items": [{"id": "a"}]}}
}`, "")
	if _, err := SeedLocal(context.Background(), p, false); err == nil || !strings.Contains(err.Error(), "items row 0") {
		t.Fatalf("a row breaking NOT NULL was not reported: %v", err)
	}
}

func TestEmptySections(t *testing.T) {
	p := writeProject(t, `{"name": "empty", "seed": [], "tests": []}`, "")
	if results, err := Run(context.Background(), p); err != nil || len(results) != 0 {
		t.Fatalf("%v %v", results, err)
	}
}

func TestRun_Jobs(t *testing.T) {
	p := writeProject(t, `{
  "name": "jobs",
  "schema": {"tables": [{"name": "orders", "columns": [{"name": "id", "type": "uuid", "primary": true}, {"name": "invoiced", "type": "boolean"}]}]},
  "api_settings": {"email": {"smtp": {"enabled": true, "provider": "smtp", "host": "smtp.example.com", "port": 587, "username": "u", "password": "p", "encryption": "tls", "from_email": "a@example.com", "from_name": "A"}}},
  "functions": {
    "checkout": {"route": {"path": "/fn/checkout", "method": "POST"}, "steps": [
      {"type": "transaction", "steps": [
        {"type": "db.insert", "table": "orders", "data": {"invoiced": false}, "assign": "order"},
        {"type": "job.enqueue", "function_id": "invoice", "args": {"order_id": "{{order.id}}"}, "delay": 600}
      ]},
      {"type": "response", "data": {"ok": true}}]},
    "invoice": {"route": {"path": "", "method": "PRIVATE"}, "steps": [
      {"type": "db.update", "table": "orders", "where": {"id": "{{args.order_id}}"}, "data": {"invoiced": true}},
      {"type": "email.send", "mode": "raw", "to": ["billing@example.com"], "subject": "Invoice", "text": "{{args.order_id}}"}]},
    "flaky": {"route": {"path": "/fn/flaky", "method": "POST"}, "steps": [
      {"type": "job.enqueue", "function_id": "boom", "max_attempts": 2}, {"type": "response", "data": {}}]},
    "boom": {"route": {"path": "", "method": "PRIVATE"}, "steps": [{"type": "fail", "message": "always fails"}]}
  },
  "tests": {
    "queued, not run": {"call": "POST /fn/checkout", "expect": {"status": 200, "emails": 0,
      "jobs": [{"function": "invoice", "status": "pending"}], "db": {"orders": {"where": {"invoiced": false}, "count": 1}}}},
    "run": {"call": "POST /fn/checkout", "run_jobs": true, "expect": {"status": 200, "emails": 1,
      "jobs": [{"function": "invoice", "status": "done", "attempts": 1}], "db": {"orders": {"where": {"invoiced": true}, "count": 1}}}},
    "retried then failed": {"call": "POST /fn/flaky", "run_jobs": true, "expect": {"status": 200,
      "jobs": [{"function": "boom", "status": "failed", "attempts": 2}]}}
  }
}`, "")
	results, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s: %v %v %s", r.Name, r.Failures, r.ServerErrors, r.Body)
		}
		if r.Name == "retried then failed" && (len(r.ServerErrors) != 1 || !strings.Contains(r.ServerErrors[0], "always fails")) {
			t.Errorf("failed job not reported: %v", r.ServerErrors)
		}
	}
}

// A secret JSON would escape (here "<") is still masked in a body diff.
func TestRun_MasksEscapedSecrets(t *testing.T) {
	p := writeProject(t, `{
  "name": "esc",
  "api_settings": {"env": {"K": "${KEY}"}},
  "functions": {"f": {"route": {"path": "/fn/f", "method": "GET"}, "steps": [{"type": "response", "data": {"k": "{{env.K}}"}}]}},
  "tests": {"t": {"call": "GET /fn/f", "expect": {"body": {"k": "other"}}}}
}`, "KEY=ab<cd&ef\n")
	results, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(append(results[0].Failures, results[0].Body), "\n")
	if strings.Contains(all, `ab<cd`) || strings.Contains(all, "ab<cd") || !strings.Contains(all, "[secret KEY]") {
		t.Fatalf("secret leaked or not masked: %s", all)
	}
}

func TestRun_Webhooks(t *testing.T) {
	p := writeProject(t, `{
  "name": "hooks",
  "auth_enabled": true,
  "schema": {"tables": [{"name": "orders", "columns": [{"name": "id", "type": "uuid", "primary": true}, {"name": "total", "type": "int"}]}]},
  "api_settings": {
    "tables": {"orders": {"create": {"enabled": true}, "delete": {"enabled": true}}},
    "webhooks": {
      "erp": {"url": "${ERP_URL}", "events": ["orders.*"], "secret": "${ERP_SECRET}", "max_attempts": 1},
      "crm": {"url": "https://crm.example.com/hook", "events": ["users.insert"], "secret": "${ERP_SECRET}"}
    }
  },
  "functions": {
    "atomic": {"route": {"path": "/fn/atomic", "method": "POST"}, "steps": [
      {"type": "transaction", "steps": [
        {"type": "db.insert", "table": "orders", "data": {"total": 5}},
        {"type": "fail", "message": "rolled back"}]}]}
  },
  "seed": {"tables": {"orders": [{"id": "11111111-1111-1111-1111-111111111111", "total": 1}]}},
  "tests": {
    "create queues a delivery": {"call": "POST /create", "body": {"table_id": "orders", "data": {"total": 9}},
      "expect": {"status": 201, "webhooks": [{"webhook": "erp", "event": "orders.insert", "rows": [{"total": 9}], "status": "pending"}]}},
    "delivered through a mock": {"call": "POST /create", "body": {"table_id": "orders", "data": {"total": 9}}, "run_jobs": true,
      "http_mocks": {"https://erp.example.com/": {"status": 200}},
      "expect": {"webhooks": [{"webhook": "erp", "status": "done"}]}},
    "a failing receiver": {"call": "POST /create", "body": {"table_id": "orders", "data": {"total": 9}}, "run_jobs": true,
      "http_mocks": {"https://erp.example.com/": {"status": 500}},
      "expect": {"webhooks": [{"webhook": "erp", "status": "failed"}]}},
    "delete sends the deleted rows": {"call": "POST /delete", "body": {"table_id": "orders", "where": {"total": 1}},
      "expect": {"webhooks": [{"event": "orders.delete", "rows": [{"id": "11111111-1111-1111-1111-111111111111", "total": 1}]}]}},
    "a rolled back transaction sends nothing": {"call": "POST /fn/atomic", "expect": {"status": 400, "webhooks": []}},
    "register sends no password": {"call": "POST /auth/register", "body": {"email": "a@x.co", "password": "Secret123!"},
      "expect": {"status": 201, "webhooks": [{"webhook": "crm", "event": "users.insert", "rows": [{"email": "a@x.co"}]}]}}
  }
}`, "ERP_URL=https://erp.example.com/hook\nERP_SECRET=whsec_test\n")
	results, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s: %v %v %s", r.Name, r.Failures, r.ServerErrors, r.Body)
		}
	}
}
