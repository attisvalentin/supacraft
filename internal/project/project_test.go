package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, cfg string) *Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const blog = `{
  "name": "My Blog",
  "auth_enabled": true,
  "schema": {"tables": [{"name": "posts", "columns": [
    {"name": "id", "type": "uuid", "primary": true},
    {"name": "title", "type": "varchar"},
    {"name": "author_id", "type": "uuid"}]}],
    "relations": [{"from": {"table": "posts", "column": "author_id"}, "to": {"table": "users", "column": "id"}, "type": "one-to-many"}]},
  "api_settings": {"env": {"STRIPE_KEY": "${STRIPE_KEY}"}, "tables": {"posts": {"query": {"enabled": true}}}},
  "functions": {
    "list": {"route": {"path": "/fn/posts", "method": "GET"}, "steps": [
      {"type": "db.select", "table": "posts", "assign": "posts"},
      {"type": "function.call", "function_id": "helper"},
      {"type": "response", "status_code": 200, "data": {"posts": "{{posts}}"}}]},
    "helper": {"route": {"method": "PRIVATE"}, "steps": [
      {"type": "transform.math", "expression": "ceil({{a}} / {{b}})", "assign": "x"}]}
  },
  "cronjobs": {"nightly": {"schedule": "0 3 * * *", "steps": [
    {"type": "db.delete", "table": "posts", "where": {"title": {"$null": true}}}]}}
}`

func TestValidate_ValidProject(t *testing.T) {
	p := load(t, blog)
	if issues := p.Validate(); HasErrors(issues) {
		t.Fatalf("unexpected errors: %v", issues)
	}
}

func TestValidate_ReportsPaths(t *testing.T) {
	p := load(t, `{
  "name": "x",
  "schema": {"tables": [{"name": "posts", "columns": [{"name": "title", "type": "strin"}]}]},
  "api_settings": {"tables": {"postz": {}}, "cors": {"allowed_origin": "https://a.com/app"}},
  "functions": {"f": {"route": {"path": "/posts", "method": "GET"}, "steps": [
    {"type": "db.insert", "table": "posts", "data": {"nope": 1}},
    {"type": "transform.math", "expression": "1 +", "assign": "x"},
    {"type": "email.send", "to": ["a@b.c"], "mode": "template", "template_id": "welcome"},
    {"type": "magic"}]}},
  "cronjobs": {"c": {"schedule": "every day", "steps": [{"type": "set_variable"}]}}
}`)
	want := []string{
		"schema.tables[0].columns[0].type",
		"api_settings.tables.postz",
		"api_settings.cors.allowed_origin",
		"functions.f.route.path",
		"functions.f.steps[0].data.nope",
		"functions.f.steps[1].expression",
		"functions.f.steps[2].template_id",
		"functions.f.steps[3].type",
		"cronjobs.c.schedule",
		"cronjobs.c.steps[0].assign",
	}
	got := map[string]bool{}
	for _, i := range p.Validate() {
		if i.Severity == "error" {
			got[i.Path] = true
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing error at %s (got %v)", w, got)
		}
	}
}

func TestCompile_NamesAsIDsAuthAndEnv(t *testing.T) {
	p := load(t, blog)
	os.WriteFile(filepath.Join(p.Dir, ".env"), []byte("STRIPE_KEY=sk_test_123\n"), 0o600)

	c, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MissingEnv) != 0 {
		t.Errorf("missing env: %v", c.MissingEnv)
	}
	if c.Payload["db_name"] != "my_blog" {
		t.Errorf("db_name = %v", c.Payload["db_name"])
	}
	data, _ := json.Marshal(c.Payload)
	s := string(data)
	for _, want := range []string{`"uuid":"posts"`, `"uuid":"title"`, `"name":"users"`, `"uuid":"password"`, `"STRIPE_KEY":"sk_test_123"`, `"name":"helper"`, `"enabled":true`} {
		if !strings.Contains(s, want) {
			t.Errorf("compiled payload lacks %s", want)
		}
	}
	// Compiling must not touch the source config.
	if strings.Contains(mustJSON(p.Raw), "sk_test_123") {
		t.Error("secret leaked into Raw")
	}
}

func TestCompile_ReportsMissingEnv(t *testing.T) {
	p := load(t, blog)
	t.Setenv("STRIPE_KEY", "")
	os.Unsetenv("STRIPE_KEY")
	c, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MissingEnv) != 1 || c.MissingEnv[0] != "STRIPE_KEY" {
		t.Errorf("MissingEnv = %v", c.MissingEnv)
	}
}

func TestApply_MigratesAdditively(t *testing.T) {
	p := load(t, blog)
	ctx := context.Background()
	res, err := p.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || len(res.Migrations) == 0 {
		t.Fatalf("first apply: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(p.DataDir(), "tenants", "my_blog.json")); err != nil {
		t.Fatal(err)
	}

	tables := p.Raw["schema"].(map[string]any)["tables"].([]any)
	posts := tables[0].(map[string]any)
	posts["columns"] = append(posts["columns"].([]any), map[string]any{"name": "slug", "type": "varchar"})
	res, err = p.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Migrations) != 1 || !strings.Contains(res.Migrations[0], "slug") {
		t.Fatalf("second apply migrations = %v", res.Migrations)
	}
}

