package query

import (
	"time"
)

// NormalizeRow converts SQLite-driver raw types to JSON-friendly values.
// time.Time values are converted to loc (defaults to UTC when nil).
// modernc.org/sqlite returns datetimes as time.Time directly when columns are
// declared TIMESTAMP/DATETIME, and as strings otherwise — those cases are handled
// by the row scanner in scan.go before reaching here.
func NormalizeRow(row map[string]any, loc *time.Location) {
	if loc == nil {
		loc = time.UTC
	}
	for k, v := range row {
		switch val := v.(type) {
		case time.Time:
			row[k] = val.In(loc)
		}
	}
}
