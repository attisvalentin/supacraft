// Package studio is a local, read-only database viewer for SupaCraft projects
// (Drizzle-Studio style). It serves a small web UI plus a JSON API over the
// compiled tenant config in {DataDir}/tenants/{id}.json and the SQLite file at
// {DataDir}/{db_name}.db. The DataSource interface keeps the HTTP layer and
// frontend independent of where the data comes from (local file or a remote
// server via ProxySource).
package studio

import "context"

// TenantSummary is the sidebar entry for one tenant.
type TenantSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	DBName      string `json:"db_name"`
	TableCount  int    `json:"table_count"`
}

// Column describes one column. Type/Nullable/PK come from live DB introspection;
// Readable is enriched from the tenant config schema when present (so the UI can
// hint which columns the API would actually expose).
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	PK       bool   `json:"pk"`
	Readable *bool  `json:"readable,omitempty"`
}

// Table is a single table and its columns.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Relation is a foreign-key style relationship taken from the config schema.
type Relation struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	Type       string `json:"type"`
}

// TenantSchema is the full schema for one tenant.
type TenantSchema struct {
	Tables    []Table    `json:"tables"`
	Relations []Relation `json:"relations"`
}

// Filter is a single WHERE clause: Column <Op> Value, with Value always bound
// as a parameter. Op is one of eq, neq, gt, gte, lt, lte, like.
type Filter struct {
	Column string
	Op     string
	Value  string
}

// QueryOptions controls a row query. Limit is clamped by the source.
type QueryOptions struct {
	Limit   int
	Offset  int
	OrderBy string
	Desc    bool
	Filters []Filter
}

// RowsPage is one page of rows plus the total matching count for pagination.
type RowsPage struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// DataSource is the backend the HTTP layer talks to: SQLiteSource for local
// files, ProxySource for a remote server.
type DataSource interface {
	ListTenants(ctx context.Context) ([]TenantSummary, error)
	GetSchema(ctx context.Context, tenantID string) (TenantSchema, error)
	QueryRows(ctx context.Context, tenantID, table string, opts QueryOptions) (RowsPage, error)
	Close() error
}
