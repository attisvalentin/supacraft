package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// BuildWhereRaw builds a parameterized WHERE clause without schema validation.
// Used when bypass_schema_flags=true. Column names are still double-quoted for SQL safety.
// Supports the same operators as query.BuildWhere: $eq, $ne, $gt, $gte, $lt, $lte, $like, $in, $null.
//
// SQLite uses positional `?` placeholders; paramOffset is unused and kept
// for call-site compatibility.
func BuildWhereRaw(where map[string]any, paramOffset int) (string, []any, error) {
	if len(where) == 0 {
		return "", nil, nil
	}
	_ = paramOffset

	var conditions []string
	var args []any

	for colName, value := range where {
		quotedCol := fmt.Sprintf("%q", colName)

		opMap, isOp := value.(map[string]any)
		if !isOp {
			conditions = append(conditions, fmt.Sprintf("%s = ?", quotedCol))
			args = append(args, marshalTimeArg(value))
			continue
		}

		for op, opVal := range opMap {
			switch op {
			case "$eq":
				conditions = append(conditions, fmt.Sprintf("%s = ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$ne":
				conditions = append(conditions, fmt.Sprintf("%s != ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$gt":
				conditions = append(conditions, fmt.Sprintf("%s > ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$gte":
				conditions = append(conditions, fmt.Sprintf("%s >= ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$lt":
				conditions = append(conditions, fmt.Sprintf("%s < ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$lte":
				conditions = append(conditions, fmt.Sprintf("%s <= ?", quotedCol))
				args = append(args, marshalTimeArg(opVal))
			case "$like":
				conditions = append(conditions, fmt.Sprintf("%s LIKE ? COLLATE NOCASE", quotedCol))
				args = append(args, marshalTimeArg(opVal))
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
					args = append(args, marshalTimeArg(v))
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

	return strings.Join(conditions, " AND "), args, nil
}

// marshalTimeArg converts a non-UTC time.Time to a SQLite-friendly wall-clock string.
// SQLite stores datetimes as TEXT; comparing UTC time.Time vs stored local-time strings
// fails, so we render the local wall-clock without offset. UTC times pass through.
func marshalTimeArg(v any) any {
	t, ok := v.(time.Time)
	if !ok {
		return v
	}
	if t.Location() == time.UTC {
		return t.Format("2006-01-02 15:04:05.999999999")
	}
	return t.Format("2006-01-02 15:04:05.999999999")
}
