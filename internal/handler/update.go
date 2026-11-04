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

type UpdateRequest struct {
	TableID string         `json:"table_id"`
	Where   map[string]any `json:"where"`
	Data    map[string]any `json:"data"`
}

func HandleUpdate(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.CRUDEndpoints.UpdateEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		tableSettings, ok := cfg.APISettings.Tables[req.TableID]
		if !ok || !tableSettings.Update.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		claims, ok := authorize(w, r, cfg, pool, tableSettings.Update.Auth, tableSettings.Update.Roles)
		if !ok {
			return
		}

		table, err := schema.FindTable(&cfg.Schema, req.TableID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "table not found"})
			return
		}

		columns := make([]string, 0, len(req.Data))
		for col := range req.Data {
			columns = append(columns, col)
		}

		if err := schema.ValidateUpdateColumns(columns, table); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		whereSql, whereArgs, err := query.BuildWhere(req.Where, table, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		if tableSettings.Update.RowPolicy != "" {
			policySql, policyArgs, err := query.BuildRowPolicy(tableSettings.Update.RowPolicy, claims, len(whereArgs))
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

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		// RETURNING reads the rows as changed, even when the change moves
		// them out of the WHERE (status: pending → shipped).
		updateSQL, args := query.BuildUpdate(table.Name, req.Data, whereSql, whereArgs)
		results, err := tenant.WriteWithChanges(r.Context(), dbPool, cfg, table.Name, tenant.OpUpdate, func(q tenant.Querier) ([]map[string]any, error) {
			return queryRows(r.Context(), q, cfg, updateSQL+" RETURNING *", args...)
		})
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("update failed: %v", err))
			return
		}
		if len(results) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no matching record found"})
			return
		}

		row := schema.FilterNonReadableFromRow(results[0], table)
		writeJSON(w, http.StatusOK, map[string]any{"data": row})
	}
}