func TestApply_RefusesInvalid(t *testing.T) {
	p := load(t, `{"name": "x", "functions": {"f": {"route": {"path": "bad", "method": "GET"}, "steps": []}}}`)
	_, err := p.Apply(context.Background())
	var verr *ValidationError
	if err == nil || !asValidation(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if _, statErr := os.Stat(p.DataDir()); statErr == nil {
		t.Error("invalid apply created state")
	}
}

func TestSaveKeepsNameFirst(t *testing.T) {
	p := New(t.TempDir(), "demo", true)
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(p.Dir, FileName))
	if !strings.HasPrefix(string(data), "{\n  \"name\": \"demo\"") {
		t.Errorf("unexpected layout:\n%s", data)
	}
	if _, err := Load(p.Dir); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func asValidation(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

func TestValidate_SecurityLimits(t *testing.T) {
	long := strings.Repeat("a", MaxTenantIDLength+1)
	p := load(t, `{"name": "`+long+`", "api_settings": {"auth": {"password_min_length": 100}}}`)
	got := map[string]bool{}
	for _, i := range p.Validate() {
		if i.Severity == "error" {
			got[i.Path] = true
		}
	}
	for _, want := range []string{"name", "api_settings.auth.password_min_length"} {
		if !got[want] {
			t.Errorf("missing error at %s (got %v)", want, got)
		}
	}
}

func TestValidate_WarnsAboutPrivilegedUsersColumns(t *testing.T) {
	p := load(t, `{"name": "x", "auth_enabled": true, "schema": {"tables": [{"name": "users", "columns": [
    {"name": "role", "type": "varchar", "creatable": false}]}]}}`)
	var warned bool
	for _, i := range p.Validate() {
		if i.Severity == "warning" && i.Path == "schema.tables[0].columns[0].updatable" {
			warned = true
		}
	}
	if !warned {
		t.Error("no warning for updatable users.role")
	}
}

func TestCompile_BuiltinAuthColumnsWin(t *testing.T) {
	p := load(t, `{"name": "x", "auth_enabled": true, "schema": {"tables": [{"name": "users", "columns": [
    {"name": "is_active", "type": "boolean"},
    {"name": "password", "type": "varchar"},
    {"name": "nickname", "type": "varchar"}]}]}}`)
	c, err := p.Compile()
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]map[string]any{}
	for _, t := range c.Payload["schema"].(map[string]any)["tables"].([]any) {
		tm := t.(map[string]any)
		if tm["name"] != "users" {
			continue
		}
		for _, col := range tm["columns"].([]any) {
			cm := col.(map[string]any)
			cols[cm["name"].(string)] = cm
		}
	}
	if cols["is_active"]["creatable"] != false || cols["password"]["readable"] != false {
		t.Errorf("declared auth columns overrode built-in flags: %v / %v", cols["is_active"], cols["password"])
	}
	if cols["nickname"] == nil {
		t.Error("custom users column dropped")
	}
}

func TestValidate_PipelineCore(t *testing.T) {
	p := load(t, `{
  "name": "x",
  "schema": {
    "tables": [
      {"name": "posts", "columns": [{"name": "id", "type": "uuid", "primary": true}, {"name": "title", "type": "text"}]},
      {"name": "tags", "columns": [{"name": "id", "type": "uuid", "primary": true}]}]
  },
  "functions": {
    "a": {"route": {"path": "/fn/posts/{id}", "method": "GET",
      "input": {"q": {"type": "strng"}, "n": {"type": "integer", "min": 5, "max": 1}, "s": {"type": "string", "pattern": "("}}},
      "steps": [
        {"type": "db.select", "table": "posts", "order_by": ["nope desc", "title sideways"], "limit": -1, "include": [{"table": "tags"}]},
        {"type": "db.aggregate", "table": "posts", "aggregates": {"n": "median:title"}},
        {"type": "transaction", "steps": [{"type": "http.request", "url": "https://x.y"}, {"type": "response"}]},
        {"type": "fail", "status_code": 200},
        {"type": "try", "as": "bad name", "steps": [{"type": "fail"}]}]},
    "b": {"route": {"path": "/fn/posts/{slug}", "method": "GET"}, "steps": [{"type": "response"}]},
    "c": {"route": {"path": "/fn/x/{id", "method": "GET"}, "steps": [{"type": "response"}]}
  }
}`)
	want := []string{
		"functions.a.route.input.q.type",
		"functions.a.route.input.n",
		"functions.a.route.input.s.pattern",
		"functions.a.steps[0].order_by",
		"functions.a.steps[0].limit",
		"functions.a.steps[0].include[0].table",
		"functions.a.steps[1].aggregates.n",
		"functions.a.steps[2].steps[0]",
		"functions.a.steps[2].steps[1]",
		"functions.a.steps[3].status_code",
		"functions.a.steps[4].as",
		"functions.c.route.path",
	}
	got := map[string]bool{}
	routeDup := false
	for _, i := range p.Validate() {
		if i.Severity == "error" {
			got[i.Path] = true
			if strings.HasSuffix(i.Path, ".route") {
				routeDup = true
			}
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing error at %s (got %v)", w, got)
		}
	}
	if !routeDup {
		t.Error("/fn/posts/{id} and /fn/posts/{slug} not reported as the same route")
	}
}
