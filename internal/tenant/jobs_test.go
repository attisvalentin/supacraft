package tenant

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := &DB{DB: sqlDB}

	if jobs, err := ClaimJobs(ctx, sqlDB, 10, time.Minute); err != nil || len(jobs) != 0 {
		t.Fatalf("claim without a queue: %v %v", jobs, err)
	}
	if _, err := EnqueueJob(ctx, db, "x", "later", nil, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	id, err := EnqueueJob(ctx, db, "x", "now", map[string]any{"n": 1}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}

	// Only the due job is claimed, once.
	jobs, err := ClaimJobs(ctx, sqlDB, 10, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].ID != id || jobs[0].Attempts != 1 || jobs[0].Args["n"] != 1.0 {
		t.Fatalf("claim: %+v %v", jobs, err)
	}
	if again, _ := ClaimJobs(ctx, sqlDB, 10, time.Minute); len(again) != 0 {
		t.Fatalf("claimed twice: %+v", again)
	}

	// First failure retries; the second uses up the attempts.
	if status, err := FinishJob(ctx, sqlDB, jobs[0], errors.New("boom"), 0); err != nil || status != JobPending {
		t.Fatalf("first failure: %s %v", status, err)
	}
	jobs, _ = ClaimJobs(ctx, sqlDB, 10, time.Minute)
	if len(jobs) != 1 || jobs[0].Attempts != 2 {
		t.Fatalf("retry claim: %+v", jobs)
	}
	if status, _ := FinishJob(ctx, sqlDB, jobs[0], errors.New("boom again"), 0); status != JobFailed {
		t.Fatalf("second failure: %s", status)
	}
	failed, _ := ListJobs(ctx, sqlDB, JobFailed, 10)
	if len(failed) != 1 || failed[0].LastError != "boom again" || failed[0].FinishedAt == nil {
		t.Fatalf("failed job: %+v", failed)
	}

	// Retry gives it fresh attempts; only failed jobs can be retried.
	if err := RetryJob(ctx, sqlDB, "x", id); err != nil {
		t.Fatal(err)
	}
	if err := RetryJob(ctx, sqlDB, "x", id); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("retry of a pending job: %v", err)
	}
	if jobs, _ = ClaimJobs(ctx, sqlDB, 10, time.Minute); len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("after retry: %+v", jobs)
	}

	// A lease that ran out (the server stopped mid-run) makes it due again.
	if _, err := sqlDB.Exec(`UPDATE "_supacraft_jobs" SET "locked_until" = 0 WHERE "id" = ?`, id); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = ClaimJobs(ctx, sqlDB, 10, time.Minute); len(jobs) != 1 || jobs[0].Attempts != 2 {
		t.Fatalf("expired lease: %+v", jobs)
	}

	// Its last lease runs out too (say the job crashes the server): it is
	// not claimed again but failed.
	if _, err := sqlDB.Exec(`UPDATE "_supacraft_jobs" SET "locked_until" = 0 WHERE "id" = ?`, id); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = ClaimJobs(ctx, sqlDB, 10, time.Minute); len(jobs) != 0 {
		t.Fatalf("an exhausted job was claimed again: %+v", jobs)
	}
	abandoned, err := FailAbandonedJobs(ctx, sqlDB)
	if err != nil || len(abandoned) != 1 || abandoned[0].ID != id || abandoned[0].Status != JobFailed {
		t.Fatalf("abandoned: %+v %v", abandoned, err)
	}

	// Delays are checked in seconds, before they could overflow.
	if _, err := EnqueueTenantJob(ctx, t.TempDir(), "x", "f", nil, 9223372037); !errors.Is(err, ErrJobDelay) {
		t.Fatalf("huge delay: %v", err)
	}
}
