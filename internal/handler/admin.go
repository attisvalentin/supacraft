package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/envref"
	"github.com/attisvalentin/supacraft/internal/secrets"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/go-chi/chi/v5"
)

var adminTenantRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// SecretsStatus is the response of GET /admin/tenants/{id}/secrets. It
// carries names only; secret values are never returned.
type SecretsStatus struct {
	Tenant     string   `json:"tenant"`
	Deployed   bool     `json:"deployed"`
	Set        []string `json:"set"`
	Referenced []string `json:"referenced"`
	Missing    []string `json:"missing"`
}

// adminTenant returns the tenant id from the URL, or writes an error. Admin
// endpoints only exist on a central server (serve --data-dir).
func adminTenant(w http.ResponseWriter, r *http.Request, cfg *config.Config) (string, bool) {
	if !cfg.Central {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a central server (start it with serve --data-dir)"})
		return "", false
	}
	id := chi.URLParam(r, "tenantID")
	if !adminTenantRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return "", false
	}
	return id, true
}

// SecretsStatusFor compares a tenant's stored secret names with the names its
// deployed config references.
func SecretsStatusFor(dataDir, id string) (*SecretsStatus, error) {
	set, err := secrets.New(dataDir).Names(id)
	if err != nil {
		return nil, err
	}
	st := &SecretsStatus{Tenant: id, Set: set, Referenced: []string{}, Missing: []string{}}
	data, err := os.ReadFile(filepath.Join(dataDir, "tenants", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	st.Deployed = true
	have := map[string]bool{}
	for _, n := range set {
		have[n] = true
	}
	for _, n := range envref.Names(raw["api_settings"]) {
		st.Referenced = append(st.Referenced, n)
		if !have[n] {
			st.Missing = append(st.Missing, n)
		}
	}
	return st, nil
}

// HandleSecretsList: GET /admin/tenants/{tenantID}/secrets
func HandleSecretsList(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		st, err := SecretsStatusFor(cfg.DataDir, id)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "read secrets: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// HandleSecretSet: PUT /admin/tenants/{tenantID}/secrets/{name} {"value": "..."}
func HandleSecretSet(cfg *config.Config, store *tenant.CachedStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		var body struct {
			Value *string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"value": "..."}`})
			return
		}
		if err := secrets.New(cfg.DataDir).Set(id, chi.URLParam(r, "name"), *body.Value); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		store.Invalidate()
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSecretUnset: DELETE /admin/tenants/{tenantID}/secrets/{name}
func HandleSecretUnset(cfg *config.Config, store *tenant.CachedStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		if err := secrets.New(cfg.DataDir).Unset(id, chi.URLParam(r, "name")); err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, "unset secret: "+err.Error())
			return
		}
		store.Invalidate()
		w.WriteHeader(http.StatusNoContent)
	}
}
