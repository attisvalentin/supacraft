package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

type contextKey string

const tenantContextKey contextKey = "tenant"

// validTenantID matches the ids the CLI generates (project.Slug). The id is
// used in file paths and as the rate-limit key, so anything else — "../x",
// "./demo" — is rejected before it reaches the filesystem.
var validTenantID = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func TenantMiddleware(store *tenant.CachedStore, defaultTenant string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				tenantID = defaultTenant
				// Downstream handlers read the tenant from the header.
				r.Header.Set("X-Tenant-ID", tenantID)
			}
			if tenantID == "" {
				writeError(w, http.StatusBadRequest, "X-Tenant-ID header is required")
				return
			}
			if !validTenantID.MatchString(tenantID) {
				writeError(w, http.StatusNotFound, "tenant not found")
				return
			}

			cfg, err := store.GetTenantConfig(r.Context(), tenantID)
			if errors.Is(err, tenant.ErrTenantNotFound) {
				writeError(w, http.StatusNotFound, "tenant not found")
				return
			}
			if err != nil {
				msg := fmt.Sprintf("failed to load tenant config: %v", err)
				slog.Error("failed to load tenant config", "tenant_id", tenantID, "error", err)
				if errorlog.Global != nil {
					errorlog.Global.Log(tenantID, r.Method, r.URL.Path, http.StatusInternalServerError, msg)
				}
				writeError(w, http.StatusInternalServerError, "failed to load tenant config")
				return
			}

			if !cfg.Enabled {
				writeError(w, http.StatusServiceUnavailable, "tenant is disabled")
				return
			}

			ctx := context.WithValue(r.Context(), tenantContextKey, cfg)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func TenantFromContext(ctx context.Context) *tenant.TenantConfig {
	cfg, _ := ctx.Value(tenantContextKey).(*tenant.TenantConfig)
	return cfg
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
