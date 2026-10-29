package pipeline

import (
	"context"
	"fmt"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteFunctionCall calls another pipeline function (reusable functions).
func ExecuteFunctionCall(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	if deps.CurrentDepth >= deps.MaxDepth {
		return fmt.Errorf("function.call: max recursion depth reached (%d)", deps.MaxDepth)
	}

	fn, ok := deps.Functions[step.FunctionID]
	if !ok {
		return fmt.Errorf("function.call: function %q not found", step.FunctionID)
	}

	// Create isolated child context — only args and built-ins are available, no parent variables
	childCtx := NewPipelineContext()
	InitializeBuiltins(childCtx, deps.Timezone)

	// Resolve args from parent context and inject under the "args" namespace
	if step.Args != nil {
		resolvedArgs, err := ResolveMap(step.Args, pCtx)
		if err != nil {
			return fmt.Errorf("function.call: resolve args: %w", err)
		}
		childCtx.Set("args", resolvedArgs)
	}

	// Create child deps (nested mode: no ResponseWriter, incremented depth)
	childDeps := &StepDeps{
		DBPool:            deps.DBPool,
		Schema:            deps.Schema,
		Config:            deps.Config,
		Mailer:            deps.Mailer,
		Claims:            deps.Claims,
		BypassSchemaFlags: fn.BypassSchemaFlags,
		ResponseWriter:    nil, // capture mode
		MaxDepth:          deps.MaxDepth,
		CurrentDepth:      deps.CurrentDepth + 1,
		Functions:         deps.Functions,
		FunctionID:        step.FunctionID,
		FunctionName:      fn.Name,
		Timezone:          deps.Timezone,
	}

	// Execute sub-function
	err := Execute(ctx, fn.Steps, childCtx, childDeps)
	if err == ErrResponseSent {
		// Sub-function produced a response — capture it
		if step.Assign != "" && childDeps.CapturedResponse != nil {
			pCtx.Set(step.Assign, childDeps.CapturedResponse)
		}
		return nil // Don't propagate ErrResponseSent from nested calls
	}
	if err != nil {
		return fmt.Errorf("function.call %q: %w", step.FunctionID, err)
	}

	return nil
}
