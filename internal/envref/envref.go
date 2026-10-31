// Package envref resolves "${NAME}" references: config strings that are
// entirely a reference to an environment variable or server-side secret.
package envref

import (
	"regexp"
	"sort"
)

// Ref matches a string that is entirely a reference: "${NAME}".
var Ref = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// NameRe is a valid variable name.
var NameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Resolve replaces every reference in v (in place, recursively) with
// lookup(name). Unknown names become "" and are added to missing (may be nil).
func Resolve(v any, lookup func(string) (string, bool), missing map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = Resolve(child, lookup, missing)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = Resolve(child, lookup, missing)
		}
		return t
	case string:
		m := Ref.FindStringSubmatch(t)
		if m == nil {
			return t
		}
		if val, ok := lookup(m[1]); ok {
			return val
		}
		if missing != nil {
			missing[m[1]] = struct{}{}
		}
		return ""
	default:
		return v
	}
}

// Names returns the sorted, unique names referenced anywhere in v.
func Names(v any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		case string:
			if m := Ref.FindStringSubmatch(t); m != nil {
				seen[m[1]] = true
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
