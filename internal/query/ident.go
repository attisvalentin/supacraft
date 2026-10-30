package query

import "strings"

// QuoteIdent quotes a table or column name for SQLite. Embedded double quotes
// are doubled, so the result is always a single identifier. Never use Go's %q
// for this: its backslash escapes mean nothing to SQLite.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
