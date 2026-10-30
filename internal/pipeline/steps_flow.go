package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// FailError is returned by a fail step. It stops the pipeline (rolling back
// an enclosing transaction) and becomes the HTTP response unless a try step
// catches it.
type FailError struct {
	Status  int
	Message string
	Data    map[string]any
}

func (e *FailError) Error() string { return e.Message }

// ExecuteFail stops the pipeline with a client-facing error.
func ExecuteFail(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	msg, err := ResolveString(step.Message, pCtx)
	if err != nil {
		return fmt.Errorf("fail: resolve message: %w", err)
	}
	data, err := ResolveMap(step.Data, pCtx)
	if err != nil {
		return fmt.Errorf("fail: resolve data: %w", err)
	}
	status := step.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusBadRequest
	}
	text := fmt.Sprint(msg)
	if text == "" {
		text = http.StatusText(status)
	}
	return &FailError{Status: status, Message: text, Data: data}
}

// ExecuteTransaction runs its steps atomically: any error rolls back every
// database write made inside. A transaction nested in another one (directly
// or through function.call) joins the outer one.
func ExecuteTransaction(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	if deps.InTransaction {
		return Execute(ctx, step.SubSteps, pCtx, deps)
	}
	db, ok := deps.DBPool.(*tenant.DB)
	if !ok {
		return fmt.Errorf("transaction: no database connection")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("transaction: begin: %w", err)
	}

	txDeps := *deps
	txDeps.DBPool = tx
	txDeps.InTransaction = true
	if err := Execute(ctx, step.SubSteps, pCtx, &txDeps); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
}

// ExecuteTry runs its steps; if one fails, it runs catch with the error in
// the variable named by "as" (default "error"). A response step inside try is
// not an error and is never caught.
func ExecuteTry(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	err := Execute(ctx, step.SubSteps, pCtx, deps)
	if err == nil || errors.Is(err, ErrResponseSent) || ctx.Err() != nil {
		return err
	}
	name := step.As
	if name == "" {
		name = "error"
	}
	pCtx.Set(name, caughtError(err))
	if len(step.Catch) == 0 {
		return nil
	}
	return Execute(ctx, step.Catch, pCtx, deps)
}

// caughtError describes err to catch steps. Only fail errors carry their own
// message; other failures can include SQL or upstream details, so their
// message is generic and the full text is in "detail" (for logs, not clients).
func caughtError(err error) map[string]any {
	var fail *FailError
	if errors.As(err, &fail) {
		return map[string]any{"message": fail.Message, "status": fail.Status, "data": fail.Data, "detail": fail.Message}
	}
	return map[string]any{"message": "internal error", "status": http.StatusInternalServerError, "detail": err.Error()}
}

// stepsForbiddenInTx can't be undone by a rollback, or (response) would send
// a reply before the commit is known to succeed.
var stepsForbiddenInTx = map[string]bool{"http.request": true, "email.send": true, "response": true}

// StepAllowedInTransaction reports whether a step type may run inside a
// transaction.
func StepAllowedInTransaction(stepType string) bool { return !stepsForbiddenInTx[stepType] }
