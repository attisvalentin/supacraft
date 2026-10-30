// Package ddl is SupaCraft's SQLite DDL engine. It deliberately only supports
//
//  1. CREATE TABLE IF NOT EXISTS for new tables.
//  2. ALTER TABLE ADD COLUMN for new columns on existing tables.
//
// Column drops, renames, and type changes are not handled — they would
// require SQLite's "table rebuild" dance (rename → create new → INSERT … SELECT
// → drop old) which is non-trivial when foreign keys or triggers exist. Users
// running into a destructive schema change can delete the SQLite file and
// re-apply.
package ddl

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/sqlitedb"
)

// Column represents a schema column parsed from the config JSON.
type Column struct {
	UUID     string
	Name     string
	Type     string
	Params   map[string]any
	Nullable bool
	Unique   bool
	Primary  bool
	Default  *string
}

// Table represents a schema table parsed from the config JSON.
type Table struct {
	UUID    string
	Name    string
	Columns []Column
}

// Operation is a single DDL operation produced by PlanMigration. Only Desc
// and the raw SQL matter to the executor.
type Operation struct {
	Desc string
	SQL  string
}

// Executor runs DDL on tenant SQLite databases.
type Executor struct {
	prov *sqlitedb.Provisioner
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{prov: sqlitedb.New(cfg)}
}

// ConnectTenant returns a *sql.DB for the named tenant — used by PlanMigration
// to introspect existing tables. Caller is responsible for closing.
func (e *Executor) ConnectTenant(ctx context.Context, dbName string) (*sql.DB, error) {
	return e.prov.OpenTenant(ctx, dbName)
}

// Execute runs a list of raw SQL statements in a single transaction.
func (e *Executor) Execute(ctx context.Context, dbName string, stmts []string) error {
	if len(stmts) == 0 {
		return nil
	}
	db, err := e.prov.OpenTenant(ctx, dbName)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec %q: %w", s, err)
		}
	}
	return tx.Commit()
}

// ExecuteOperations runs a list of Operations (just the SQL field) inside one
// transaction.
func (e *Executor) ExecuteOperations(ctx context.Context, dbName string, ops []Operation) error {
	stmts := make([]string, len(ops))
	for i, op := range ops {
		stmts[i] = op.SQL
	}
	return e.Execute(ctx, dbName, stmts)
}

// ParseSchema converts the config schema JSON shape into our Table list.
// The schema is expected as map[string]any with keys "tables" → []any.
func ParseSchema(raw any) ([]Table, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema is not an object")
	}
	rawTables, _ := m["tables"].([]any)
	out := make([]Table, 0, len(rawTables))
	for _, rt := range rawTables {
		tm, ok := rt.(map[string]any)
		if !ok {
			continue
		}
		t := Table{
			UUID: asString(tm["uuid"]),
			Name: asString(tm["name"]),
		}
		rawCols, _ := tm["columns"].([]any)
		for _, rc := range rawCols {
			cm, ok := rc.(map[string]any)
			if !ok {
				continue
			}
			col := Column{
				UUID:    asString(cm["uuid"]),
				Name:    asString(cm["name"]),
				Type:    strings.ToLower(asString(cm["type"])),
				Params:  asMap(cm["params"]),
				Primary: asBool(cm["primary"]),
				Unique:  asBool(cm["unique"]),
			}
			// Default nullable to true unless explicitly false.
			if v, ok := cm["nullable"]; ok {
				col.Nullable = asBool(v)
			} else {
				col.Nullable = true
			}
			// The column default lives under "default_value"; tolerate the
			// legacy "default" key too.
			if d, ok := cm["default_value"].(string); ok {
				col.Default = &d
			} else if d, ok := cm["default"].(string); ok {
				col.Default = &d
			}
			t.Columns = append(t.Columns, col)
		}
		out = append(out, t)
	}
	return out, nil
}

// PlanMigration computes the additive DDL operations needed to bring the
// tenant DB schema in line with newTables. It introspects the live DB
// (sqlite_master + pragma_table_info) and emits CREATE TABLE for missing
// tables and ALTER TABLE ADD COLUMN for columns that aren't already there.
//
// The live DB, not a previously applied config, is the source of truth:
// columns added by side channels — notably the auth migration in
// internal/sqlitedb/provision.go, which creates users(id, email, password, …)
// before this runs — must not be re-ADDed.
//
// Drops, renames, and type changes are intentionally ignored — see the
// package doc.
func PlanMigration(ctx context.Context, db *sql.DB, newTables []Table) ([]Operation, error) {
	existing, err := introspect(ctx, db)
	if err != nil {
		return nil, err
	}
	var ops []Operation
	for _, t := range newTables {
		if cols, ok := existing[t.Name]; ok {
			for _, col := range t.Columns {
				if _, found := cols[col.Name]; found {
					continue
				}
				ops = append(ops, Operation{
					Desc: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", t.Name, col.Name),
					SQL:  alterAddColumnSQL(t.Name, col),
				})
			}
			continue
		}
		ops = append(ops, Operation{
			Desc: "CREATE TABLE " + t.Name,
			SQL:  createTableSQL(t),
		})
	}
	return ops, nil
}

