package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

const (
	MaxForeachIterations = 1000
	MaxCallDepth         = 10
	DefaultTimeout       = 30 * time.Second
)

// ErrResponseSent is a sentinel error indicating the response step has written the HTTP response.
var ErrResponseSent = errors.New("response sent")

// StepDeps bundles all dependencies needed by step executors.
type StepDeps struct {
	DBPool            tenant.Querier
	Schema            *tenant.Schema
	Config            *tenant.TenantConfig
	Mailer            Mailer
	Claims            *tenant.AuthClaims
	BypassSchemaFlags bool
	ResponseWriter    http.ResponseWriter
	MaxDepth          int
	CurrentDepth      int
	Functions         map[string]tenant.Function
	FunctionID        string
	FunctionName      string
	Timezone          *time.Location
	// InTransaction is set inside a transaction step; DBPool is then a *tenant.Tx.
	InTransaction bool

	// CapturedResponse holds the response data when executing in nested mode
	// (function.call). When ResponseWriter is nil, the response step stores
	// its data here instead of writing HTTP.
	CapturedResponse map[string]any
}

// Execute runs a pipeline function's steps sequentially.
func Execute(ctx context.Context, steps []tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	for i, step := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("pipeline timed out at step %d", i)
		}

		err := executeStep(ctx, &step, pCtx, deps)
		if errors.Is(err, ErrResponseSent) {
			return ErrResponseSent
		}
		if err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i, step.Type, err)
		}
	}
	return nil
}

func executeStep(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	if deps.InTransaction && !StepAllowedInTransaction(step.Type) {
		return fmt.Errorf("%s is not allowed inside a transaction; move it after the transaction step", step.Type)
	}
	switch step.Type {
	case "db.select":
		return ExecuteDBSelect(ctx, step, pCtx, deps)
	case "db.aggregate":
		return ExecuteDBAggregate(ctx, step, pCtx, deps)
	case "db.insert":
		return ExecuteDBInsert(ctx, step, pCtx, deps)
	case "db.update":
		return ExecuteDBUpdate(ctx, step, pCtx, deps)
	case "db.delete":
		return ExecuteDBDelete(ctx, step, pCtx, deps)
	case "transaction":
		return ExecuteTransaction(ctx, step, pCtx, deps)
	case "try":
		return ExecuteTry(ctx, step, pCtx, deps)
	case "fail":
		return ExecuteFail(ctx, step, pCtx, deps)
	case "if":
		return ExecuteIf(ctx, step, pCtx, deps)
	case "foreach":
		return ExecuteForeach(ctx, step, pCtx, deps)
	case "set_variable":
		return ExecuteSetVariable(ctx, step, pCtx, deps)
	case "transform.sum":
		return ExecuteTransformSum(ctx, step, pCtx, deps)
	case "transform.count":
		return ExecuteTransformCount(ctx, step, pCtx, deps)
	case "transform.map":
		return ExecuteTransformMap(ctx, step, pCtx, deps)
	case "transform.filter":
		return ExecuteTransformFilter(ctx, step, pCtx, deps)
	case "transform.math":
		return ExecuteTransformMath(ctx, step, pCtx, deps)
	case "transform.datetime":
		return ExecuteTransformDatetime(ctx, step, pCtx, deps)
	case "response":
		return ExecuteResponse(ctx, step, pCtx, deps)
	case "function.call":
		return ExecuteFunctionCall(ctx, step, pCtx, deps)
	case "http.request":
		return ExecuteHTTPRequest(ctx, step, pCtx, deps)
	case "email.send":
		return ExecuteEmailSend(ctx, step, pCtx, deps)
	default:
		return fmt.Errorf("unknown step type: %s", step.Type)
	}
}
