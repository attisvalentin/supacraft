package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

type DeleteRequest struct {
	TableID string         `json:"table_id"`
	Where   map[string]any `json:"where"`
}

func HandleDelete(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.CRUDEndpoints.DeleteEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req DeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		tableSettings, ok := cfg.APISettings.Tables[req.TableID]
		if !ok || !tableSettings.Delete.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var claims *tenant.AuthClaims
		if tableSettings.Delete.Auth {
			var err error
			claims, err = validateJWT(r, cfg, pool)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		table, err := schema.FindTable(&cfg.Schema, req.TableID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "table not found"})
			return
		}

		whereSql, whereArgs, err := query.BuildWhere(req.Where, table, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		if tableSettings.Delete.RowPolicy != "" {
			policySql, policyArgs, err := query.BuildRowPolicy(tableSettings.Delete.RowPolicy, claims, len(whereArgs))
			if err != nil {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
				return
			}
			if policySql != "" {
				if whereSql != "" {
					whereSql += " AND (" + policySql + ")"
				} else {
					whereSql = "(" + policySql + ")"
				}
				whereArgs = append(whereArgs, policyArgs...)
			}
		}

		sql, args := query.BuildDelete(table.Name, whereSql, whereArgs)

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		result, err := dbPool.Exec(r.Context(), sql, args...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("delete failed: %v", err))
			return
		}

		affected, _ := result.RowsAffected()
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"deleted": affected},
		})
	}
}
