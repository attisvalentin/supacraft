package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteJobEnqueue queues a PRIVATE function to run in the background with
// args. Inside a transaction step the job is queued only if it commits.
func ExecuteJobEnqueue(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	fn, ok := deps.Functions[step.FunctionID]
	if !ok {
		return fmt.Errorf("job.enqueue: function %q not found", step.FunctionID)
	}
	if !strings.EqualFold(fn.Route.Method, "PRIVATE") {
		return fmt.Errorf("job.enqueue: function %q must be PRIVATE", step.FunctionID)
	}
	args := map[string]any{}
	if step.Args != nil {
		resolved, err := ResolveMap(step.Args, pCtx)
		if err != nil {
			return fmt.Errorf("job.enqueue: resolve args: %w", err)
		}
		args = resolved
	}
	seconds, err := resolveCount(step.Delay, "delay", pCtx)
	if err != nil {
		return fmt.Errorf("job.enqueue: %w", err)
	}
	if seconds > tenant.MaxJobDelaySeconds {
		return fmt.Errorf("job.enqueue: delay is longer than 30 days")
	}
	delay := time.Duration(max(seconds, 0)) * time.Second
	id, err := tenant.EnqueueJob(ctx, deps.DBPool, deps.Config.DBName, step.FunctionID, args, delay, step.MaxAttempts)
	if err != nil {
		return fmt.Errorf("job.enqueue: %w", err)
	}
	if step.Assign != "" {
		pCtx.Set(step.Assign, map[string]any{"id": id})
	}
	return nil
}
