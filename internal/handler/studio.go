package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"github.com/go-chi/chi/v5"
)

// The /admin/studio endpoints back the CLI "supacraft studio --proxy" mode: an
// operator-authenticated, read-only view of tenant data for deployments where
// the SQLite data is not on the operator's machine. They reuse the
// schema-validated query builder, so only schema-defined tables and readable
// columns are ever exposed.

const (
	studioDefaultLimit = 50
	studioMaxLimit     = 500
)

type studioTenant struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	DBName      string `json:"db_name"`
	TableCount  int    `json:"table_count"`
}

type studioColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	PK       bool   `json:"pk"`
	Readable *bool  `json:"readable,omitempty"`
}

type studioTable struct {
	Name    string         `json:"name"`
	Columns []studioColumn `json:"columns"`
}

type studioRelation struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	Type       string `json:"type"`
}

type studioSchemaResp struct {
	Tables    []studioTable    `json:"tables"`
	Relations []studioRelation `json:"relations"`
}

type studioFilter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

type studioQueryReq struct {
	Table   string         `json:"table"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
	OrderBy string         `json:"order_by"`
	Desc    bool           `json:"desc"`
	Filters []studioFilter `json:"filters"`
}

type studioRowsResp struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// HandleStudioTenants lists every tenant with its db_name and table count.
func HandleStudioTenants(store *tenant.CachedStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ids, err := store.ScanTenantIDs(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]studioTenant, 0, len(ids))
		for _, id := range ids {
			cfg, err := store.GetTenantConfig(r.Context(), id)
			if err != nil {
				continue
			}
			display := cfg.DisplayName
			if display == "" {
				display = id
			}
			out = append(out, studioTenant{
				ID:          id,
				DisplayName: display,
				DBName:      cfg.DBName,
				TableCount:  len(cfg.Schema.Tables),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
		writeJSON(w, http.StatusOK, out)
	}
}

// HandleStudioSchema returns a tenant's schema (readable columns only).
func HandleStudioSchema(store *tenant.CachedStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := studioLoadConfig(w, r, store)
		if !ok {
			return
		}

		tables := make([]studioTable, 0, len(cfg.Schema.Tables))
		for i := range cfg.Schema.Tables {
			t := &cfg.Schema.Tables[i]
			cols := make([]studioColumn, 0, len(t.Columns))
			for j := range t.Columns {
				c := &t.Columns[j]
				if !c.IsReadable() {
					continue
				}
				pk := c.Type == "primary"
				cols = append(cols, studioColumn{
					Name:     c.Name,
					Type:     c.Type,
					Nullable: !pk,
					PK:       pk,
					Readable: c.Readable,
				})
			}
			tables = append(tables, studioTable{Name: t.Name, Columns: cols})
		}

		relations := make([]studioRelation, 0, len(cfg.Schema.Relations))
		for _, rel := range cfg.Schema.Relations {
			relations = append(relations, studioRelation{
				FromTable: rel.From.Table, FromColumn: rel.From.Column,
				ToTable: rel.To.Table, ToColumn: rel.To.Column, Type: rel.Type,
			})
		}

		writeJSON(w, http.StatusOK, studioSchemaResp{Tables: tables, Relations: relations})
	}
}

// HandleStudioQuery runs a read-only, schema-validated SELECT against a tenant.
func HandleStudioQuery(store *tenant.CachedStore, pool *tenant.PoolManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := studioLoadConfig(w, r, store)
		if !ok {
			return
		}

		var req studioQueryReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		table := studioFindTable(cfg, req.Table)
		if table == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "table not found"})
			return
		}

		readable := schema.GetReadableColumns(table)
		columns := make([]string, len(readable))
		for i, c := range readable {
			columns[i] = c.Name
		}
		if len(columns) == 0 {
			writeJSON(w, http.StatusOK, studioRowsResp{Columns: []string{}, Rows: []map[string]any{}, Limit: req.Limit, Offset: req.Offset})
			return
		}

		where, err := studioFiltersToWhere(req.Filters)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		whereSql, whereArgs, err := query.BuildWhere(where, table, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		orderBySql := ""
		if req.OrderBy != "" {
			dir := "asc"
			if req.Desc {
				dir = "desc"
			}
			orderBySql, err = query.BuildOrderBy(map[string]string{req.OrderBy: dir}, table)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}

		limit := req.Limit
		if limit <= 0 {
			limit = studioDefaultLimit
		}
		if limit > studioMaxLimit {
			limit = studioMaxLimit
		}
		offset := req.Offset
		if offset < 0 {
			offset = 0
		}

		paginationSql, paginationArgs := query.ApplyPagination(limit, offset, len(whereArgs))
		sql, args := query.BuildSelect(table.Name, columns, whereSql, whereArgs, orderBySql, paginationSql, paginationArgs)

		dbPool, err := pool.GetPool(r.Context(), cfg)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("database connection failed: %v", err)})
			return
		}

		rows, err := dbPool.Query(r.Context(), sql, args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("query failed: %v", err)})
			return
		}

		loc := tenant.LoadTimezone(cfg.APISettings.Timezone)
		all, err := query.ScanAllRowsToMaps(rows, loc)
		rows.Close()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("scan rows: %v", err)})
			return
		}

		results := make([]map[string]any, 0, len(all))
		for _, row := range all {
			results = append(results, schema.FilterNonReadableFromRow(row, table))
		}

		var total int
		countSql, countArgs := query.BuildCount(table.Name, whereSql, whereArgs)
		_ = dbPool.QueryRow(r.Context(), countSql, countArgs...).Scan(&total)

		writeJSON(w, http.StatusOK, studioRowsResp{
			Columns: columns,
			Rows:    results,
			Total:   total,
			Limit:   limit,
			Offset:  offset,
		})
	}
}

func studioLoadConfig(w http.ResponseWriter, r *http.Request, store *tenant.CachedStore) (*tenant.TenantConfig, bool) {
	id := chi.URLParam(r, "tenantID")
	cfg, err := store.GetTenantConfig(r.Context(), id)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return nil, false
	}
	return cfg, true
}

func studioFindTable(cfg *tenant.TenantConfig, name string) *tenant.SchemaTable {
	for i := range cfg.Schema.Tables {
		if cfg.Schema.Tables[i].Name == name {
			return &cfg.Schema.Tables[i]
		}
	}
	return nil
}

// studioFiltersToWhere translates studio filters into the query builder's where
// map. Unknown operators are rejected; "like" becomes a %contains% match.
func studioFiltersToWhere(filters []studioFilter) (map[string]any, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	where := make(map[string]any, len(filters))
	for _, f := range filters {
		var op string
		var val any = f.Value
		switch f.Op {
		case "eq":
			op = "$eq"
		case "neq":
			op = "$ne"
		case "gt":
			op = "$gt"
		case "gte":
			op = "$gte"
		case "lt":
			op = "$lt"
		case "lte":
			op = "$lte"
		case "like":
			op = "$like"
			val = "%" + f.Value + "%"
		default:
			return nil, fmt.Errorf("unsupported filter operator %q", f.Op)
		}
		where[f.Column] = map[string]any{op: val}
	}
	return where, nil
}
