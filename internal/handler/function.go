package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// HandleFunction handles requests to pipeline function routes under /fn/*.
func HandleFunction(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		// Extract the function path from the URL
		fnPath := r.URL.Path

		// Match against tenant's function routes
		var matchedFunc *tenant.Function
		var matchedFuncID string
		for fnID, fn := range cfg.Functions {
			if fn.Route.Path == fnPath && strings.EqualFold(fn.Route.Method, r.Method) {
				f := fn
				matchedFunc = &f
				matchedFuncID = fnID
				break
			}
		}
		if matchedFunc == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		// Auth check
		var claims *tenant.AuthClaims
		if matchedFunc.Route.AuthRequired {
			var err error
			claims, err = validateJWT(r, cfg, pool)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		// Get DB pool
		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		// Build initial pipeline context
		loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
		pCtx := pipeline.NewPipelineContext()
		pipeline.InitializeBuiltins(pCtx, loc)

		// Auth context
		if claims != nil {
			pCtx.Set("auth", map[string]any{
				"user_id": claims.UserID,
				"email":   claims.Email,
			})
		}

		// Request body (for POST/PUT/PATCH)
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			var body map[string]any
			err := json.NewDecoder(r.Body).Decode(&body)
			var tooLarge *http.MaxBytesError
			switch {
			case err == nil:
				pCtx.Set("request", body)
			case errors.As(err, &tooLarge):
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
				return
			case !errors.Is(err, io.EOF):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
		}

		// Query params — merge url_params defaults with actual request values
		queryParams := make(map[string]any)
		if len(matchedFunc.Route.URLParams) > 0 {
			// Start with defaults from function config
			for k, def := range matchedFunc.Route.URLParams {
				queryParams[k] = def
			}
			// Override with actual request values (only for declared keys)
			for k := range matchedFunc.Route.URLParams {
				if v := r.URL.Query().Get(k); v != "" {
					queryParams[k] = v
				}
			}
		} else {
			// Backward compat: inject all URL query params for functions without url_params
			for key, values := range r.URL.Query() {
				if len(values) == 1 {
					queryParams[key] = values[0]
				} else {
					anyValues := make([]any, len(values))
					for i, v := range values {
						anyValues[i] = v
					}
					queryParams[key] = anyValues
				}
			}
		}
		if len(queryParams) > 0 {
			pCtx.Set("query", queryParams)
		}

		// Env variables from api_settings
		if len(cfg.APISettings.Env) > 0 {
			pCtx.Set("env", map[string]string(cfg.APISettings.Env))
		}

		// Build step deps
		deps := &pipeline.StepDeps{
			DBPool:            dbPool,
			Schema:            &cfg.Schema,
			Config:            cfg,
			Claims:            claims,
			BypassSchemaFlags: matchedFunc.BypassSchemaFlags,
			ResponseWriter:    w,
			MaxDepth:          pipeline.MaxCallDepth,
			CurrentDepth:      0,
			Functions:         cfg.Functions,
			FunctionID:        matchedFuncID,
			FunctionName:      matchedFunc.Name,
			Timezone:          loc,
		}

		// Execute with timeout
		execCtx, cancel := context.WithTimeout(r.Context(), pipeline.DefaultTimeout)
		defer cancel()

		err = pipeline.Execute(execCtx, matchedFunc.Steps, pCtx, deps)
		if errors.Is(err, pipeline.ErrResponseSent) {
			return // Response already written by the response step
		}
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("pipeline execution failed: %v", err))
			return
		}

		// No response step was executed
		w.WriteHeader(http.StatusNoContent)
	}
}
