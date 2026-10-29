package studio

import (
	"fmt"
	"strings"
)

// quoteIdent double-quotes a SQL identifier for safe embedding. Identifiers
// always originate from live DB introspection (sqlite_master / PRAGMA), but we
// reject anything containing a quote or NUL as defense-in-depth before quoting.
func quoteIdent(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\"\x00") {
		return "", fmt.Errorf("invalid identifier %q", name)
	}
	return `"` + name + `"`, nil
}

// assertInSet returns an error unless name is a member of allowed. This is the
// allowlist guard: table/column names in a request must match objects the
// introspection step actually found in the database.
func assertInSet(name string, allowed map[string]struct{}) error {
	if _, ok := allowed[name]; !ok {
		return fmt.Errorf("unknown table or column %q", name)
	}
	return nil
}

// sqlOperator maps a filter op token to its SQL operator. The bool is false for
// unknown ops so callers can reject the request.
func sqlOperator(op string) (string, bool) {
	switch op {
	case "eq":
		return "=", true
	case "neq":
		return "!=", true
	case "gt":
		return ">", true
	case "gte":
		return ">=", true
	case "lt":
		return "<", true
	case "lte":
		return "<=", true
	case "like":
		return "LIKE", true
	default:
		return "", false
	}
}
