package handler

import (
	"net/http"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/go-chi/chi/v5"
)

// authAttemptsPerMinute limits /auth/* calls per tenant and client IP.
const authAttemptsPerMinute = 20

func NewRouter(store *tenant.CachedStore, pool *tenant.PoolManager, errLog *errorlog.ErrorLog, serverCfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.CORSPreflight)
	r.Use(middleware.RequestLogger())

	// Tenant API — X-Tenant-ID selects the tenant, falling back to DEFAULT_TENANT.
	r.Group(func(r chi.Router) {
		r.Use(middleware.CORSResponse(store, serverCfg.DefaultTenant))
		r.Use(middleware.TenantMiddleware(store, serverCfg.DefaultTenant))
		r.Use(middleware.RateLimitMiddleware(serverCfg.TrustProxy))

		// Uploads enforce MaxFileSize themselves; everything else is JSON.
		r.Post("/files", HandleFileUpload(pool, serverCfg))

		r.Group(func(r chi.Router) {
			r.Use(middleware.MaxBody(serverCfg.MaxBodySize))

			// CRUD endpoints
			r.Post("/query", HandleQuery(pool))
			r.Post("/create", HandleCreate(pool))
			r.Post("/update", HandleUpdate(pool))
			r.Post("/delete", HandleDelete(pool))

			// Meta
			r.Get("/meta/schema", HandleMeta)

			// Auth
			r.Group(func(r chi.Router) {
				r.Use(middleware.AuthRateLimit(authAttemptsPerMinute, serverCfg.TrustProxy))
				r.Post("/auth/register", HandleRegister(pool))
				r.Post("/auth/login", HandleLogin(pool))
				r.Post("/auth/refresh", HandleRefresh(pool))
				r.Post("/auth/forgot-password", HandleForgotPassword(pool))
				r.Post("/auth/reset-password", HandleResetPassword(pool))
				r.Post("/auth/verify-email", HandleVerifyEmail(pool))
				r.Post("/auth/resend-verification", HandleResendVerification(pool))
			})

			// Files
			r.Get("/files/*", HandleFileDownload(pool, serverCfg))
			r.Delete("/files/*", HandleFileDelete(pool, serverCfg))

			// Pipeline functions
			r.HandleFunc("/fn/*", HandleFunction(pool))
		})
	})

	// Operator studio proxy — HMAC-authenticated, no tenant header. Tenant is
	// addressed by URL path. Disabled unless STUDIO_SECRET is set.
	r.Group(func(r chi.Router) {
		r.Use(middleware.StudioAuth(serverCfg.StudioSecret))

		r.Get("/admin/studio/tenants", HandleStudioTenants(store))
		r.Get("/admin/studio/tenants/{tenantID}/schema", HandleStudioSchema(store))
		r.Post("/admin/studio/tenants/{tenantID}/query", HandleStudioQuery(store, pool))
	})

	// Central-server management (deploys, secrets, tenants, error logs) — HMAC-signed with
	// SUPACRAFT_ADMIN_SECRET; disabled unless it is set.
	r.Group(func(r chi.Router) {
		r.Use(middleware.AdminAuth(serverCfg.AdminSecret))

		r.Get("/admin/tenants", HandleTenantsList(serverCfg))
		r.Put("/admin/tenants/{tenantID}", HandleDeploy(serverCfg, store))
		r.Delete("/admin/tenants/{tenantID}", HandleTenantRemove(serverCfg, store, pool))
		r.Post("/admin/tenants/{tenantID}/disable", HandleTenantSetDisabled(serverCfg, store, true))
		r.Post("/admin/tenants/{tenantID}/enable", HandleTenantSetDisabled(serverCfg, store, false))
		r.Get("/admin/tenants/{tenantID}/errors", HandleTenantErrors(serverCfg, errLog))
		r.Get("/admin/tenants/{tenantID}/secrets", HandleSecretsList(serverCfg))
		r.Put("/admin/tenants/{tenantID}/secrets/{name}", HandleSecretSet(serverCfg, store))
		r.Delete("/admin/tenants/{tenantID}/secrets/{name}", HandleSecretUnset(serverCfg, store))
	})

	return r
}
