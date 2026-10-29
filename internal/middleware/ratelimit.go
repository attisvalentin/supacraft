package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/httprate"
)

// RateLimitMiddleware enforces a per-tenant request rate limit using an
// in-memory sliding-window counter. The limit value is read from
// api_settings.rate_limit on the tenant config in context, so this middleware
// must be mounted AFTER TenantMiddleware.
//
// When rate_limit.enabled is false or requests_per_minute <= 0, the request
// passes through unlimited. Each tenant has its own counter — one tenant
// exhausting their limit cannot affect another.
func RateLimitMiddleware() func(http.Handler) http.Handler {
	keyFunc := func(r *http.Request) (string, error) {
		return r.Header.Get("X-Tenant-ID"), nil
	}

	return func(next http.Handler) http.Handler {
		limited := httprate.Limit(
			1_000_000,
			time.Minute,
			httprate.WithKeyFuncs(keyFunc),
		)(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg := TenantFromContext(r.Context())
			if cfg == nil || !cfg.APISettings.RateLimit.Enabled || cfg.APISettings.RateLimit.RequestsPerMinute <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			ctx := httprate.WithRequestLimit(r.Context(), cfg.APISettings.RateLimit.RequestsPerMinute)
			limited.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
