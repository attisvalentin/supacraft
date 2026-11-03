package handler

import (
	"context"
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

		if _, ok := authorize(w, r, cfg, pool, tableSettings.Create.Auth, tableSettings.Create.Roles); !ok {
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
		row, err := insertRow(r.Context(), dbPool, cfg, table, insertSQL, args)
		if err != nil {
			writeErrorJSON(w, r, http.StatusInternalServerError, fmt.Sprintf("insert failed: %v", err))
			return
		}
		if row == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no result returned"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{"data": row})
	}
}

// insertRow runs an INSERT and its webhooks atomically and returns the new
// row (readable columns only), nil if it can't be read back.
func insertRow(ctx context.Context, db *tenant.DB, cfg *tenant.TenantConfig, table *tenant.SchemaTable, insertSQL string, args []any) (map[string]any, error) {
	rows, err := tenant.WriteWithWebhooks(ctx, db, cfg, table.Name, tenant.OpInsert, func(q tenant.Querier) ([]map[string]any, error) {
		res, err := q.Exec(ctx, insertSQL, args...)
		if err != nil {
			return nil, err
		}
		lastID, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("last_insert_rowid: %w", err)
		}
		return queryRows(ctx, q, cfg, "SELECT * FROM "+query.QuoteIdent(table.Name)+" WHERE rowid = ?", lastID)
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return schema.FilterNonReadableFromRow(rows[0], table), nil
}
