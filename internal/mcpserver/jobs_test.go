package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Locally the agent queues and retries jobs without a prompt.
func TestJobTools_Local(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := `{"name": "demo", "functions": {
	  "work": {"route": {"path": "", "method": "PRIVATE"}, "steps": [{"type": "response", "data": {}}]},
	  "page": {"route": {"path": "/fn/page", "method": "GET"}, "steps": [{"type": "response", "data": {}}]}}}`
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
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}

	if out, isErr := call("enqueue_job", map[string]any{"function": "work", "args": map[string]any{"n": 1}}); isErr || !strings.Contains(out, "job_id") {
		t.Fatalf("enqueue: %s", out)
	}
	if out, isErr := call("enqueue_job", map[string]any{"function": "page"}); !isErr || !strings.Contains(out, "not PRIVATE") {
		t.Fatalf("a public function was queued: %s", out)
	}
	jobs, err := tenant.ListTenantJobs(ctx, p.DataDir(), p.TenantID(), "", 10)
	if err != nil || len(jobs) != 1 || jobs[0].Function != "work" || jobs[0].Args["n"] != 1.0 {
		t.Fatalf("queue: %+v %v", jobs, err)
	}
	if out, isErr := call("retry_job", map[string]any{"id": jobs[0].ID}); !isErr || !strings.Contains(out, "no failed job") {
		t.Fatalf("retry of a pending job: %s", out)
	}
}
