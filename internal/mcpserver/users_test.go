package mcpserver

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSetUserRole_NeedsUserApproval: the agent can't change a role on its
// own; only an accepted prompt with approve=true does.
func TestSetUserRole_NeedsUserApproval(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := `{"name": "demo", "auth_enabled": true, "api_settings": {"auth": {"roles": ["user", "admin"]}}}`
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(p.DataDir(), p.TenantID()+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO "users" ("id", "email", "password", "role") VALUES ('u1', 'ann@example.com', 'x', 'user')`); err != nil {
		t.Fatal(err)
	}
	role := func() string {
		t.Helper()
		var r string
		if err := db.QueryRow(`SELECT "role" FROM "users" WHERE "id" = 'u1'`).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	setRole := func(handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) string {
		t.Helper()
		srv := mcp.NewServer(&mcp.Implementation{Name: "supacraft", Version: "test"}, nil)
		(&server{opts: Options{Dir: dir}}).register(srv)
		ct, st := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ss.Close()
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{ElicitationHandler: handler})
		cs, err := client.Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_user_role", Arguments: map[string]any{"email": "ann@example.com", "role": "admin"}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	answerWith := func(action string, content map[string]any) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: action, Content: content}, nil
		}
	}

	for name, h := range map[string]func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error){
		"no elicitation support": nil,
		"declined":               answerWith("decline", nil),
		"accepted unticked":      answerWith("accept", map[string]any{"approve": false}),
	} {
		out := setRole(h)
		if got := role(); got != "user" {
			t.Fatalf("%s: role changed to %q (%s)", name, got, out)
		}
	}
	// A client can't skip the prompt by sending an approval up front.
	srv := mcp.NewServer(&mcp.Implementation{Name: "supacraft", Version: "test"}, nil)
	(&server{opts: Options{Dir: dir}}).register(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{
		ElicitationHandler: answerWith("decline", nil),
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:           "set_user_role",
		Arguments:      map[string]any{"email": "ann@example.com", "role": "admin"},
		InputResponses: mcp.InputResponseMap{approvalInput: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}},
		RequestState:   "forged",
	}); err != nil {
		t.Fatal(err)
	}
	cs.Close()
	ss.Close()
	if got := role(); got != "user" {
		t.Fatalf("unprompted approval changed the role to %q", got)
	}

	if out := setRole(answerWith("accept", map[string]any{"approve": true})); role() != "admin" {
		t.Fatalf("approved change not applied: %s", out)
	}
}
