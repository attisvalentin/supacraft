package cronjob

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"

	_ "modernc.org/sqlite"
)

const cronProject = `{
  "name": "jobs",
  "schema": {"tables": [{"name": "runs", "columns": [
    {"name": "id", "type": "uuid", "primary": true}, {"name": "label", "type": "text"}]}]},
  "cronjobs": {"tick": {"schedule": "0 3 * * *", "enabled": true, "steps": [
    {"type": "db.insert", "table": "runs", "data": {"id": "LABEL", "label": "LABEL"}}]}}
}`

// A job whose steps change but whose schedule doesn't must run the new steps.
func TestRunCurrent_UsesLatestSteps(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	apply := func(label string) {
		t.Helper()
		cfg := strings.ReplaceAll(cronProject, "LABEL", label)
		if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := project.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ApplyTo(ctx, data, false); err != nil {
			t.Fatal(err)
		}
	}

	apply("first")
	cfg := &config.Config{DataDir: data}
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cm := NewCronManager(store, tenant.NewPoolManager(cfg))
	cm.reload(ctx)

	apply("second")
	cm.reload(ctx)
	cm.runCurrent("jobs", "tick")

	db, err := sql.Open("sqlite", filepath.Join(data, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var label string
	if err := db.QueryRow(`SELECT label FROM runs`).Scan(&label); err != nil || label != "second" {
		t.Fatalf("ran %q (%v), want the updated steps", label, err)
	}
}
