package middleware

import (
	"net/http"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// CORSPreflight answers every OPTIONS preflight request, mounted globally
// at the top of the router (before tenant resolution). A preflight request
// never carries the actual X-Tenant-ID header value — only
// Access-Control-Request-Headers, which lists header *names* the real
// request will use — so the tenant can't be resolved yet. Preflight
// negotiation doesn't expose tenant data by itself, so it's answered
// permissively; the real per-tenant policy is enforced on the actual
// request by CORSResponse.
func CORSPreflight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")

		reqHeaders := r.Header.Get("Access-Control-Request-Headers")
		if reqHeaders == "" {
			reqHeaders = "Content-Type, Authorization, X-Tenant-ID"
		}
		w.Header().Set("Access-Control-Allow-Headers", reqHeaders)

		w.WriteHeader(http.StatusNoContent)
	})
}

// CORSResponse sets Access-Control-Allow-Origin on the actual (non-preflight)
// response based on the resolved tenant's api_settings.cors.allowed_origin.
// Mounted before TenantMiddleware in the tenant route group so the header is
// already set even when TenantMiddleware itself returns an error response
// (e.g. 404 tenant not found, 503 tenant disabled) — those responses should
// still be readable by the configured frontend.
//
// "*" allows any origin. A specific allowed_origin only gets the header when
// it exactly matches the request's Origin header; otherwise no header is
// set and the browser blocks the response from being read by JS.
func CORSResponse(store *tenant.CachedStore, defaultTenant string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				tenantID = defaultTenant
			}

			if origin != "" && tenantID != "" {
				if cfg, err := store.GetTenantConfig(r.Context(), tenantID); err == nil {
					if allow := corsAllowOriginHeader(cfg.APISettings.CORS.AllowedOrigin, origin); allow != "" {
						w.Header().Set("Access-Control-Allow-Origin", allow)
						if allow != "*" {
							w.Header().Set("Vary", "Origin")
						}
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// corsAllowOriginHeader decides the Access-Control-Allow-Origin value for a
// tenant's configured allowed_origin and the request's Origin header.
// Returns "" when no header should be set.
func corsAllowOriginHeader(allowedOrigin, requestOrigin string) string {
	switch {
	case allowedOrigin == "*":
		return "*"
	case allowedOrigin != "" && allowedOrigin == requestOrigin:
		return requestOrigin
	default:
		return ""
	}
}
