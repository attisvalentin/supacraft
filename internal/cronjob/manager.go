package cronjob

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/robfig/cron/v3"
)

type registeredJob struct {
	entryID  cron.EntryID
	schedule string
}

// effectiveSchedule prepends CRON_TZ=<tz> to the schedule string when the tenant
// has a non-UTC timezone, enabling per-entry timezone support in robfig/cron/v3.
func effectiveSchedule(schedule, tz string) string {
	if tz == "" || tz == "UTC" {
		return schedule
	}
	return "CRON_TZ=" + tz + " " + schedule
}

// CronManager discovers tenant cronjobs from the tenant store and schedules them.
type CronManager struct {
	store       tenant.Store
	poolManager *tenant.PoolManager
	cron        *cron.Cron
	registry    map[string]registeredJob
	mu          sync.Mutex
}

func NewCronManager(store tenant.Store, poolManager *tenant.PoolManager) *CronManager {
	return &CronManager{
		store:       store,
		poolManager: poolManager,
		cron:        cron.New(),
		registry:    make(map[string]registeredJob),
	}
}

// Start loads all tenant cronjobs, starts the scheduler, and runs a periodic reload every 60s.
func (cm *CronManager) Start(ctx context.Context) {
	cm.reload(ctx)
	cm.cron.Start()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cm.reload(ctx)
			}
		}
	}()
}

// Stop halts the scheduler and returns a context that is done when in-flight jobs finish.
func (cm *CronManager) Stop() context.Context {
	return cm.cron.Stop()
}

// reload scans all tenants and diffs against the current job registry.
func (cm *CronManager) reload(ctx context.Context) {
	tenantIDs, err := cm.store.ScanTenantIDs(ctx)
	if err != nil {
		slog.Error("cronjob: failed to scan tenant IDs", "err", err)
		return
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	seen := make(map[string]bool)

	for _, tenantID := range tenantIDs {
		cfg, err := cm.store.GetTenantConfig(ctx, tenantID)
		if err != nil {
			slog.Error("cronjob: failed to load tenant config", "tenantID", tenantID, "err", err)
			continue
		}

		if !cfg.Enabled {
			slog.Info("cronjob: skipping disabled tenant", "tenantID", tenantID)
			continue
		}

		if cfg.DBName == "" {
			slog.Warn("cronjob: skipping tenant jobs, db name not set", "tenantID", tenantID)
			continue
		}

		for jobID, job := range cfg.Cronjobs {
			if !job.Enabled {
				continue
			}

			key := tenantID + ":" + jobID
			seen[key] = true

			sched := effectiveSchedule(job.Schedule, cfg.APISettings.Timezone)

			if existing, ok := cm.registry[key]; ok {
				if existing.schedule == sched {
					continue // no change
				}
				// Schedule changed — remove old entry
				cm.cron.Remove(existing.entryID)
				delete(cm.registry, key)
			}

			// Capture loop variables for the closure
			tenantIDCopy := tenantID
			jobIDCopy := jobID
			jobCopy := job
			cfgCopy := cfg

			cronJob := cron.NewChain(
				cron.SkipIfStillRunning(cron.DiscardLogger),
			).Then(cron.FuncJob(func() {
				cm.runJob(tenantIDCopy, jobIDCopy, &jobCopy, cfgCopy)
			}))

			entryID, err := cm.cron.AddJob(sched, cronJob)
			if err != nil {
				slog.Error("cronjob: invalid schedule", "tenantID", tenantID, "jobID", jobID, "schedule", sched, "err", err)
				continue
			}

			cm.registry[key] = registeredJob{entryID: entryID, schedule: sched}
			slog.Info("cronjob: registered job", "tenantID", tenantID, "jobID", jobID, "schedule", sched)
		}
	}

	// Remove jobs that no longer exist or have been disabled
	for key, reg := range cm.registry {
		if !seen[key] {
			cm.cron.Remove(reg.entryID)
			delete(cm.registry, key)
			slog.Info("cronjob: removed job", "key", key)
		}
	}
}

// runJob executes a single cronjob using the pipeline engine.
func (cm *CronManager) runJob(tenantID, jobID string, job *tenant.Cronjob, cfg *tenant.TenantConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	slog.Info("cronjob: starting", "tenantID", tenantID, "jobID", jobID, "name", job.Name)

	pool, err := cm.poolManager.GetPool(ctx, cfg)
	if err != nil {
		slog.Error("cronjob: failed to get db pool", "tenantID", tenantID, "jobID", jobID, "err", err)
		return
	}

	loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
	pCtx := pipeline.NewPipelineContext()
	pipeline.InitializeBuiltins(pCtx, loc)
	if len(cfg.APISettings.Env) > 0 {
		pCtx.Set("env", map[string]string(cfg.APISettings.Env))
	}

	deps := &pipeline.StepDeps{
		DBPool:       pool,
		Schema:       &cfg.Schema,
		Config:       cfg,
		Claims:       nil, // no auth context in cron
		MaxDepth:     pipeline.MaxCallDepth,
		CurrentDepth: 0,
		Functions:    cfg.Functions,
		Timezone:     loc,
		// ResponseWriter: nil → response step captures instead of writing HTTP
	}

	err = pipeline.Execute(ctx, job.Steps, pCtx, deps)
	duration := time.Since(start)

	if err != nil && err != pipeline.ErrResponseSent {
		slog.Error("cronjob: failed", "tenantID", tenantID, "jobID", jobID, "name", job.Name, "duration", duration, "err", err)
		return
	}

	slog.Info("cronjob: completed", "tenantID", tenantID, "jobID", jobID, "name", job.Name, "duration", duration)
}

// Reload rescans tenants and reschedules changed jobs immediately.
func (cm *CronManager) Reload(ctx context.Context) {
	cm.reload(ctx)
}
