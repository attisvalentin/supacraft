package pipeline

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

var routeParamRe = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// RouteParams returns the {name} parameters of a route path in order, or an
// error for a malformed segment (e.g. "{id" or "x{id}").
func RouteParams(path string) ([]string, error) {
	var params []string
	seen := map[string]bool{}
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if !strings.ContainsAny(seg, "{}") {
			continue
		}
		m := routeParamRe.FindStringSubmatch(seg)
		if m == nil {
			return nil, fmt.Errorf("invalid path segment %q: a parameter must be a whole segment like {id}", seg)
		}
		if seen[m[1]] {
			return nil, fmt.Errorf("duplicate path parameter %q", m[1])
		}
		seen[m[1]] = true
		params = append(params, m[1])
	}
	return params, nil
}

// RouteShape replaces every {param} segment with {} so that routes differing
// only in parameter names compare equal.
func RouteShape(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		if routeParamRe.MatchString(s) {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// MatchFunction finds the function serving method + escapedPath (the URL's
// escaped path, so an encoded "/" stays inside one parameter). A literal
// segment beats a parameter at the first position where candidates differ, so
// /fn/posts/latest wins over /fn/posts/{id}.
func MatchFunction(functions map[string]tenant.Function, method, escapedPath string) (id string, fn *tenant.Function, params map[string]string) {
	reqSegs := strings.Split(strings.Trim(escapedPath, "/"), "/")
	var bestRank []bool
	for fnID, f := range functions {
		if !strings.EqualFold(f.Route.Method, method) {
			continue
		}
		p, rank, ok := matchSegments(strings.Split(strings.Trim(f.Route.Path, "/"), "/"), reqSegs)
		if !ok {
			continue
		}
		if fn == nil || betterRank(rank, bestRank) || (!betterRank(bestRank, rank) && fnID < id) {
			f := f
			id, fn, params, bestRank = fnID, &f, p, rank
		}
	}
	return id, fn, params
}

// matchSegments returns the decoded parameters and, per segment, whether it
// matched literally.
func matchSegments(route, req []string) (map[string]string, []bool, bool) {
	if len(route) != len(req) {
		return nil, nil, false
	}
	params := map[string]string{}
	rank := make([]bool, len(route))
	for i, seg := range route {
		if m := routeParamRe.FindStringSubmatch(seg); m != nil {
			v, err := url.PathUnescape(req[i])
			if err != nil || v == "" {
				return nil, nil, false
			}
			params[m[1]] = v
			continue
		}
		unescaped, err := url.PathUnescape(req[i])
		if err != nil || unescaped != seg {
			return nil, nil, false
		}
		rank[i] = true
	}
	return params, rank, true
}

func betterRank(a, b []bool) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i]
		}
	}
	return false
}
