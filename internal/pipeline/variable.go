package pipeline

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolveString resolves {{...}} references in a string against the pipeline context.
// If the entire string is a single {{path}} reference, returns the raw value (preserving type).
// If the string mixes literal text with {{...}}, returns a concatenated string.
func ResolveString(s string, ctx *PipelineContext) (any, error) {
	refs := findRefs(s)
	if len(refs) == 0 {
		return s, nil
	}

	// Single reference covering the whole string — return raw value
	if len(refs) == 1 && refs[0].start == 0 && refs[0].end == len(s) {
		val, err := resolveFromContext(refs[0].path, ctx)
		if err != nil {
			return nil, err
		}
		return val, nil
	}

	// Mixed text + references — build string
	var b strings.Builder
	cursor := 0
	for _, ref := range refs {
		b.WriteString(s[cursor:ref.start])
		val, err := resolveFromContext(ref.path, ctx)
		if err != nil {
			return nil, err
		}
		b.WriteString(fmt.Sprintf("%v", val))
		cursor = ref.end
	}
	b.WriteString(s[cursor:])
	return b.String(), nil
}

// ResolveMap resolves all {{...}} references in a map's values recursively.
func ResolveMap(m map[string]any, ctx *PipelineContext) (map[string]any, error) {
	result := make(map[string]any, len(m))
	for k, v := range m {
		resolved, err := ResolveValue(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		result[k] = resolved
	}
	return result, nil
}

// ResolveValue resolves {{...}} references in any value type.
func ResolveValue(v any, ctx *PipelineContext) (any, error) {
	switch val := v.(type) {
	case string:
		return ResolveString(val, ctx)
	case map[string]any:
		return ResolveMap(val, ctx)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			resolved, err := ResolveValue(item, ctx)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	default:
		return v, nil
	}
}

// ResolvePath resolves a dot-separated path against a root value.
// Handles map[string]any (key lookup), []any (numeric index or field collection),
// and []map[string]any (numeric index or field collection).
func ResolvePath(path string, root any) (any, error) {
	segments := strings.Split(path, ".")
	current := root

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if current == nil {
			return nil, fmt.Errorf("cannot resolve %q: nil value", path)
		}

		switch val := current.(type) {
		case map[string]any:
			v, ok := val[seg]
			if !ok {
				return nil, nil
			}
			current = v

		case map[string]string:
			// e.g. {{env.WORLD_CUP_API_TOKEN}} — api_settings.env is a string map
			v, ok := val[seg]
			if !ok {
				return nil, nil
			}
			current = v

		case []any:
			// Try numeric index first
			if idx, err := strconv.Atoi(seg); err == nil {
				if idx < 0 || idx >= len(val) {
					return nil, fmt.Errorf("index %d out of range (len=%d)", idx, len(val))
				}
				current = val[idx]
			} else {
				// Collect field from all items
				collected := make([]any, 0, len(val))
				for _, item := range val {
					if m, ok := item.(map[string]any); ok {
						if v, ok := m[seg]; ok {
							collected = append(collected, v)
						}
					}
				}
				current = collected
			}

		case []map[string]any:
			// Try numeric index first
			if idx, err := strconv.Atoi(seg); err == nil {
				if idx < 0 || idx >= len(val) {
					return nil, fmt.Errorf("index %d out of range (len=%d)", idx, len(val))
				}
				current = val[idx]
			} else {
				// Collect field from all items
				collected := make([]any, 0, len(val))
				for _, item := range val {
					if v, ok := item[seg]; ok {
						collected = append(collected, v)
					}
				}
				current = collected
			}

		default:
			return nil, fmt.Errorf("cannot traverse %q in value of type %T", seg, current)
		}
	}

	return current, nil
}

type ref struct {
	path  string
	start int
	end   int
}

// findRefs scans a string for {{...}} references and returns their positions and paths.
func findRefs(s string) []ref {
	var refs []ref
	i := 0
	for i < len(s)-3 {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			// Find closing }}
			j := strings.Index(s[i+2:], "}}")
			if j == -1 {
				break
			}
			path := strings.TrimSpace(s[i+2 : i+2+j])
			refs = append(refs, ref{
				path:  path,
				start: i,
				end:   i + 2 + j + 2,
			})
			i = i + 2 + j + 2
		} else {
			i++
		}
	}
	return refs
}

// resolveFromContext resolves a dot-path from the pipeline context.
// The first segment is the top-level context key; remaining segments traverse the value.
func resolveFromContext(path string, ctx *PipelineContext) (any, error) {
	parts := strings.SplitN(path, ".", 2)
	rootKey := parts[0]

	val, ok := ctx.Get(rootKey)
	if !ok {
		return nil, nil
	}

	if len(parts) == 1 {
		return val, nil
	}

	return ResolvePath(parts[1], val)
}
