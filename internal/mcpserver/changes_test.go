package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// get_changes reads the realtime log: readable columns, after since.
func TestGetChanges_Local(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := `{"name": "demo",
	  "schema": {"tables": [{"name": "news", "columns": [
	    {"name": "id", "type": "uuid", "primary": true},
	    {"name": "title", "type": "varchar"},
	    {"name": "draft", "type": "varchar", "readable": false}]}]},
	  "api_settings": {"tables": {"news": {"query": {"enabled": true}}}, "realtime": {"tables": ["news"]}}}`
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
	db, tcfg, err := tenant.OpenTenantDB(ctx, p.DataDir(), p.TenantID())
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"id": "1", "title": "a", "draft": "x"}, {"id": "2", "title": "b", "draft": "y"}}
	if err := tenant.EmitChanges(ctx, &tenant.DB{DB: db}, tcfg, "news", tenant.OpInsert, rows); err != nil {
		t.Fatal(err)
	}
	db.Close()

	srv := mcp.NewServer(&mcp.Implementation{Name: "supacraft", Version: "test"}, nil)
	(&server{opts: Options{Dir: dir}}).register(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"since": 1}})
	if err != nil || res.IsError {
		t.Fatalf("get_changes: %v %+v", err, res)
	}
	var out struct {
		Changes []tenant.Change `json:"changes"`
		LastSeq int64           `json:"last_seq"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.LastSeq != 2 || len(out.Changes) != 1 || out.Changes[0].Row["title"] != "b" {
		t.Fatalf("changes: %+v", out)
	}
	if _, leaked := out.Changes[0].Row["draft"]; leaked {
		t.Fatal("a non-readable column was returned")
	}
}
