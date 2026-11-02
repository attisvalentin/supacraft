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
