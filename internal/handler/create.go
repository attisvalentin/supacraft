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

type CreateRequest struct {
	TableID string         `json:"table_id"`
	Data    map[string]any `json:"data"`
}

func HandleCreate(pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())

		if !cfg.APISettings.CRUDEndpoints.CreateEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		tableSettings, ok := cfg.APISettings.Tables[req.TableID]
		if !ok || !tableSettings.Create.Enabled {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}

		if tableSettings.Create.Auth {
			if _, err := validateJWT(r, cfg, pool); err != nil {
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

		if err := schema.ValidateCreateColumns(columns, table); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("database connection failed: %v", err))
			return
		}

		insertSQL, args := query.BuildInsert(table.Name, req.Data)
		res, err := dbPool.Exec(r.Context(), insertSQL, args...)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("insert failed: %v", err))
			return
		}

		row, err := readInsertedRow(r, dbPool, table, res, cfg)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		if row == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no result returned"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{"data": row})
	}
}

// readInsertedRow re-fetches the freshly inserted row via last_insert_rowid()
// and applies readable-flag filtering. Returns nil if no row matches.
func readInsertedRow(r *http.Request, dbPool *tenant.DB, table *tenant.SchemaTable, res interface{ LastInsertId() (int64, error) }, cfg *tenant.TenantConfig) (map[string]any, error) {
	lastID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last_insert_rowid: %v", err)
	}

	selectSQL := "SELECT * FROM " + query.QuoteIdent(table.Name) + " WHERE rowid = ?"
	rows, err := dbPool.Query(r.Context(), selectSQL, lastID)
	if err != nil {
		return nil, fmt.Errorf("select inserted: %v", err)
	}
	defer rows.Close()

	loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
	results, err := query.ScanAllRowsToMaps(rows, loc)
	if err != nil {
		return nil, fmt.Errorf("scan inserted: %v", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return schema.FilterNonReadableFromRow(results[0], table), nil
}
