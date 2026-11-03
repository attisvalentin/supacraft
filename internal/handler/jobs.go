package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// HandleJobsList: GET /admin/tenants/{tenantID}/jobs?status=failed&limit=50
// lists a tenant's background jobs, newest first. Last errors can quote
// anything a step touched, so secret values are masked.
func HandleJobsList(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		status := r.URL.Query().Get("status")
		switch status {
		case "", tenant.JobPending, tenant.JobRunning, tenant.JobDone, tenant.JobFailed:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be pending, running, done or failed"})
			return
		}
		limit := queryInt(r, "limit", 50, 500)
		if limit == 0 {
			limit = 50
		}
		list, err := tenant.ListTenantJobs(r.Context(), cfg.DataDir, id, status, limit)
		if err != nil {
			writeTenantErr(w, r, err)
			return
		}
		if err := maskJobs(cfg.DataDir, id, list); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "read secrets: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": list})
	}
}

// HandleJobRetry: POST /admin/tenants/{tenantID}/jobs/{jobID}/retry queues
// a failed job again.
func HandleJobRetry(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		err := tenant.RetryTenantJob(r.Context(), cfg.DataDir, id, chi.URLParam(r, "jobID"))
		switch {
		case errors.Is(err, tenant.ErrJobNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case err != nil:
			writeTenantErr(w, r, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

// EnqueueRequest is the body of POST /admin/tenants/{tenantID}/jobs.
type EnqueueRequest struct {
	Function string         `json:"function"`
	Args     map[string]any `json:"args,omitempty"`
	Delay    int            `json:"delay,omitempty"` // seconds
}

// HandleJobEnqueue: POST /admin/tenants/{tenantID}/jobs queues a PRIVATE
// function to run as a job.
func HandleJobEnqueue(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		var req EnqueueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Function == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"function\", \"args\"?, \"delay\"? (0-2592000 seconds)}"})
			return
		}
		jobID, err := tenant.EnqueueTenantJob(r.Context(), cfg.DataDir, id, req.Function, req.Args, req.Delay)
		var fnErr *tenant.JobFunctionError
		switch {
		case errors.As(err, &fnErr), errors.Is(err, tenant.ErrJobDelay):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case err != nil:
			writeTenantErr(w, r, err)
		default:
			writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]string{"id": jobID}})
		}
	}
}

// maskJobs replaces the tenant's secret values in job errors and args.
func maskJobs(dataDir, id string, list []tenant.Job) error {
	vals, err := secrets.New(dataDir).Values(id)
	if err != nil {
		return err
	}
	MaskJobs(list, vals)
	return nil
}

// MaskJobs masks secret values in job errors and args, nested ones too.
func MaskJobs(list []tenant.Job, vals map[string]string) {
	for i := range list {
		list[i].LastError = MaskSecrets(list[i].LastError, vals)
		for k, v := range list[i].Args {
			list[i].Args[k] = MaskValue(v, vals)
		}
	}
}

// MaskValue masks secret values in every string of a JSON value.
func MaskValue(v any, vals map[string]string) any {
	switch t := v.(type) {
	case string:
		return MaskSecrets(t, vals)
	case map[string]any:
		for k, x := range t {
			t[k] = MaskValue(x, vals)
		}
	case []any:
		for i, x := range t {
			t[i] = MaskValue(x, vals)
		}
	}
	return v
}
