package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// JobsDDL creates the background job queue of a tenant database. A job
// runs a PRIVATE function with args; see internal/jobs for the worker.
var JobsDDL = []string{
	`CREATE TABLE IF NOT EXISTS "_supacraft_jobs" (
		"id"           TEXT PRIMARY KEY,
		"function"     TEXT NOT NULL,
		"args"         TEXT NOT NULL,
		"status"       TEXT NOT NULL,
		"run_at"       INTEGER NOT NULL,
		"attempts"     INTEGER NOT NULL DEFAULT 0,
		"max_attempts" INTEGER NOT NULL,
		"locked_until" INTEGER NOT NULL DEFAULT 0,
		"last_error"   TEXT NOT NULL DEFAULT '',
		"created_at"   INTEGER NOT NULL,
		"finished_at"  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS "_supacraft_jobs_due" ON "_supacraft_jobs" ("status", "run_at")`,
}

// Job statuses.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// DefaultJobAttempts is how often a job runs before it counts as failed.
const DefaultJobAttempts = 5

// Job is one queued run of a function.
type Job struct {
	ID          string         `json:"id"`
	Function    string         `json:"function"`
	Args        map[string]any `json:"args"`
	Status      string         `json:"status"`
	RunAt       time.Time      `json:"run_at"`
	Attempts    int            `json:"attempts"`
	MaxAttempts int            `json:"max_attempts"`
	LastError   string         `json:"last_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
}

// JobsEnqueued, when set, is told that a database got a new job, so the
// worker looks at it now instead of on its next scan.
var JobsEnqueued func(dbName string)

// EnqueueJob queues function to run with args after delay. q may be a
// transaction: the job then exists only if it commits.
func EnqueueJob(ctx context.Context, q Querier, dbName, function string, args map[string]any, delay time.Duration, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = DefaultJobAttempts
	}
	if args == nil {
		args = map[string]any{}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("args: %w", err)
	}
	for _, stmt := range JobsDDL {
		if _, err := q.Exec(ctx, stmt); err != nil {
			return "", err
		}
	}
	id := uuid.NewString()
	now := time.Now()
	_, err = q.Exec(ctx,
		`INSERT INTO "_supacraft_jobs" ("id", "function", "args", "status", "run_at", "max_attempts", "created_at") VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, function, string(data), JobPending, now.Add(delay).Unix(), maxAttempts, now.Unix())
	if err != nil {
		return "", err
	}
	if JobsEnqueued != nil {
		JobsEnqueued(dbName)
	}
	return id, nil
}

const jobColumns = `"id", "function", "args", "status", "run_at", "attempts", "max_attempts", "last_error", "created_at", "finished_at"`

// ScanJob reads a row selected with jobColumns.
func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var args string
	var runAt, created, finished int64
	if err := row.Scan(&j.ID, &j.Function, &args, &j.Status, &runAt, &j.Attempts, &j.MaxAttempts, &j.LastError, &created, &finished); err != nil {
		return j, err
	}
	if err := json.Unmarshal([]byte(args), &j.Args); err != nil {
		return j, err
	}
	j.RunAt, j.CreatedAt = time.Unix(runAt, 0).UTC(), time.Unix(created, 0).UTC()
	if finished > 0 {
		t := time.Unix(finished, 0).UTC()
		j.FinishedAt = &t
	}
	return j, nil
}

func noJobsTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// ListJobs returns the jobs of db, newest first, optionally of one status.
func ListJobs(ctx context.Context, db *sql.DB, status string, limit int) ([]Job, error) {
	q := `SELECT ` + jobColumns + ` FROM "_supacraft_jobs"`
	args := []any{}
	if status != "" {
		q += ` WHERE "status" = ?`
		args = append(args, status)
	}
	q += ` ORDER BY "created_at" DESC, rowid DESC LIMIT ?`
	rows, err := db.QueryContext(ctx, q, append(args, limit)...)
	if noJobsTable(err) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ErrJobNotFound: no failed job has that id.
var ErrJobNotFound = errors.New("no failed job with that id")

// RetryJob queues a failed job again with a fresh set of attempts.
func RetryJob(ctx context.Context, db *sql.DB, dbName, id string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE "_supacraft_jobs" SET "status" = ?, "attempts" = 0, "run_at" = ?, "locked_until" = 0, "finished_at" = 0 WHERE "id" = ? AND "status" = ?`,
		JobPending, time.Now().Unix(), id, JobFailed)
	if noJobsTable(err) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrJobNotFound
	}
	if JobsEnqueued != nil {
		JobsEnqueued(dbName)
	}
	return nil
}

// ClaimJobs marks up to limit due jobs as running for lease and returns
// them. A running job whose lease ran out (the server stopped mid-run) is
// due again.
func ClaimJobs(ctx context.Context, db *sql.DB, limit int, lease time.Duration) ([]Job, error) {
	now := time.Now()
	rows, err := db.QueryContext(ctx, `UPDATE "_supacraft_jobs"
		SET "status" = ?, "attempts" = "attempts" + 1, "locked_until" = ?
		WHERE "id" IN (SELECT "id" FROM "_supacraft_jobs"
			WHERE ("status" = ? AND "run_at" <= ?) OR ("status" = ? AND "locked_until" < ?)
			ORDER BY "run_at" LIMIT ?)
		RETURNING `+jobColumns,
		JobRunning, now.Add(lease).Unix(), JobPending, now.Unix(), JobRunning, now.Unix(), limit)
	if noJobsTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FinishJob records the outcome of a claimed job: done, pending again
// after retryIn, or failed when it has no attempts left.
func FinishJob(ctx context.Context, db *sql.DB, j Job, runErr error, retryIn time.Duration) (status string, err error) {
	now := time.Now()
	switch {
	case runErr == nil:
		status = JobDone
		_, err = db.ExecContext(ctx, `UPDATE "_supacraft_jobs" SET "status" = ?, "finished_at" = ?, "last_error" = '' WHERE "id" = ?`, status, now.Unix(), j.ID)
	case j.Attempts >= j.MaxAttempts:
		status = JobFailed
		_, err = db.ExecContext(ctx, `UPDATE "_supacraft_jobs" SET "status" = ?, "finished_at" = ?, "last_error" = ? WHERE "id" = ?`, status, now.Unix(), runErr.Error(), j.ID)
	default:
		status = JobPending
		_, err = db.ExecContext(ctx, `UPDATE "_supacraft_jobs" SET "status" = ?, "run_at" = ?, "locked_until" = 0, "last_error" = ? WHERE "id" = ?`, status, now.Add(retryIn).Unix(), runErr.Error(), j.ID)
	}
	return status, err
}

// NextJobAt is when the next pending or running job of db is due; ok is
// false when there is none.
func NextJobAt(ctx context.Context, db *sql.DB) (at time.Time, ok bool, err error) {
	var next sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN "status" = ? THEN "run_at" ELSE "locked_until" END) FROM "_supacraft_jobs" WHERE "status" IN (?, ?)`,
		JobPending, JobPending, JobRunning).Scan(&next)
	if noJobsTable(err) {
		return at, false, nil
	}
	if err != nil || !next.Valid {
		return at, false, err
	}
	return time.Unix(next.Int64, 0), true, nil
}

// PruneJobs deletes finished jobs older than keep.
func PruneJobs(ctx context.Context, db *sql.DB, keep time.Duration) error {
	_, err := db.ExecContext(ctx, `DELETE FROM "_supacraft_jobs" WHERE "status" IN (?, ?) AND "finished_at" > 0 AND "finished_at" < ?`,
		JobDone, JobFailed, time.Now().Add(-keep).Unix())
	if noJobsTable(err) {
		return nil
	}
	return err
}

// ListTenantJobs is ListJobs for a tenant in dataDir (operator commands).
func ListTenantJobs(ctx context.Context, dataDir, id, status string, limit int) ([]Job, error) {
	db, _, err := OpenTenantDB(ctx, dataDir, id)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return ListJobs(ctx, db, status, limit)
}

// RetryTenantJob is RetryJob for a tenant in dataDir. A server running
// the tenant picks the job up within a minute.
func RetryTenantJob(ctx context.Context, dataDir, id, jobID string) error {
	db, cfg, err := OpenTenantDB(ctx, dataDir, id)
	if err != nil {
		return err
	}
	defer db.Close()
	return RetryJob(ctx, db, cfg.DBName, jobID)
}

// JobFunctionError: the function can't run as a job.
type JobFunctionError struct{ Function, Reason string }

func (e *JobFunctionError) Error() string {
	return fmt.Sprintf("function %q %s", e.Function, e.Reason)
}

// EnqueueTenantJob queues a PRIVATE function of a tenant in dataDir to run
// with args (operator commands: CLI, MCP, admin endpoint).
func EnqueueTenantJob(ctx context.Context, dataDir, id, function string, args map[string]any, delay time.Duration) (string, error) {
	db, cfg, err := OpenTenantDB(ctx, dataDir, id)
	if err != nil {
		return "", err
	}
	defer db.Close()
	fn, ok := cfg.Functions[function]
	switch {
	case !ok:
		return "", &JobFunctionError{function, "does not exist"}
	case fn.Route.Method != "PRIVATE":
		return "", &JobFunctionError{function, "is not PRIVATE; only PRIVATE functions run as jobs"}
	}
	return EnqueueJob(ctx, &DB{DB: db}, cfg.DBName, function, args, delay, 0)
}
