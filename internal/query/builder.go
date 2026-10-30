package query

import (
	"fmt"
	"strings"

	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

func BuildSelect(tableName string, columns []string, whereSql string, whereArgs []any, orderBySql string, paginationSql string, paginationArgs []any) (string, []any) {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = QuoteIdent(c)
	}

	sql := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoted, ", "), QuoteIdent(tableName))
	args := whereArgs

	if whereSql != "" {
		sql += " WHERE " + whereSql
	}

	sql += orderBySql
	sql += paginationSql
	args = append(args, paginationArgs...)

	return sql, args
}

func BuildCount(tableName string, whereSql string, whereArgs []any) (string, []any) {
	sql := fmt.Sprintf("SELECT COUNT(*) FROM %s", QuoteIdent(tableName))
	if whereSql != "" {
		sql += " WHERE " + whereSql
	}
	return sql, whereArgs
}

// BuildInsert returns SQL and args for an INSERT, plus the deterministically-ordered
// column list. SQLite has no RETURNING * across all versions; callers should look up
// the inserted row via last_insert_rowid() (see ReadInsertedRow in helpers).
//
// Column order is alphabetical so the generated SQL is stable for tests/logs.
func BuildInsert(tableName string, data map[string]any) (string, []any) {
	cols := sortedKeys(data)
	quoted := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, col := range cols {
		quoted[i] = QuoteIdent(col)
		placeholders[i] = "?"
		args[i] = data[col]
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", QuoteIdent(tableName), strings.Join(quoted, ", "), strings.Join(placeholders, ", "))
	return sql, args
}

// BuildUpdate returns SQL and args for an UPDATE. SQLite does not support RETURNING
// portably here, so callers fetch updated rows separately via the same WHERE.
//
// Args order: SET values first (textual order: alphabetical column order), then WHERE values.
func BuildUpdate(tableName string, data map[string]any, whereSql string, whereArgs []any) (string, []any) {
	cols := sortedKeys(data)
	setClauses := make([]string, len(cols))
	setArgs := make([]any, len(cols))
	for i, col := range cols {
		setClauses[i] = fmt.Sprintf("%s = ?", QuoteIdent(col))
		setArgs[i] = data[col]
	}

	sql := fmt.Sprintf("UPDATE %s SET %s", QuoteIdent(tableName), strings.Join(setClauses, ", "))
	if whereSql != "" {
		sql += " WHERE " + whereSql
	}

	args := append(setArgs, whereArgs...)
	return sql, args
}

func BuildDelete(tableName string, whereSql string, whereArgs []any) (string, []any) {
	sql := fmt.Sprintf("DELETE FROM %s", QuoteIdent(tableName))
	if whereSql != "" {
		sql += " WHERE " + whereSql
	}
	return sql, whereArgs
}

func BuildOrderBy(orderBy map[string]string, table *tenant.SchemaTable) (string, error) {
	if len(orderBy) == 0 {
		return "", nil
	}

	var clauses []string
	for colName, direction := range orderBy {
		col, err := schema.FindColumn(table, colName)
		if err != nil {
			return "", fmt.Errorf("invalid order_by column: %s", colName)
		}
		if !col.IsReadable() {
			return "", fmt.Errorf("cannot order by non-readable column: %s", colName)
		}

		dir := strings.ToUpper(direction)
		if dir != "ASC" && dir != "DESC" {
			return "", fmt.Errorf("invalid order direction: %s (must be asc or desc)", direction)
		}

		clauses = append(clauses, fmt.Sprintf("%s %s", QuoteIdent(colName), dir))
	}

	return " ORDER BY " + strings.Join(clauses, ", "), nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// stdlib sort
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
