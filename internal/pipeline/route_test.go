package pipeline

import (
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestMatchFunction(t *testing.T) {
	fns := map[string]tenant.Function{
		"by_id":    {Route: tenant.FunctionRoute{Path: "/fn/files/{name}", Method: "GET"}},
		"comments": {Route: tenant.FunctionRoute{Path: "/fn/posts/{id}/comments/{cid}", Method: "GET"}},
		"latest":   {Route: tenant.FunctionRoute{Path: "/fn/files/latest", Method: "GET"}},
		"upload":   {Route: tenant.FunctionRoute{Path: "/fn/files/{name}", Method: "POST"}},
	}
	cases := []struct {
		method, path, id string
		params           map[string]string
	}{
		{"GET", "/fn/files/latest", "latest", nil},
		{"GET", "/fn/files/a%2Fb.txt", "by_id", map[string]string{"name": "a/b.txt"}},
		{"POST", "/fn/files/x", "upload", map[string]string{"name": "x"}},
		{"GET", "/fn/posts/7/comments/9", "comments", map[string]string{"id": "7", "cid": "9"}},
		{"GET", "/fn/files", "", nil},
		{"GET", "/fn/files/", "", nil},
		{"DELETE", "/fn/files/x", "", nil},
	}
	for _, c := range cases {
		id, _, params := MatchFunction(fns, c.method, c.path)
		if id != c.id {
			t.Errorf("%s %s: matched %q, want %q", c.method, c.path, id, c.id)
			continue
		}
		for k, v := range c.params {
			if params[k] != v {
				t.Errorf("%s %s: param %s = %q, want %q", c.method, c.path, k, params[k], v)
			}
		}
	}
}

func TestValidateInput_Query(t *testing.T) {
	min := 1.0
	spec := map[string]tenant.InputField{
		"page":   {Type: "integer", Min: &min, Default: float64(1)},
		"active": {Type: "boolean"},
		"sort":   {Type: "string", Enum: []any{"new", "top"}},
	}
	clean, errs := ValidateInput(spec, map[string]any{"active": "true", "sort": "top", "_t": "123"}, true)
	if errs != nil || clean["page"] != float64(1) || clean["active"] != true || clean["_t"] != nil {
		t.Fatalf("clean=%v errs=%v", clean, errs)
	}
	if _, errs := ValidateInput(spec, map[string]any{"sort": "old", "page": "0"}, true); errs["sort"] == "" || errs["page"] == "" {
		t.Fatalf("expected sort and page errors, got %v", errs)
	}
}

func TestRouteShape_IgnoresSlashes(t *testing.T) {
	if RouteShape("/fn/a/{id}/") != RouteShape("/fn/a/{slug}") {
		t.Fatal("trailing slash changes the route shape")
	}
}
