package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteResponse sends the HTTP response or captures it for nested function calls.
func ExecuteResponse(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	// Resolve all {{...}} in data
	resolved, err := ResolveMap(step.Data, pCtx)
	if err != nil {
		return fmt.Errorf("response: resolve data: %w", err)
	}

	// Convert time.Time values to tenant timezone before output
	loc := deps.Timezone
	if loc == nil {
		loc = time.UTC
	}
	converted := make(map[string]any, len(resolved))
	for k, v := range resolved {
		converted[k] = convertTimesInValue(v, loc)
	}

	statusCode := step.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	// Nested call mode: capture response instead of writing HTTP
	if deps.ResponseWriter == nil {
		deps.CapturedResponse = converted
		return ErrResponseSent
	}

	// Top-level: write HTTP response
	deps.ResponseWriter.Header().Set("Content-Type", "application/json")
	deps.ResponseWriter.WriteHeader(statusCode)
	if err := json.NewEncoder(deps.ResponseWriter).Encode(converted); err != nil {
		return fmt.Errorf("response: encode json: %w", err)
	}

	return ErrResponseSent
}

// convertTimesInValue recursively walks v and converts any time.Time values to loc.
func convertTimesInValue(v any, loc *time.Location) any {
	switch val := v.(type) {
	case time.Time:
		return val.In(loc)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, elem := range val {
			out[k] = convertTimesInValue(elem, loc)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = convertTimesInValue(elem, loc)
		}
		return out
	default:
		return v
	}
}
