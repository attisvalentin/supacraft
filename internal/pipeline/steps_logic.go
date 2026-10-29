package pipeline

import (
	"context"
	"fmt"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteIf evaluates a condition and executes the then or else branch.
func ExecuteIf(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	result, err := EvaluateCondition(step.Condition, pCtx)
	if err != nil {
		return fmt.Errorf("if: evaluate condition: %w", err)
	}

	if result {
		if len(step.Then) > 0 {
			return Execute(ctx, step.Then, pCtx, deps)
		}
	} else {
		if len(step.Else) > 0 {
			return Execute(ctx, step.Else, pCtx, deps)
		}
	}
	return nil
}

// ExecuteForeach iterates over an array in the context and executes sub-steps per item.
func ExecuteForeach(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	// Resolve items path
	val, err := ResolveString(step.Items, pCtx)
	if err != nil {
		return fmt.Errorf("foreach: resolve items: %w", err)
	}

	// Get array to iterate
	var items []any
	switch v := val.(type) {
	case []any:
		items = v
	case []map[string]any:
		items = make([]any, len(v))
		for i, m := range v {
			items[i] = m
		}
	default:
		return fmt.Errorf("foreach: items must be an array, got %T", val)
	}

	if len(items) > MaxForeachIterations {
		return fmt.Errorf("foreach: iteration limit exceeded (%d > %d)", len(items), MaxForeachIterations)
	}

	loopVar := step.As
	if loopVar == "" {
		loopVar = "item"
	}

	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("foreach: timed out at iteration %d", i)
		}

		pCtx.Set(loopVar, item)
		pCtx.Set(loopVar+"_index", i)

		if err := Execute(ctx, step.SubSteps, pCtx, deps); err != nil {
			return err
		}
	}

	return nil
}

// ExecuteSetVariable sets a variable in the pipeline context.
func ExecuteSetVariable(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	if step.Assign == "" {
		return fmt.Errorf("set_variable: assign is required")
	}

	resolved, err := ResolveValue(step.Value, pCtx)
	if err != nil {
		return fmt.Errorf("set_variable: resolve value: %w", err)
	}

	pCtx.Set(step.Assign, resolved)
	return nil
}
