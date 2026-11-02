package jobs

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestBackoff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 20: time.Hour} {
		if got := backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// The worker picks up a job when notified, runs it, and records the result;
// a disabled tenant's jobs wait.
func TestWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(`{
  "name": "shop",
  "schema": {"tables": [{"name": "marks", "columns": [{"name": "id", "type": "uuid", "primary": true}, {"name": "note", "type": "varchar"}]}]},
  "functions": {"mark": {"route": {"path": "", "method": "PRIVATE"},
    "steps": [{"type": "db.insert", "table": "marks", "data": {"note": "{{args.note}}"}}]}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: p.DataDir()}
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pools := tenant.NewPoolManager(cfg)
	defer pools.Retain(nil)
	var disabled atomic.Bool
	w := NewWorker(store, pools, func(string) bool { return !disabled.Load() })
	w.Start(ctx)

	tcfg, err := store.GetTenantConfig(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	db, err := pools.GetPool(ctx, tcfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.EnqueueJob(ctx, db, "shop", "mark", map[string]any{"note": "hi"}, 0, 0); err != nil {
		t.Fatal(err)
	}
	w.Notify("shop")

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the job to run", func() bool {
		list, _ := tenant.ListJobs(ctx, db.DB, tenant.JobDone, 10)
		return len(list) == 1
	})
	var note string
	if err := db.QueryRow(ctx, `SELECT "note" FROM "marks"`).Scan(&note); err != nil || note != "hi" {
		t.Fatalf("job effect: %q %v", note, err)
	}

	// A disabled tenant's jobs are not run.
	disabled.Store(true)
	if _, err := tenant.EnqueueJob(ctx, db, "shop", "mark", map[string]any{"note": "later"}, 0, 0); err != nil {
		t.Fatal(err)
	}
	w.Notify("shop")
	time.Sleep(1500 * time.Millisecond)
	if list, _ := tenant.ListJobs(ctx, db.DB, tenant.JobPending, 10); len(list) != 1 {
		t.Fatalf("disabled tenant's job ran: %+v", list)
	}
}
