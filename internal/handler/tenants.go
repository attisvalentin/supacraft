package handler

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// HandleTenantsList: GET /admin/tenants
func HandleTenantsList(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Central {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a central server (start it with serve --data-dir)"})
			return
		}
		list, err := tenant.ListTenants(r.Context(), cfg.DataDir)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "list tenants: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": list})
	}
}

// HandleTenantSetDisabled: POST /admin/tenants/{tenantID}/disable and /enable
func HandleTenantSetDisabled(cfg *config.Config, store *tenant.CachedStore, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		if err := tenant.SetDisabled(cfg.DataDir, id, disabled); err != nil {
			writeTenantErr(w, r, err)
			return
		}
		store.Invalidate()
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleTenantRemove: DELETE /admin/tenants/{tenantID}[?purge=true] stops
// serving a tenant; with purge it also deletes its database, uploads and
// secrets (see tenant.RemoveTenant).
func HandleTenantRemove(cfg *config.Config, store *tenant.CachedStore, pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		purge := r.URL.Query().Get("purge") == "true"
		// For masking a failure message; the purge deletes the secrets.
		vals, _ := secrets.New(cfg.DataDir).Values(id)
		deployMu.Lock()
		defer deployMu.Unlock()
		if purge {
			// Stop serving before the files go, so no request reopens (and
			// recreates) the database meanwhile.
			if err := tenant.SetDisabled(cfg.DataDir, id, true); err != nil {
				writeTenantErr(w, r, err)
				return
			}
			store.Invalidate()
			pool.Drop(id)
		}
		res, err := tenant.RemoveTenant(r.Context(), cfg.DataDir, cfg.StoragePath, id, purge)
		store.Invalidate()
		pool.Drop(id)
		if errors.Is(err, tenant.ErrTenantNotFound) {
			writeTenantErr(w, r, err)
			return
		}
		if err != nil {
			// Only the operator sees this, and needs the whole message (a
			// storage error says what to fix before retrying).
			msg := MaskSecrets(err.Error(), vals)
			if errorlog.Global != nil {
				errorlog.Global.Log(id, r.Method, r.URL.Path, http.StatusInternalServerError, msg)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// HandleTenantErrors: GET /admin/tenants/{tenantID}/errors?limit=50&offset=0
// returns the tenant's recent API errors for the operator. Messages can
// quote anything a step touched, so the tenant's secret values are masked.
func HandleTenantErrors(cfg *config.Config, errLog *errorlog.ErrorLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		if errLog == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "error log is not available"})
			return
		}
		limit := queryInt(r, "limit", 50, 500)
		if limit == 0 {
			limit = 50
		}
		offset := queryInt(r, "offset", 0, 1<<30)
		entries, total, err := errLog.Query(r.Context(), id, limit, offset)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "read error log: "+err.Error())
			return
		}
		vals, err := secrets.New(cfg.DataDir).Values(id)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "read secrets: "+err.Error())
			return
		}
		for i := range entries {
			entries[i].ErrorMessage = MaskSecrets(entries[i].ErrorMessage, vals)
			entries[i].Path = MaskSecrets(entries[i].Path, vals)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": entries, "total": total})
	}
}

// MaskSecrets replaces every secret value found in s with "[secret NAME]",
// longest values first so one value inside another is still masked whole.
// Values under 4 bytes are left alone; masking them would garble the text
// without hiding anything guessable.
func MaskSecrets(s string, vals map[string]string) string {
	names := make([]string, 0, len(vals))
	for n, v := range vals {
		if len(v) >= 4 {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(vals[names[i]]) != len(vals[names[j]]) {
			return len(vals[names[i]]) > len(vals[names[j]])
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		s = strings.ReplaceAll(s, vals[n], "[secret "+n+"]")
	}
	return s
}

func queryInt(r *http.Request, key string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n < 0 {
		return def
	}
	return min(n, max)
}

func writeTenantErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, tenant.ErrTenantNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such tenant"})
		return
	}
	writeErrorJSON(w, r, http.StatusInternalServerError, err.Error())
}
