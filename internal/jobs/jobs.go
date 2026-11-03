// Package jobs runs the background jobs queued by job.enqueue steps: each
// job calls a PRIVATE function with its args, retrying with backoff until
// it succeeds or runs out of attempts.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const (
	// lease is how long a claimed job may run; after it, a job whose
	// server stopped mid-run is due again.
	lease = 6 * time.Minute
	// runTimeout bounds one run, like a cron job.
	runTimeout = 5 * time.Minute
	batch      = 10
	keepDone   = 7 * 24 * time.Hour
)

// backoff is the wait before attempt n+1: 30s, 1m, 2m, ... at most 1h.
func backoff(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Run executes one job: a webhook delivery, or its PRIVATE function with args.* set to the job's
// args and job.id / job.attempt. A fail step or any error fails the run.
func Run(ctx context.Context, cfg *tenant.TenantConfig, db *tenant.DB, j tenant.Job) error {
	if name, ok := tenant.WebhookName(j.Function); ok {
		return deliver(ctx, cfg, name, j)
	}
	fn, ok := cfg.Functions[j.Function]
	if !ok {
		return fmt.Errorf("function %q no longer exists", j.Function)
	}
	if !strings.EqualFold(fn.Route.Method, "PRIVATE") {
		return fmt.Errorf("function %q is not PRIVATE", j.Function)
	}
	loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
	pCtx := pipeline.NewPipelineContext()
	pipeline.InitializeBuiltins(pCtx, loc)
	if len(cfg.APISettings.Env) > 0 {
		pCtx.Set("env", map[string]string(cfg.APISettings.Env))
	}
	pCtx.Set("args", j.Args)
	pCtx.Set("job", map[string]any{"id": j.ID, "attempt": j.Attempts})
	deps := &pipeline.StepDeps{
		DBPool:            db,
		Schema:            &cfg.Schema,
		Config:            cfg,
		BypassSchemaFlags: fn.BypassSchemaFlags,
		MaxDepth:          pipeline.MaxCallDepth,
		Functions:         cfg.Functions,
		FunctionID:        j.Function,
		FunctionName:      fn.Name,
		Timezone:          loc,
	}
	err := pipeline.Execute(ctx, fn.Steps, pCtx, deps)
	if errors.Is(err, pipeline.ErrResponseSent) {
		err = nil // a response step just ends the run
	}
	return err
}

// RunDue claims the due jobs of one database and runs them in turn,
// recording each outcome. It returns how many ran.
func RunDue(ctx context.Context, cfg *tenant.TenantConfig, db *tenant.DB, tenantID string) (int, error) {
	abandoned, err := tenant.FailAbandonedJobs(ctx, db.DB)
	if err != nil {
		return 0, err
	}
	for _, j := range abandoned {
		logFailed(ctx, tenantID, j, errors.New(j.LastError))
	}
	claimed, err := tenant.ClaimJobs(ctx, db.DB, batch, lease)
	if err != nil {
		return 0, err
	}
	for _, j := range claimed {
		runCtx, cancel := context.WithTimeout(ctx, runTimeout)
		runErr := Run(runCtx, cfg, db, j)
		cancel()
		status, err := tenant.FinishJob(ctx, db.DB, j, runErr, backoff(j.Attempts))
		if err != nil {
			return 0, err
		}
		switch status {
		case tenant.JobFailed:
			logFailed(ctx, tenantID, j, runErr)
		case tenant.JobPending:
			// The error can quote resolved secrets; it is kept with the job
			// (masked when read), not in the server log.
			slog.Info("job will retry", "tenant", tenantID, "job", j.ID, "function", j.Function, "attempt", j.Attempts)
		}
	}
	return len(claimed), nil
}

// logFailed reports a job that ran out of attempts to the error log (the
// test's sandbox in supacraft test).
func logFailed(ctx context.Context, tenantID string, j tenant.Job, runErr error) {
	msg := fmt.Sprintf("job %s (%s) failed after %d attempt(s): %v", j.ID, j.Function, j.Attempts, runErr)
	if sb := pipeline.SandboxFrom(ctx); sb != nil && sb.Errors != nil {
		sb.Errors(msg)
	} else if errorlog.Global != nil {
		errorlog.Global.Log(tenantID, "JOB", j.Function, http.StatusInternalServerError, msg)
	}
	slog.Warn("job failed; see supacraft jobs list", "tenant", tenantID, "job", j.ID, "function", j.Function)
}

// Worker runs the jobs of every tenant a server serves. It scans all
// tenants every minute and otherwise only watches databases known to have
// jobs, so idle tenants cost nothing.
type Worker struct {
	store   tenant.Store
	pools   *tenant.PoolManager
	serving func(id string) bool

	mu      sync.Mutex
	next    map[string]time.Time // tenant id → when its next job is due
	running map[string]bool
	wake    chan struct{}
}

// NewWorker creates a worker; serving reports whether a tenant may run
// (exists and is not disabled).
func NewWorker(store tenant.Store, pools *tenant.PoolManager, serving func(id string) bool) *Worker {
	return &Worker{store: store, pools: pools, serving: serving,
		next: map[string]time.Time{}, running: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// Notify marks a tenant as having a job due now.
func (w *Worker) Notify(id string) {
	w.mu.Lock()
	w.next[id] = time.Now()
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start runs the worker until ctx ends.
func (w *Worker) Start(ctx context.Context) {
	go func() {
		w.scan(ctx)
		tick := time.NewTicker(time.Second)
		rescan := time.NewTicker(time.Minute)
		defer tick.Stop()
		defer rescan.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rescan.C:
				w.scan(ctx)
			case <-tick.C:
			case <-w.wake:
			}
			w.runDue(ctx)
		}
	}()
}

// scan looks at every tenant: it prunes old finished jobs and notes when
// each next job is due.
func (w *Worker) scan(ctx context.Context) {
	ids, err := w.store.ScanTenantIDs(ctx)
	if err != nil {
		slog.Error("jobs: scan tenants", "err", err)
		return
	}
	for _, id := range ids {
		if !w.serving(id) {
			continue
		}
		cfg, db, err := w.open(ctx, id)
		if err != nil || cfg == nil {
			continue
		}
		if err := tenant.PruneJobs(ctx, db.DB, keepDone); err != nil {
			slog.Warn("jobs: prune", "tenant", id, "err", err)
		}
		w.schedule(ctx, id, db)
	}
}

func (w *Worker) open(ctx context.Context, id string) (*tenant.TenantConfig, *tenant.DB, error) {
	cfg, err := w.store.GetTenantConfig(ctx, id)
	if err != nil || !cfg.Enabled || cfg.DBName == "" {
		return nil, nil, err
	}
	db, err := w.pools.GetPool(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return cfg, db, nil
}

// schedule records when the tenant's next job is due, or forgets it.
func (w *Worker) schedule(ctx context.Context, id string, db *tenant.DB) {
	at, ok, err := tenant.NextJobAt(ctx, db.DB)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil || !ok {
		delete(w.next, id)
		return
	}
	w.next[id] = at
}

// runDue starts a run for every tenant whose next job is due and that has
// no run in progress.
func (w *Worker) runDue(ctx context.Context) {
	now := time.Now()
	w.mu.Lock()
	var due []string
	for id, at := range w.next {
		if !at.After(now) && !w.running[id] {
			w.running[id] = true
			due = append(due, id)
		}
	}
	w.mu.Unlock()
	for _, id := range due {
		go func(id string) {
			defer func() {
				w.mu.Lock()
				delete(w.running, id)
				w.mu.Unlock()
			}()
			if !w.serving(id) {
				w.mu.Lock()
				delete(w.next, id)
				w.mu.Unlock()
				return
			}
			cfg, db, err := w.open(ctx, id)
			if err != nil || cfg == nil {
				return
			}
			if _, err := RunDue(ctx, cfg, db, id); err != nil {
				slog.Error("jobs: run", "tenant", id, "err", err)
			}
			w.schedule(ctx, id, db)
		}(id)
	}
}
