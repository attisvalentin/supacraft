package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/tenantfile"
)

const (
	defaultRowLimit = 50
	maxRowLimit     = 500
)

// SQLiteSource implements DataSource over local files. It reads tenant config
// JSON via tenantfile and opens each tenant's SQLite file read-only.
type SQLiteSource struct {
	cfg      *config.Config
	tenants  *tenantfile.Client
	tenisDir string

	mu  sync.Mutex
	dbs map[string]*sql.DB // keyed by db_name
}

// NewSQLiteSource builds a source rooted at cfg.DataDir.
func NewSQLiteSource(cfg *config.Config) (*SQLiteSource, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data dir is not configured")
	}
	tc, err := tenantfile.New(cfg)
	if err != nil {
		return nil, err
	}
	return &SQLiteSource{
		cfg:      cfg,
		tenants:  tc,
		tenisDir: filepath.Join(cfg.DataDir, "tenants"),
		dbs:      map[string]*sql.DB{},
	}, nil
}

// loadTenant returns the tenant payload, its db_name, display name and parsed
// config schema. A missing tenant file is reported as a not-found error.
func (s *SQLiteSource) loadTenant(ctx context.Context, tenantID string) (dbName, display string, schema cfgSchema, err error) {
	payload, err := s.tenants.ReadTenant(ctx, tenantID)
	if err != nil {
		return "", "", cfgSchema{}, err
	}
	if payload == nil {
		return "", "", cfgSchema{}, fmt.Errorf("tenant %q not found", tenantID)
	}
	dbName, _ = payload["db_name"].(string)
	if dbName == "" {
		return "", "", cfgSchema{}, fmt.Errorf("tenant %q has no db_name", tenantID)
	}
	display, _ = payload["display_name"].(string)
	schema = parseCfgSchema(payload["schema"])
	return dbName, display, schema, nil
}

// --- DataSource: ListTenants ----------------------------------------------

func (s *SQLiteSource) ListTenants(ctx context.Context) ([]TenantSummary, error) {
	entries, err := os.ReadDir(s.tenisDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TenantSummary{}, nil
		}
		return nil, fmt.Errorf("read tenants dir: %w", err)
	}

	out := []TenantSummary{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		dbName, display, schema, lErr := s.loadTenant(ctx, id)
		if lErr != nil {
			continue
		}
		if display == "" {
			display = id
		}
		out = append(out, TenantSummary{
			ID:          id,
			DisplayName: display,
			DBName:      dbName,
			TableCount:  len(schema.Tables),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

// --- DataSource: GetSchema ------------------------------------------------

func (s *SQLiteSource) GetSchema(ctx context.Context, tenantID string) (TenantSchema, error) {
	dbName, _, cfg, err := s.loadTenant(ctx, tenantID)
	if err != nil {
		return TenantSchema{}, err
	}
	db, err := s.openDB(dbName)
	if err != nil {
		return TenantSchema{}, err
	}

	tableNames, err := s.listTables(ctx, db)
	if err != nil {
		return TenantSchema{}, err
	}

	readable := readableLookup(cfg)

	tables := make([]Table, 0, len(tableNames))
	for _, t := range tableNames {
		cols, err := s.tableColumns(ctx, db, t)
		if err != nil {
			return TenantSchema{}, err
		}
		for i := range cols {
			if r, ok := readable[t+"."+cols[i].Name]; ok {
				cols[i].Readable = r
			}
		}
		tables = append(tables, Table{Name: t, Columns: cols})
	}

	return TenantSchema{Tables: tables, Relations: relationsFrom(cfg)}, nil
}

// --- DataSource: QueryRows ------------------------------------------------

func (s *SQLiteSource) QueryRows(ctx context.Context, tenantID, table string, opts QueryOptions) (RowsPage, error) {
	dbName, _, _, err := s.loadTenant(ctx, tenantID)
	if err != nil {
		return RowsPage{}, err
	}
	db, err := s.openDB(dbName)
	if err != nil {
		return RowsPage{}, err
	}

	tableNames, err := s.listTables(ctx, db)
	if err != nil {
		return RowsPage{}, err
	}
	if err := assertInSet(table, toSet(tableNames)); err != nil {
		return RowsPage{}, err
	}
	cols, err := s.tableColumns(ctx, db, table)
	if err != nil {
		return RowsPage{}, err
	}
	colSet := map[string]struct{}{}
	for _, c := range cols {
		colSet[c.Name] = struct{}{}
	}

	qTable, err := quoteIdent(table)
	if err != nil {
		return RowsPage{}, err
	}

	where, args, err := buildWhere(opts.Filters, colSet)
	if err != nil {
		return RowsPage{}, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultRowLimit
	}
	if limit > maxRowLimit {
		limit = maxRowLimit
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM " + qTable + where
	if err := db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return RowsPage{}, fmt.Errorf("count: %w", err)
	}

	orderBy := ""
	if opts.OrderBy != "" {
		if err := assertInSet(opts.OrderBy, colSet); err != nil {
			return RowsPage{}, err
		}
		qCol, qErr := quoteIdent(opts.OrderBy)
		if qErr != nil {
			return RowsPage{}, qErr
		}
		dir := "ASC"
		if opts.Desc {
			dir = "DESC"
		}
		orderBy = " ORDER BY " + qCol + " " + dir
	}

	rowSQL := "SELECT * FROM " + qTable + where + orderBy + " LIMIT ? OFFSET ?"
	rowArgs := append(append([]any{}, args...), limit, offset)

	rows, err := db.QueryContext(ctx, rowSQL, rowArgs...)
	if err != nil {
		return RowsPage{}, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return RowsPage{}, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return RowsPage{}, err
		}
		m := make(map[string]any, len(colNames))
		for i, c := range colNames {
			m[c] = normalizeValue(vals[i])
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return RowsPage{}, err
	}

	return RowsPage{
		Columns: colNames,
		Rows:    out,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

func buildWhere(filters []Filter, colSet map[string]struct{}) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}
	clauses := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))
	for _, f := range filters {
		if err := assertInSet(f.Column, colSet); err != nil {
			return "", nil, err
		}
		op, ok := sqlOperator(f.Op)
		if !ok {
			return "", nil, fmt.Errorf("unsupported filter operator %q", f.Op)
		}
		qCol, err := quoteIdent(f.Column)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, qCol+" "+op+" ?")
		if f.Op == "like" {
			args = append(args, "%"+f.Value+"%")
		} else {
			args = append(args, f.Value)
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func normalizeValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// --- SQLite plumbing ------------------------------------------------------

func (s *SQLiteSource) openDB(dbName string) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[dbName]; ok {
		return db, nil
	}

	path := filepath.Join(s.cfg.DataDir, dbName+".db")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("database file not found for tenant (run: supacraft apply): %s", path)
		}
		return nil, err
	}

	// query_only enforces read-only at the SQLite level on every connection;
	// modernc applies _pragma params per-connection.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(2)
	s.dbs[dbName] = db
	return db, nil
}

func (s *SQLiteSource) listTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLiteSource) tableColumns(ctx context.Context, db *sql.DB, table string) ([]Column, error) {
	qTable, err := quoteIdent(table)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+qTable+")")
	if err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()

	var cols []Column
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, Column{
			Name:     name,
			Type:     ctype,
			Nullable: notnull == 0,
			PK:       pk > 0,
		})
	}
	return cols, rows.Err()
}

func toSet(items []string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, it := range items {
		m[it] = struct{}{}
	}
	return m
}

// Close closes all cached tenant DB handles.
func (s *SQLiteSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, db := range s.dbs {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.dbs = map[string]*sql.DB{}
	return firstErr
}
