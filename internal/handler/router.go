package handler

import (
	"net/http"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/go-chi/chi/v5"
)

func NewRouter(store *tenant.CachedStore, pool *tenant.PoolManager, errLog *errorlog.ErrorLog, serverCfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.CORSPreflight)
	r.Use(middleware.RequestLogger())

	// Tenant API — X-Tenant-ID selects the tenant, falling back to DEFAULT_TENANT.
	r.Group(func(r chi.Router) {
		r.Use(middleware.CORSResponse(store, serverCfg.DefaultTenant))
		r.Use(middleware.TenantMiddleware(store, serverCfg.DefaultTenant))
		r.Use(middleware.RateLimitMiddleware())

		// CRUD endpoints
		r.Post("/query", HandleQuery(pool))
		r.Post("/create", HandleCreate(pool))
		r.Post("/update", HandleUpdate(pool))
		r.Post("/delete", HandleDelete(pool))

		// Meta
		r.Get("/meta/schema", HandleMeta)

		// Auth
		r.Post("/auth/register", HandleRegister(pool))
		r.Post("/auth/login", HandleLogin(pool))
		r.Post("/auth/refresh", HandleRefresh(pool))

		// Files
		r.Post("/files", HandleFileUpload(pool, serverCfg))
		r.Get("/files/*", HandleFileDownload(pool, serverCfg))
		r.Delete("/files/*", HandleFileDelete(pool, serverCfg))

		// Logs
		r.Get("/logs", HandleLogs(errLog))

		// Pipeline functions
		r.HandleFunc("/fn/*", HandleFunction(pool))
	})

	// Operator studio proxy — HMAC-authenticated, no tenant header. Tenant is
	// addressed by URL path. Disabled unless STUDIO_SECRET is set.
	r.Group(func(r chi.Router) {
		r.Use(middleware.StudioAuth(serverCfg.StudioSecret))

		r.Get("/admin/studio/tenants", HandleStudioTenants(store))
		r.Get("/admin/studio/tenants/{tenantID}/schema", HandleStudioSchema(store))
		r.Post("/admin/studio/tenants/{tenantID}/query", HandleStudioQuery(store, pool))
	})

	return r
}
