package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

type QueryRequest struct {
	TableID string                 `json:"table_id"`
	Select  []string               `json:"select"`
	Include []query.IncludeRequest `json:"include"`
	Where   map[string]any         `json:"where"`
	OrderBy map[string]string      `json:"order_by"`
	Limit   int                    `json:"limit"`
	Offset  int                    `json:"offset"`
	Cursor  string                 `json:"cursor"`
}

func HandleQuery(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.CRUDEndpoints.QueryEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		tableSettings, ok := cfg.APISettings.Tables[req.TableID]
		if !ok || !tableSettings.Query.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var claims *tenant.AuthClaims
		if tableSettings.Query.Auth {
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

		columns := req.Select
		if len(columns) == 0 {
			readable := schema.GetReadableColumns(table)
			columns = make([]string, len(readable))
			for i, c := range readable {
				columns[i] = c.Name
			}
		} else {
			if err := schema.ValidateSelectColumns(columns, table); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}

		whereSql, whereArgs, err := query.BuildWhere(req.Where, table, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		var cursorCol string
		if req.Cursor != "" {
			cursor, err := query.DecodeCursor(req.Cursor)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}

			col, err := schema.FindColumn(table, cursor.Column)
			if err != nil || !col.IsReadable() {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cursor column"})
				return
			}

			cursorCol = cursor.Column

			cursorWhere, cursorArgs := query.ApplyCursor(cursor, len(whereArgs))
			if whereSql != "" {
				whereSql += " AND " + cursorWhere
			} else {
				whereSql = cursorWhere
			}
			whereArgs = append(whereArgs, cursorArgs...)

			req.OrderBy = map[string]string{cursor.Column: cursor.Direction}
		}

		if tableSettings.Query.RowPolicy != "" {
			policySql, policyArgs, err := query.BuildRowPolicy(tableSettings.Query.RowPolicy, claims, len(whereArgs))
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

		orderBySql, err := query.BuildOrderBy(req.OrderBy, table)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		paginationSql, paginationArgs := query.ApplyPagination(req.Limit, req.Offset, len(whereArgs))
		sql, args := query.BuildSelect(table.Name, columns, whereSql, whereArgs, orderBySql, paginationSql, paginationArgs)

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		rows, err := dbPool.Query(r.Context(), sql, args...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
			return
		}

		loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
		all, err := query.ScanAllRowsToMaps(rows, loc)
		rows.Close()
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("scan rows: %v", err))
			return
		}

		results := make([]map[string]any, 0, len(all))
		for _, row := range all {
			results = append(results, schema.FilterNonReadableFromRow(row, table))
		}

		if err := query.ResolveIncludes(r.Context(), dbPool, req.TableID, results, req.Include, &cfg.Schema, loc); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		response := map[string]any{
			"data": results,
		}

		if req.Limit > 0 {
			countSql, countArgs := query.BuildCount(table.Name, whereSql, whereArgs)
			var total int
			dbPool.QueryRow(r.Context(), countSql, countArgs...).Scan(&total)

			meta := map[string]any{
				"total":    total,
				"limit":    req.Limit,
				"offset":   req.Offset,
				"has_more": req.Offset+len(results) < total,
			}

			if len(results) > 0 && len(results) == req.Limit {
				sortCol := cursorCol
				if sortCol == "" {
					for col := range req.OrderBy {
						sortCol = col
						break
					}
				}
				if sortCol == "" {
					sortCol = "id"
				}
				sortDir := "ASC"
				if d, ok := req.OrderBy[sortCol]; ok {
					sortDir = strings.ToUpper(d)
				}

				lastRow := results[len(results)-1]
				if lastVal, ok := lastRow[sortCol]; ok {
					meta["next_cursor"] = query.EncodeCursor(sortCol, lastVal, sortDir)
				}
			}

			response["meta"] = meta
		}

		writeJSON(w, http.StatusOK, response)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeErrorJSON(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if errorlog.Global != nil {
		errorlog.Global.Log(r.Header.Get("X-Tenant-ID"), r.Method, r.URL.Path, status, msg)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
