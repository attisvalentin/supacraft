package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

const rolesProject = `{
  "name": "demo",
  "auth_enabled": true,
  "schema": {
    "tables": [
      {"name": "posts", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "title", "type": "varchar"}]},
      {"name": "notes", "columns": [
        {"name": "id", "type": "uuid", "primary": true},
        {"name": "body", "type": "varchar"},
        {"name": "owner_id", "type": "uuid"}]}
    ]
  },
  "api_settings": {
    "auth": {"roles": ["user", "editor", "admin"], "default_role": "user"},
    "tables": {
      "posts": {"query": {"enabled": true}, "create": {"enabled": true, "roles": ["editor", "admin"]}},
      "notes": {"query": {"enabled": true, "auth": true, "row_policy": "$auth.role = 'admin' OR owner_id = $auth.id"}}
    }
  },
  "functions": {
    "admin_only": {"route": {"path": "/fn/admin", "method": "GET", "roles": ["admin"]},
                   "steps": [{"type": "response", "data": {"role": "{{auth.role}}"}}]}
  }
}`

func TestRoles(t *testing.T) {
	a := newTestAPIWith(t, rolesProject)
	dataDir := filepath.Dir(a.dbPath)
	ctx := context.Background()

	aliceID := a.register("alice@example.com")
	bobID := a.register("bob@example.com")
	bearer := "Bearer " + a.login("alice@example.com")["token"].(string)
	bobBearer := "Bearer " + a.login("bob@example.com")["token"].(string)

	createPost := `{"table_id":"posts","data":{"title":"hi"}}`
	if code, _, raw := a.do("POST", "/create", createPost); code != http.StatusUnauthorized {
		t.Fatalf("create without a token: %d %s", code, raw)
	}
	if code, _, raw := a.do("POST", "/create", createPost, "Authorization", bearer); code != http.StatusForbidden {
		t.Fatalf("create as user: %d %s", code, raw)
	}

	// The role is read on every request: the token alice already has
	// works as soon as she is promoted.
	if _, err := tenant.SetUserRole(ctx, dataDir, "demo", "alice@example.com", "editor"); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := a.do("POST", "/create", createPost, "Authorization", bearer); code != http.StatusCreated {
		t.Fatalf("create as editor: %d %s", code, raw)
	}
	if code, _, raw := a.do("GET", "/fn/admin", "", "Authorization", bearer); code != http.StatusForbidden {
		t.Fatalf("admin function as editor: %d %s", code, raw)
	}
	if _, err := tenant.SetUserRole(ctx, dataDir, "demo", "alice@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	code, out, raw := a.do("GET", "/fn/admin", "", "Authorization", bearer)
	if code != http.StatusOK || out["role"] != "admin" {
		t.Fatalf("admin function as admin: %d %s", code, raw)
	}

	// $auth.role in a row policy: admins see every note, others their own.
	db, err := sql.Open("sqlite", a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, owner := range []string{aliceID, bobID, bobID} {
		if _, err := db.ExecContext(ctx, `INSERT INTO "notes" ("id", "body", "owner_id") VALUES (?, 'n', ?)`, string(rune('a'+i)), owner); err != nil {
			t.Fatal(err)
		}
	}
	countNotes := func(auth string) int {
		t.Helper()
		code, out, raw := a.do("POST", "/query", `{"table_id":"notes"}`, "Authorization", auth)
		if code != http.StatusOK {
			t.Fatalf("query notes: %d %s", code, raw)
		}
		return len(out["data"].([]any))
	}
	if n := countNotes(bearer); n != 3 {
		t.Fatalf("admin sees %d notes, want 3", n)
	}
	if n := countNotes(bobBearer); n != 2 {
		t.Fatalf("bob sees %d notes, want 2", n)
	}

	// Clients can't pick their own role.
	if code, _, raw := a.do("POST", "/auth/register", `{"email":"eve@example.com","password":"Secret123!","role":"admin"}`); code != http.StatusBadRequest {
		t.Fatalf("register with a role: %d %s", code, raw)
	}

	if _, err := tenant.SetUserRole(ctx, dataDir, "demo", "bob@example.com", "root"); !errors.As(err, new(*tenant.UnknownRoleError)) {
		t.Fatalf("unknown role: %v", err)
	}
	if _, err := tenant.SetUserRole(ctx, dataDir, "demo", "nobody@example.com", "admin"); !errors.Is(err, tenant.ErrUserNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	users, total, err := tenant.ListUsers(ctx, dataDir, "demo", 10, 0)
	if err != nil || total != 2 || users[0].Role != "admin" || users[1].Role != "user" {
		t.Fatalf("list users: %+v %d %v", users, total, err)
	}
}

func TestRoles_IncludeChecksRelatedTableRoles(t *testing.T) {
	a := newTestAPIWith(t, `{
  "name": "demo",
  "auth_enabled": true,
  "schema": {
    "tables": [
      {"name": "posts", "columns": [{"name": "id", "type": "uuid", "primary": true}, {"name": "author_id", "type": "uuid"}]}
    ],
    "relations": [{"from": {"table": "posts", "column": "author_id"}, "to": {"table": "users", "column": "id"}, "type": "one-to-many"}]
  },
  "api_settings": {
    "auth": {"roles": ["user", "admin"]},
    "tables": {
      "posts": {"query": {"enabled": true}},
      "users": {"query": {"enabled": true, "roles": ["admin"]}}
    }
  }
}`)
	a.register("ann@example.com")
	bearer := "Bearer " + a.login("ann@example.com")["token"].(string)
	if code, _, raw := a.do("POST", "/query", `{"table_id":"posts","include":[{"table_id":"users"}]}`, "Authorization", bearer); code != http.StatusBadRequest {
		t.Fatalf("include of an admin-only table as user: %d %s", code, raw)
	}
	if code, _, raw := a.do("POST", "/query", `{"table_id":"users"}`, "Authorization", bearer); code != http.StatusForbidden {
		t.Fatalf("query of an admin-only table as user: %d %s", code, raw)
	}
}
