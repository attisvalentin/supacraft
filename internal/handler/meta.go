package handler

import (
	"net/http"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/openapi"
)

// HandleOpenAPI: GET /meta/openapi.json describes the tenant's endpoints:
// what each accepts and returns, nothing else of the schema. Projects turn
// it off with api_settings.openapi: false.
func HandleOpenAPI(serverCfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		if !cfg.APISettings.OpenAPIEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		opts := openapi.Options{}
		if serverCfg.Central {
			opts.TenantHeader = cfg.DBName
		}
		writeJSON(w, http.StatusOK, openapi.Build(cfg, opts))
	}
}
