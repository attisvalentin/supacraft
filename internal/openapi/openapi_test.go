package openapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const shopProject = `{
  "name": "Shop",
  "auth_enabled": true,
  "schema": {"tables": [
    {"name": "products", "columns": [
      {"name": "id", "type": "uuid", "primary": true},
      {"name": "title", "type": "varchar", "nullable": false},
      {"name": "status", "type": "enum", "enum_values": ["draft", "live"]},
      {"name": "secret_note", "type": "text", "readable": false}]},
    {"name": "logs", "columns": [{"name": "id", "type": "uuid", "primary": true}]}
  ]},
  "api_settings": {
    "auth": {"roles": ["user", "admin"]},
    "crud_endpoints": {"delete": false},
    "tables": {
      "products": {"query": {"enabled": true}, "create": {"enabled": true, "roles": ["admin"]}, "delete": {"enabled": true}},
      "logs": {"query": {"enabled": false}}
    }
  },
  "functions": {
    "search": {"route": {"path": "/fn/search", "method": "GET", "input": {"q": {"type": "string", "required": true, "min": 2}}},
               "steps": [{"type": "response", "data": {}}]},
    "buy": {"route": {"path": "/fn/products/{id}/buy", "method": "POST", "roles": ["user"], "input": {"qty": {"type": "integer", "min": 1}}},
            "steps": [{"type": "response", "data": {}}]},
    "helper": {"route": {"path": "", "method": "PRIVATE"}, "steps": [{"type": "response", "data": {}}]}
  }
}`

func build(t *testing.T, opts Options) map[string]any {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(shopProject), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := p.TenantConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON, as a client sees it.
	data, err := json.Marshal(Build(cfg, opts))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func get(m any, keys ...string) any {
	for _, k := range keys {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[k]
	}
	return m
}

func TestBuild(t *testing.T) {
	doc := build(t, Options{})
	if doc["openapi"] != "3.1.0" || get(doc, "info", "title") != "Shop" {
		t.Fatalf("header: %v %v", doc["openapi"], doc["info"])
	}
	paths := doc["paths"].(map[string]any)

	for _, p := range []string{"/auth/register", "/auth/login", "/auth/refresh", "/query", "/create", "/fn/search", "/fn/products/{id}/buy"} {
		if paths[p] == nil {
			t.Errorf("missing %s", p)
		}
	}
	// Disabled endpoints, unconfigured flows, files without storage and
	// private functions are left out.
	for _, p := range []string{"/delete", "/update", "/auth/forgot-password", "/files", "/fn/helper", ""} {
		if paths[p] != nil {
			t.Errorf("unexpected %s", p)
		}
	}

	// Only enabled tables are in the CRUD request; non-readable columns
	// stay out of the row schema.
	mapping := get(paths, "/query", "post", "requestBody", "content", "application/json", "schema", "discriminator", "mapping").(map[string]any)
	if len(mapping) != 1 || mapping["products"] == nil {
		t.Errorf("query tables: %v", mapping)
	}
	row := get(doc, "components", "schemas", "products", "properties").(map[string]any)
	if row["secret_note"] != nil || row["title"] == nil {
		t.Errorf("row schema: %v", row)
	}
	if req := get(doc, "components", "schemas", "products.create", "required"); len(req.([]any)) != 1 || req.([]any)[0] != "title" {
		t.Errorf("create required: %v", req)
	}
	if get(paths, "/create", "post", "responses", "403") == nil {
		t.Error("/create has role-restricted tables but no 403")
	}

	// GET input is query parameters; POST input is the body; roles mean
	// a token and 403.
	search := get(paths, "/fn/search", "get").(map[string]any)
	if params := search["parameters"].([]any); len(params) != 1 || get(params[0], "in") != "query" || get(params[0], "required") != true {
		t.Errorf("search params: %v", search["parameters"])
	}
	if sec := search["security"].([]any); len(sec) != 0 {
		t.Errorf("public function has security %v", sec)
	}
	buy := get(paths, "/fn/products/{id}/buy", "post").(map[string]any)
	if get(buy, "parameters").([]any)[0].(map[string]any)["name"] != "id" {
		t.Errorf("path param: %v", buy["parameters"])
	}
	if get(buy, "requestBody", "content", "application/json", "schema", "properties", "qty", "minimum") != 1.0 {
		t.Errorf("body: %v", buy["requestBody"])
	}
	if get(buy, "responses", "403") == nil || len(buy["security"].([]any)) != 1 {
		t.Errorf("roles: %v %v", buy["security"], buy["responses"])
	}
}

func TestBuild_CentralTenantHeader(t *testing.T) {
	doc := build(t, Options{ServerURL: "https://api.example.com", TenantHeader: "shop"})
	if get(doc, "servers").([]any)[0].(map[string]any)["url"] != "https://api.example.com" {
		t.Errorf("servers: %v", doc["servers"])
	}
	for path, item := range doc["paths"].(map[string]any) {
		for method, op := range item.(map[string]any) {
			params, _ := get(op, "parameters").([]any)
			if len(params) == 0 || get(params[0], "name") != "X-Tenant-ID" {
				t.Errorf("%s %s has no X-Tenant-ID header", method, path)
			}
		}
	}
}

func TestColumnSchema(t *testing.T) {
	no := false
	cases := []struct {
		col  tenant.SchemaColumn
		want string
	}{
		{tenant.SchemaColumn{Type: "int", Nullable: &no}, `{"type":"integer"}`},
		{tenant.SchemaColumn{Type: "timestamptz"}, `{"format":"date-time","type":["string","null"]}`},
		{tenant.SchemaColumn{Type: "primary"}, `{"format":"uuid","type":"string"}`},
		{tenant.SchemaColumn{Type: "jsonb"}, `{}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(columnSchema(&c.col))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("%s: %s, want %s", c.col.Type, got, c.want)
		}
	}
}
