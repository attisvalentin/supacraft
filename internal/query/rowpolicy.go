package query

import (
	"fmt"
	"strings"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// BuildRowPolicy takes a row_policy expression (e.g. "user_id = $auth.id") and
// replaces $auth.* variables with `?` placeholders. paramOffset is unused for
// SQLite (positional `?`) but kept in the signature for API parity.
func BuildRowPolicy(expression string, claims *tenant.AuthClaims, paramOffset int) (string, []any, error) {
	if expression == "" {
		return "", nil, nil
	}
	_ = paramOffset

	var args []any
	result := expression

	if strings.Contains(result, "$auth.id") {
		if claims == nil {
			return "", nil, fmt.Errorf("row_policy references $auth.id but no authenticated user")
		}
		result = strings.ReplaceAll(result, "$auth.id", "?")
		args = append(args, claims.UserID)
	}

	if strings.Contains(result, "$auth.email") {
		if claims == nil {
			return "", nil, fmt.Errorf("row_policy references $auth.email but no authenticated user")
		}
		result = strings.ReplaceAll(result, "$auth.email", "?")
		args = append(args, claims.Email)
	}

	return result, args, nil
}
