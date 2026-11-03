package mcpserver

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
)

// Every central-server change waits for approval: until then nothing
// reaches the server; a dry run needs none.
func TestAdminTools_ServerNeedsApproval(t *testing.T) {
	central := newFakeCentral(t, `{"tenant": "demo", "dry_run": true}`)
	dir := projectDir(t, `{"name": "demo"}`, central)
	opts := Options{Dir: dir}

	calls := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"deploy", map[string]any{}, "PUT /admin/tenants/demo"},
		{"manage_tenant", map[string]any{"action": "disable"}, "POST /admin/tenants/demo/disable"},
		{"manage_tenant", map[string]any{"action": "remove", "tenant": "other", "purge": true}, "DELETE /admin/tenants/other?purge=true"},
		{"unset_secret", map[string]any{"name": "STRIPE_KEY"}, "DELETE /admin/tenants/demo/secrets/STRIPE_KEY"},
	}
	for _, c := range calls {
		for _, refuse := range []elicitFunc{nil, answer("decline", false), answer("accept", false)} {
			callTool(t, opts, refuse, c.tool, c.args)
		}
	}
	if hits := central.requests(); len(hits) != 0 {
		t.Fatalf("reached the server without approval: %v", hits)
	}

	callTool(t, opts, nil, "deploy", map[string]any{"dry_run": true})
	if hits := central.requests(); len(hits) != 1 || hits[0] != "PUT /admin/tenants/demo" {
		t.Fatalf("dry run: %v", hits)
	}
	for _, c := range calls {
		callTool(t, opts, answer("accept", true), c.tool, c.args)
	}
	hits := central.requests()[1:]
	for i, c := range calls {
		if i >= len(hits) || hits[i] != c.want {
			t.Fatalf("approved calls: %v", hits)
		}
	}
}

// A deploy approval covers the file as it was: after an edit the approval
// prompt's state no longer matches.
func TestDeployApproval_BoundToContent(t *testing.T) {
	a := approval{action: []string{"deploy", "srv", "demo", "hash-1", "false", "false"}}
	b := approval{action: []string{"deploy", "srv", "demo", "hash-2", "false", "false"}}
	if a.state() == b.state() {
		t.Fatal("different content, same approval state")
	}
}

// Resetting the local database needs approval too.
func TestSeedReset_NeedsApproval(t *testing.T) {
	dir := projectDir(t, `{"name": "demo",
	  "schema": {"tables": [{"name": "notes", "columns": [{"name": "id", "type": "uuid", "primary": true}]}]},
	  "seed": {"tables": {"notes": [{"id": "a"}]}}}`, nil)
	opts := Options{Dir: dir}
	callTool(t, opts, nil, "seed", map[string]any{})
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(p.DataDir(), p.TenantID()+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO "notes" ("id") VALUES ('mine')`); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "notes"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	callTool(t, opts, answer("decline", false), "seed", map[string]any{"reset": true})
	if count() != 2 {
		t.Fatal("reset without approval")
	}
	callTool(t, opts, answer("accept", true), "seed", map[string]any{"reset": true})
	if count() != 1 {
		t.Fatalf("approved reset left %d rows", count())
	}
}

func TestInitProject(t *testing.T) {
	dir := t.TempDir()
	started := false
	opts := Options{Dir: dir, Started: func(context.Context) (func(context.Context), error) {
		started = true
		return nil, nil
	}}
	if out, isErr := callTool(t, opts, nil, "init_project", map[string]any{"name": "Shop"}); isErr {
		t.Fatal(out)
	}
	for _, f := range []string{project.FileName, ".env", ".gitignore", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if !started {
		t.Error("the API was not started")
	}
	if out, isErr := callTool(t, Options{Dir: dir}, nil, "init_project", map[string]any{"name": "Again"}); !isErr || !strings.Contains(out, "already exists") {
		t.Errorf("second init: %s", out)
	}
}
