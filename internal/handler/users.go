package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// HandleUsersList: GET /admin/tenants/{tenantID}/users?limit=50&offset=0
// lists a tenant's users with their roles (never password hashes).
func HandleUsersList(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		limit := queryInt(r, "limit", 50, 500)
		if limit == 0 {
			limit = 50
		}
		users, total, err := tenant.ListUsers(r.Context(), cfg.DataDir, id, limit, queryInt(r, "offset", 0, 1<<30))
		if err != nil {
			writeTenantErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": users, "total": total})
	}
}

// SetRoleRequest is the body of POST /admin/tenants/{tenantID}/users/role.
type SetRoleRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// HandleUserSetRole: POST /admin/tenants/{tenantID}/users/role gives a user
// one of the tenant's roles.
func HandleUserSetRole(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		var req SetRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Role == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"email\": ..., \"role\": ...}"})
			return
		}
		user, err := tenant.SetUserRole(r.Context(), cfg.DataDir, id, req.Email, req.Role)
		var unknown *tenant.UnknownRoleError
		switch {
		case errors.Is(err, tenant.ErrUserNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, tenant.ErrNoRoles), errors.As(err, &unknown):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case err != nil:
			writeTenantErr(w, r, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"data": user})
		}
	}
}
