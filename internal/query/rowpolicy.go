package query

import (
	"fmt"
	"regexp"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

var rowPolicyVar = regexp.MustCompile(`\$auth\.(id|email|role)\b`)

// BuildRowPolicy takes a row_policy expression (e.g. "user_id = $auth.id") and
// replaces each $auth.id, $auth.email and $auth.role with a `?` placeholder,
// returning the values in the order they appear. paramOffset is unused for
// SQLite (positional `?`) but kept in the signature for API parity.
func BuildRowPolicy(expression string, claims *tenant.AuthClaims, paramOffset int) (string, []any, error) {
	if expression == "" {
		return "", nil, nil
	}
	_ = paramOffset

	var args []any
	var missing string
	result := rowPolicyVar.ReplaceAllStringFunc(expression, func(v string) string {
		if claims == nil {
			if missing == "" {
				missing = v
			}
			return v
		}
		switch v {
		case "$auth.id":
			args = append(args, claims.UserID)
		case "$auth.email":
			args = append(args, claims.Email)
		case "$auth.role":
			args = append(args, claims.Role)
		}
		return "?"
	})
	if missing != "" {
		return "", nil, fmt.Errorf("row_policy references %s but no authenticated user", missing)
	}
	return result, args, nil
}
