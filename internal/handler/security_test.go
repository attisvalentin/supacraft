package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"

	_ "modernc.org/sqlite"
)

// The project mirrors a typical app: posts belong to users, posts are public,
// users are not exposed, and one function deletes a post by id.
const securityProject = `{
  "name": "demo",
  "auth_enabled": true,
  "schema": {
    "tables": [
      {"name": "posts", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "title", "type": "varchar"},
        {"name": "author_id", "type": "uuid"}]},
      {"name": "notes", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "body", "type": "varchar"},
        {"name": "post_id", "type": "uuid"},
        {"name": "owner_id", "type": "uuid"},
        {"name": "internal", "type": "varchar", "readable": false}]}
    ],
    "relations": [
      {"from": {"table": "posts", "column": "author_id"}, "to": {"table": "users", "column": "id"}, "type": "one-to-many"},
      {"from": {"table": "notes", "column": "post_id"}, "to": {"table": "posts", "column": "id"}, "type": "one-to-many"}
    ]
  },
  "api_settings": {
    "crud_endpoints": {"query": true, "create": true, "update": false, "delete": false},
    "tables": {
      "posts": {"query": {"enabled": true}, "create": {"enabled": true}},
      "notes": {"query": {"enabled": true, "auth": true, "row_policy": "owner_id = $auth.id"}}
    }
  },
  "functions": {
    "delete_post": {
      "route": {"path": "/fn/posts/delete", "method": "POST"},
      "steps": [
        {"type": "db.delete", "table": "posts", "where": {"id": "{{request.id}}"}, "assign": "res"},
        {"type": "response", "status_code": 200, "data": {"res": "{{res}}"}}
      ]
    }
  }
}`

