package query

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ScanAllRowsToMaps reads every row from a *sql.Rows result set, returning
// []map[colname]value. Driver-returned []byte values that look like UTF-8 strings
// are converted to string. time.Time values pass through.
func ScanAllRowsToMaps(rows *sql.Rows, loc *time.Location) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("read columns: %w", err)
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("read column types: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = coerceValue(dest[i], colTypes[i])
		}
		NormalizeRow(row, loc)
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration: %w", err)
	}
	return results, nil
}

// coerceValue normalizes raw scan values into JSON-friendly Go types:
// []byte → string, ISO strings in datetime columns → time.Time.
func coerceValue(v any, ct *sql.ColumnType) any {
	switch x := v.(type) {
	case []byte:
		s := string(x)
		if isDateTimeType(ct) {
			if t, ok := parseSQLiteDateTime(s); ok {
				return t
			}
		}
		return s
	case string:
		if isDateTimeType(ct) {
			if t, ok := parseSQLiteDateTime(x); ok {
				return t
			}
		}
		return x
	}
	return v
}

func isDateTimeType(ct *sql.ColumnType) bool {
	if ct == nil {
		return false
	}
	name := strings.ToUpper(ct.DatabaseTypeName())
	return strings.Contains(name, "TIME") || strings.Contains(name, "DATE")
}

// parseSQLiteDateTime tries the common SQLite datetime layouts. SQLite stores
// datetimes as TEXT in ISO-8601 format by convention, but applications may write
// them with or without timezone info, with space or T separator, etc.
func parseSQLiteDateTime(s string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
