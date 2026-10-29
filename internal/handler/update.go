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

		var claims *tenant.AuthClaims
		if tableSettings.Update.Auth {
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

		updateSQL, args := query.BuildUpdate(table.Name, req.Data, whereSql, whereArgs)
		res, err := dbPool.Exec(r.Context(), updateSQL, args...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("update failed: %v", err))
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no matching record found"})
			return
		}

		// Re-SELECT updated rows using the same WHERE clause.
		colNames := make([]string, len(table.Columns))
		for i, c := range table.Columns {
			colNames[i] = c.Name
		}
		selectSQL, selectArgs := query.BuildSelect(table.Name, colNames, whereSql, whereArgs, "", "", nil)
		rows, err := dbPool.Query(r.Context(), selectSQL, selectArgs...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("select updated: %v", err))
			return
		}
		defer rows.Close()

		loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
		results, err := query.ScanAllRowsToMaps(rows, loc)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("scan updated: %v", err))
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