type testAPI struct {
	t       *testing.T
	handler http.Handler
	dbPath  string
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(securityProject), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		DataDir:       p.DataDir(),
		StoragePath:   filepath.Join(p.DataDir(), "storage"),
		MaxFileSize:   1 << 20,
		MaxBodySize:   1 << 16,
		DefaultTenant: p.TenantID(),
	}
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pools := tenant.NewPoolManager(cfg)
	errLog, err := errorlog.Open(filepath.Join(p.DataDir(), "errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errLog.Close() })

	return &testAPI{
		t:       t,
		handler: NewRouter(tenant.NewCachedStore(store), pools, errLog, cfg),
		dbPath:  filepath.Join(p.DataDir(), p.TenantID()+".db"),
	}
}

func (a *testAPI) do(method, path, body string, headers ...string) (int, map[string]any, string) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (a *testAPI) register(email string) string {
	a.t.Helper()
	code, out, raw := a.do("POST", "/auth/register", `{"email":"`+email+`","password":"Secret123!"}`)
	if code != http.StatusCreated {
		a.t.Fatalf("register %s: %d %s", email, code, raw)
	}
	return out["data"].(map[string]any)["id"].(string)
}

func (a *testAPI) login(email string) map[string]any {
	a.t.Helper()
	code, out, raw := a.do("POST", "/auth/login", `{"email":"`+email+`","password":"Secret123!"}`)
	if code != http.StatusOK {
		a.t.Fatalf("login %s: %d %s", email, code, raw)
	}
	return out["data"].(map[string]any)
}

func (a *testAPI) createPost(title, authorID string) string {
	a.t.Helper()
	code, out, raw := a.do("POST", "/create", `{"table_id":"posts","data":{"title":"`+title+`","author_id":"`+authorID+`"}}`)
	if code != http.StatusCreated {
		a.t.Fatalf("create post: %d %s", code, raw)
	}
	return out["data"].(map[string]any)["id"].(string)
}

func TestInclude_RejectsSQLInjectionInSelect(t *testing.T) {
	api := newTestAPI(t)
	api.createPost("hello", api.register("victim@x.co"))

	payload := `{"table_id":"posts","include":[{"table_id":"posts","select":["x\" , (SELECT jwt_secret FROM api_config) AS leak, id FROM posts --"]}]}`
	code, out, raw := api.do("POST", "/query", payload)
	if code != http.StatusBadRequest || out["data"] != nil {
		t.Fatalf("injection not rejected: %d %s", code, raw)
	}
}

func TestInclude_RequiresQueryAccessToRelatedTable(t *testing.T) {
	api := newTestAPI(t)
	api.createPost("hello", api.register("victim@x.co"))

	code, _, raw := api.do("POST", "/query", `{"table_id":"posts","include":[{"table_id":"users"}]}`)
	if code == http.StatusOK || strings.Contains(raw, "victim@x.co") {
		t.Fatalf("users table leaked through include: %d %s", code, raw)
	}
}

func TestInclude_RejectsNonReadableColumns(t *testing.T) {
	api := newTestAPI(t)
	alice := api.register("alice@x.co")
	insertNote(t, api.dbPath, api.createPost("hello", alice), alice, "note")
	token := api.login("alice@x.co")["token"].(string)
	for _, col := range []string{"nope", "internal"} {
		code, _, raw := api.do("POST", "/query", `{"table_id":"posts","include":[{"table_id":"notes","select":["`+col+`"]}]}`, "Authorization", "Bearer "+token)
		if code != http.StatusBadRequest {
			t.Errorf("include select %q accepted: %d %s", col, code, raw)
		}
	}
}

func TestInclude_AppliesAuthAndRowPolicy(t *testing.T) {
	api := newTestAPI(t)
	alice := api.register("alice@x.co")
	bob := api.register("bob@x.co")
	post := api.createPost("hello", alice)
	insertNote(t, api.dbPath, post, alice, "alice note")
	insertNote(t, api.dbPath, post, bob, "bob note")

	include := `{"table_id":"posts","include":[{"table_id":"notes"}]}`
	if code, _, raw := api.do("POST", "/query", include); code == http.StatusOK {
		t.Fatalf("notes included without a token: %s", raw)
	}

	token := api.login("alice@x.co")["token"].(string)
	code, _, raw := api.do("POST", "/query", include, "Authorization", "Bearer "+token)
	if code != http.StatusOK {
		t.Fatalf("authorized include failed: %d %s", code, raw)
	}
	if !strings.Contains(raw, "alice note") || strings.Contains(raw, "bob note") {
		t.Fatalf("row policy not applied to include: %s", raw)
	}
}

func TestInclude_AuthorizesBeforeValidatingColumns(t *testing.T) {
	api := newTestAPI(t)
	_, out, _ := api.do("POST", "/query", `{"table_id":"posts","include":[{"table_id":"notes","select":["internal"]}]}`)
	if out["error"] != "included table is not accessible" {
		t.Fatalf("column check ran before authorization: %v", out["error"])
	}
}

func TestQuery_DefaultsToMaxLimit(t *testing.T) {
	api := newTestAPI(t)
	author := api.register("a@x.co")
	for i := 0; i < 105; i++ {
		api.createPost("p", author)
	}
	_, out, _ := api.do("POST", "/query", `{"table_id":"posts"}`)
	if n := len(out["data"].([]any)); n != 100 {
		t.Fatalf("got %d rows without limit, want 100", n)
	}
	if meta, _ := out["meta"].(map[string]any); meta == nil || meta["has_more"] != true {
		t.Fatalf("meta missing or wrong: %v", out["meta"])
	}
}

func TestFunctionWhere_RejectsOperatorInjection(t *testing.T) {
	api := newTestAPI(t)
	author := api.register("a@x.co")
	api.createPost("one", author)
	api.createPost("two", author)

	code, _, raw := api.do("POST", "/fn/posts/delete", `{"id":{"$ne":""}}`)
	if code == http.StatusOK {
		t.Fatalf("operator injection accepted: %s", raw)
	}
	_, out, _ := api.do("POST", "/query", `{"table_id":"posts"}`)
	if n := len(out["data"].([]any)); n != 2 {
		t.Fatalf("%d posts left, want 2", n)
	}
}

func TestTenantHeader_RejectsPathTraversal(t *testing.T) {
	api := newTestAPI(t)
	for _, id := range []string{"x/../demo", "../tenants/demo", "./demo", "DEMO"} {
		code, _, raw := api.do("GET", "/meta/schema", "", "X-Tenant-ID", id)
		if code != http.StatusNotFound {
			t.Errorf("X-Tenant-ID %q: %d %s", id, code, raw)
		}
	}
}

func TestLogsAreNotServed(t *testing.T) {
	api := newTestAPI(t)
	if code, _, raw := api.do("GET", "/logs", ""); code == http.StatusOK {
		t.Fatalf("/logs is public: %s", raw)
	}
}

func TestErrorResponses_HideInternalDetails(t *testing.T) {
	api := newTestAPI(t)
	api.register("dup@x.co")
	code, out, _ := api.do("POST", "/auth/register", `{"email":"dup@x.co","password":"Secret123!"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate register: %d", code)
	}
	if msg := out["error"]; msg != "registration failed" {
		t.Fatalf("error leaks details: %v", msg)
	}
}

func TestRegister_EnforcesPasswordAndIgnoresIsActive(t *testing.T) {
	api := newTestAPI(t)
	if code, _, raw := api.do("POST", "/auth/register", `{"email":"s@x.co","password":"a"}`); code != http.StatusBadRequest {
		t.Fatalf("short password accepted: %d %s", code, raw)
	}
	if code, _, raw := api.do("POST", "/auth/register", `{"email":"s@x.co","password":"Secret123!","is_active":true}`); code != http.StatusBadRequest {
		t.Fatalf("client-set is_active accepted: %d %s", code, raw)
	}
}

func TestLoginAndRefresh_RespectInactiveAndDeletedUsers(t *testing.T) {
	api := newTestAPI(t)
	api.register("gone@x.co")
	api.register("off@x.co")
	refresh := api.login("gone@x.co")["refresh_token"].(string)

	execSQL(t, api.dbPath, `UPDATE users SET is_active = 0 WHERE email = 'off@x.co'`)
	if code, _, _ := api.do("POST", "/auth/login", `{"email":"off@x.co","password":"Secret123!"}`); code != http.StatusUnauthorized {
		t.Fatalf("inactive user logged in: %d", code)
	}

	execSQL(t, api.dbPath, `DELETE FROM users WHERE email = 'gone@x.co'`)
	if code, _, _ := api.do("POST", "/auth/refresh", `{"refresh_token":"`+refresh+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("deleted user refreshed a token: %d", code)
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	api := newTestAPI(t)
	body := `{"email":"nobody@x.co","password":"wrong-password"}`
	var last int
	for i := 0; i <= authAttemptsPerMinute; i++ {
		last, _, _ = api.do("POST", "/auth/login", body)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("attempt %d got %d, want 429", authAttemptsPerMinute+1, last)
	}
}

func TestJSONBodyIsCapped(t *testing.T) {
	api := newTestAPI(t)
	big := `{"table_id":"posts","data":{"title":"` + strings.Repeat("a", 1<<17) + `"}}`
	if code, _, _ := api.do("POST", "/create", big); code == http.StatusCreated {
		t.Fatal("oversized body accepted")
	}
	if code, _, _ := api.do("POST", "/fn/posts/delete", `{"id":"`+strings.Repeat("a", 1<<17)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized function body: %d, want 413", code)
	}
	if code, _, _ := api.do("POST", "/fn/posts/delete", `{"id":`); code != http.StatusBadRequest {
		t.Fatalf("malformed function body: %d, want 400", code)
	}
}

func TestServedInline(t *testing.T) {
	for ct, want := range map[string]bool{
		"text/html; charset=utf-8":  false,
		"text/xml; charset=utf-8":   false,
		"text/plain; charset=utf-8": false,
		"image/svg+xml":             false,
		"image/png":                 true,
		"video/mp4":                 true,
		"application/pdf":           true,
	} {
		if got := servedInline(ct); got != want {
			t.Errorf("servedInline(%q) = %v", ct, got)
		}
	}
}

func insertNote(t *testing.T, dbPath, postID, ownerID, body string) {
	t.Helper()
	execSQL(t, dbPath, `INSERT INTO "notes" ("id", "body", "post_id", "owner_id") VALUES (lower(hex(randomblob(16))), ?, ?, ?)`, body, postID, ownerID)
}

func execSQL(t *testing.T, dbPath, stmt string, args ...any) {
	t.Helper()
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
}
