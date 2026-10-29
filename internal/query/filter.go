package query

import (
	"fmt"
	"strings"

	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// BuildWhere validates columns against the schema and returns parameterized SQLite SQL.
// All placeholders use `?`; the caller appends args in the order returned.
// paramOffset is accepted for API compatibility with the multi-segment builders
// (cursor / row_policy / pagination append more args after) but is unused — SQLite
// uses positional placeholders, so only args order matters.
func BuildWhere(where map[string]any, table *tenant.SchemaTable, paramOffset int) (string, []any, error) {
	if len(where) == 0 {
		return "", nil, nil
	}

	var conditions []string
	var args []any

	for colName, value := range where {
		col, err := schema.FindColumn(table, colName)
		if err != nil {
			return "", nil, fmt.Errorf("invalid filter column: %s", colName)
		}
		if !col.IsReadable() {
			return "", nil, fmt.Errorf("cannot filter on non-readable column: %s", colName)
		}

		quotedCol := fmt.Sprintf("%q", colName)

		opMap, isOp := value.(map[string]any)
		if !isOp {
			conditions = append(conditions, fmt.Sprintf("%s = ?", quotedCol))
			args = append(args, value)
			continue
		}

		for op, opVal := range opMap {
			switch op {
			case "$eq":
				conditions = append(conditions, fmt.Sprintf("%s = ?", quotedCol))
				args = append(args, opVal)
			case "$ne":
				conditions = append(conditions, fmt.Sprintf("%s != ?", quotedCol))
				args = append(args, opVal)
			case "$gt":
				conditions = append(conditions, fmt.Sprintf("%s > ?", quotedCol))
				args = append(args, opVal)
			case "$gte":
				conditions = append(conditions, fmt.Sprintf("%s >= ?", quotedCol))
				args = append(args, opVal)
			case "$lt":
				conditions = append(conditions, fmt.Sprintf("%s < ?", quotedCol))
				args = append(args, opVal)
			case "$lte":
				conditions = append(conditions, fmt.Sprintf("%s <= ?", quotedCol))
				args = append(args, opVal)
			case "$like":
				// SQLite LIKE is case-insensitive for ASCII by default; COLLATE NOCASE
				// makes that explicit and consistent with Postgres ILIKE.
				conditions = append(conditions, fmt.Sprintf("%s LIKE ? COLLATE NOCASE", quotedCol))
				args = append(args, opVal)
			case "$in":
				arr, ok := opVal.([]any)
				if !ok {
					return "", nil, fmt.Errorf("$in value must be an array for column: %s", colName)
				}
				if len(arr) == 0 {
					return "", nil, fmt.Errorf("$in array cannot be empty for column: %s", colName)
				}
				placeholders := make([]string, len(arr))
				for i, v := range arr {
					placeholders[i] = "?"
					args = append(args, v)
				}
				conditions = append(conditions, fmt.Sprintf("%s IN (%s)", quotedCol, strings.Join(placeholders, ", ")))
			case "$null":
				isNull, ok := opVal.(bool)
				if !ok {
					return "", nil, fmt.Errorf("$null value must be a boolean for column: %s", colName)
				}
				if isNull {
					conditions = append(conditions, fmt.Sprintf("%s IS NULL", quotedCol))
				} else {
					conditions = append(conditions, fmt.Sprintf("%s IS NOT NULL", quotedCol))
				}
			default:
				return "", nil, fmt.Errorf("unknown operator: %s", op)
			}
		}
	}

	_ = paramOffset
	return strings.Join(conditions, " AND "), args, nil
}
