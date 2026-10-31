package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// DeployRequest is the body of PUT /admin/tenants/{tenantID}.
type DeployRequest struct {
	Project map[string]any `json:"project"`
	Replace bool           `json:"replace,omitempty"`
	DryRun  bool           `json:"dry_run,omitempty"`
	// AllowMissingSecrets deploys even when "${NAME}" references have no
	// secret yet; they resolve to "" until set.
	AllowMissingSecrets bool `json:"allow_missing_secrets,omitempty"`
}

// DeployResponse reports what a deploy changed (or would change).
type DeployResponse struct {
	Tenant         string          `json:"tenant"`
	DryRun         bool            `json:"dry_run"`
	Created        bool            `json:"database_created"`
	Migrations     []string        `json:"migrations"`
	MissingSecrets []string        `json:"missing_secrets"`
	Warnings       []project.Issue `json:"warnings"`
}

// deployMu serialises deploys: two at once could interleave a migration and
// a config write.
var deployMu sync.Mutex

// HandleDeploy: PUT /admin/tenants/{tenantID} — validate a supacraft.json,
// migrate the tenant's database (additive) and write its config. Nothing is
// changed when validation fails, the tenant id belongs to another project,
// or secrets are missing.
func HandleDeploy(cfg *config.Config, store *tenant.CachedStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		var req DeployRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Project == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"project": {...supacraft.json...}}`})
			return
		}
		p := project.FromRaw(req.Project)
		if p.TenantID() != id {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project name maps to tenant " + p.TenantID() + ", not " + id})
			return
		}

		deployMu.Lock()
		defer deployMu.Unlock()
		res, err := p.ApplyTo(r.Context(), cfg.DataDir, project.ApplyOptions{
			Replace:        req.Replace,
			DryRun:         req.DryRun,
			RequireSecrets: !req.AllowMissingSecrets,
		})
		var invalid *project.ValidationError
		var missing *project.MissingSecretsError
		switch {
		case errors.As(err, &invalid):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid supacraft.json", "issues": invalid.Issues})
			return
		case errors.As(err, &missing):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "missing secrets", "missing_secrets": missing.Names})
			return
		case errors.Is(err, project.ErrTenantTaken):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		case err != nil:
			writeErrorJSON(w, r, http.StatusInternalServerError, "deploy failed: "+err.Error())
			return
		}
		if !req.DryRun {
			store.Invalidate()
		}

		warnings := []project.Issue{}
		for _, i := range p.Validate() {
			if i.Severity != "error" {
				warnings = append(warnings, i)
			}
		}
		missingNames := res.MissingEnv
		if missingNames == nil {
			missingNames = []string{}
		}
		writeJSON(w, http.StatusOK, DeployResponse{
			Tenant:         res.TenantID,
			DryRun:         req.DryRun,
			Created:        res.Created,
			Migrations:     res.Migrations,
			MissingSecrets: missingNames,
			Warnings:       warnings,
		})
	}
}
