package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/httprate"
)

// clientKey identifies the caller for rate limiting. Behind a reverse proxy
// every request comes from the proxy's address, so trustProxy switches to the
// X-Real-IP / X-Forwarded-For headers. Those headers are client-controlled
// when the API is reached directly, so they are ignored unless trusted.
func clientKey(trustProxy bool) httprate.KeyFunc {
	if trustProxy {
		return httprate.KeyByRealIP
	}
	return httprate.KeyByIP
}

// RateLimitMiddleware enforces api_settings.rate_limit per tenant and client
// IP, so one noisy client cannot use up the quota of every other client. The
// limit is read from the tenant config in context, so this middleware must be
// mounted AFTER TenantMiddleware.
//
// When rate_limit.enabled is false or requests_per_minute <= 0, the request
// passes through unlimited.
func RateLimitMiddleware(trustProxy bool) func(http.Handler) http.Handler {
	tenantKey := func(r *http.Request) (string, error) {
		return r.Header.Get("X-Tenant-ID"), nil
	}

	return func(next http.Handler) http.Handler {
		limited := httprate.Limit(
			1_000_000,
			time.Minute,
			httprate.WithKeyFuncs(tenantKey, clientKey(trustProxy)),
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

// AuthRateLimit caps login, register and refresh attempts per tenant and
// client IP regardless of api_settings.rate_limit, to slow down password
// guessing.
func AuthRateLimit(perMinute int, trustProxy bool) func(http.Handler) http.Handler {
	tenantKey := func(r *http.Request) (string, error) {
		return r.Header.Get("X-Tenant-ID"), nil
	}
	return httprate.Limit(perMinute, time.Minute,
		httprate.WithKeyFuncs(tenantKey, clientKey(trustProxy)))
}

// MaxBody caps the request body at n bytes. Handlers that read more get an
// error from the body reader.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