// introspect returns map[tableName] → set of column names from sqlite_master +
// pragma_table_info. Skips internal tables (sqlite_*, sqlite_sequence).
func introspect(ctx context.Context, db *sql.DB) (map[string]map[string]bool, error) {
	// Collect table names first and fully close the result set before issuing
	// the per-table PRAGMA queries. The tenant pool is capped at one connection
	// (SetMaxOpenConns(1)), so holding this rows set open while querying again
	// would deadlock waiting for a second connection that can never be acquired.
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := map[string]map[string]bool{}
	for _, name := range names {
		colRows, err := db.QueryContext(ctx, "PRAGMA table_info("+query.QuoteIdent(name)+")")
		if err != nil {
			return nil, err
		}
		colSet := map[string]bool{}
		for colRows.Next() {
			var cid int
			var cname, ctype string
			var notnull, pk int
			var dflt sql.NullString
			if err := colRows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk); err != nil {
				colRows.Close()
				return nil, err
			}
			colSet[cname] = true
		}
		colRows.Close()
		out[name] = colSet
	}
	return out, nil
}

// createTableSQL emits a CREATE TABLE IF NOT EXISTS statement.
func createTableSQL(t Table) string {
	var defs []string
	for _, c := range t.Columns {
		defs = append(defs, columnDef(c))
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)",
		query.QuoteIdent(t.Name),
		strings.Join(defs, ",\n  "),
	)
}

func alterAddColumnSQL(tableName string, c Column) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", query.QuoteIdent(tableName), columnDef(c))
}

// uuidV4Expr is a SQLite expression that generates an RFC-4122 v4 UUID. SQLite
// has no UUID generator, so primary-key columns get this as their DEFAULT, ensuring
// inserts that omit the id (CRUD create, pipeline db.insert) still receive one.
const uuidV4Expr = "lower(hex(randomblob(4))||'-'||hex(randomblob(2))||'-4'||" +
	"substr(hex(randomblob(2)),2)||'-'||substr('89ab',abs(random())%4+1,1)||" +
	"substr(hex(randomblob(2)),2)||'-'||hex(randomblob(6)))"

func columnDef(c Column) string {
	isPrimary := c.Primary || c.Type == "primary"
	parts := []string{fmt.Sprintf("%s %s", query.QuoteIdent(c.Name), sqliteType(c.Type))}
	if isPrimary {
		parts = append(parts, "PRIMARY KEY")
	}
	if !c.Nullable && !isPrimary {
		parts = append(parts, "NOT NULL")
	}
	if c.Unique && !isPrimary {
		parts = append(parts, "UNIQUE")
	}
	if def := sqliteDefault(c); def != "" {
		parts = append(parts, "DEFAULT "+def)
	} else if isPrimary {
		// Auto-generate the primary key when the row doesn't supply one.
		parts = append(parts, "DEFAULT ("+uuidV4Expr+")")
	}
	return strings.Join(parts, " ")
}

// sqliteDefault renders a column's default_value as a SQLite-valid DEFAULT
// clause. Configs may carry Postgres-shaped defaults (e.g. NOW(),
// gen_random_uuid(), bare string literals for enums); this translates them and
// quotes string literals. Returns "" when the column has no default.
func sqliteDefault(c Column) string {
	if c.Default == nil {
		return ""
	}
	v := strings.TrimSpace(*c.Default)
	if v == "" {
		return ""
	}
	// Translate common Postgres default expressions to SQLite equivalents.
	switch strings.ToUpper(strings.TrimSuffix(v, "()")) {
	case "NOW", "CURRENT_TIMESTAMP":
		return "CURRENT_TIMESTAMP"
	case "GEN_RANDOM_UUID", "UUID_GENERATE_V4":
		return "(" + uuidV4Expr + ")"
	}
	// Booleans → 1/0 regardless of the declared column type.
	switch strings.ToLower(v) {
	case "true":
		return "1"
	case "false":
		return "0"
	}
	// Numeric affinities take the literal as-is; everything else is a string
	// literal (enum, varchar, text, uuid, timestamp) and must be quoted.
	switch sqliteType(c.Type) {
	case "INTEGER", "REAL":
		return v
	}
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return v // already quoted
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// sqliteType maps the schema type names to SQLite type affinities.
// SQLite is loosely typed — anything works at the column level — but using
// affinity-correct names makes scanned values come back in the expected Go
// types (INTEGER → int64, REAL → float64, TEXT → string, BLOB → []byte).
func sqliteType(schemaType string) string {
	switch schemaType {
	case "int", "integer", "bigint", "smallint", "tinyint", "boolean":
		return "INTEGER"
	case "decimal", "numeric", "float", "real", "double":
		return "REAL"
	case "blob", "binary":
		return "BLOB"
	case "json", "jsonb":
		// SQLite has no JSON affinity but TEXT round-trips fine; the json1
		// extension is built into modernc.org/sqlite.
		return "TEXT"
	default:
		return "TEXT"
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
