package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// On a central server nothing reaches the server until the user approves
// in the prompt, and the fallback command is safely quoted.
func TestJobTools_ServerNeedsApproval(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var hits []string
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data": {"id": "job-1"}}`))
	}))
	defer central.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(`{"name": "demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := "SUPACRAFT_SERVER=" + central.URL + "\nSUPACRAFT_ADMIN_SECRET=" + strings.Repeat("s", 40) + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	call := func(handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), name string, args map[string]any) string {
		t.Helper()
		srv := mcp.NewServer(&mcp.Implementation{Name: "supacraft", Version: "test"}, nil)
		(&server{opts: Options{Dir: dir}}).register(srv)
		ct, st := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ss.Close()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{ElicitationHandler: handler}).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	enqueue := map[string]any{"function": "work; rm -rf ~", "args": map[string]any{"x": "'quoted'"}, "server": true}
	retry := map[string]any{"id": "job-1", "server": true}

	out := call(nil, "enqueue_job", enqueue)
	if !strings.Contains(out, `'work; rm -rf ~'`) || !strings.Contains(out, `'\''`) {
		t.Errorf("fallback command not quoted: %s", out)
	}
	call(answer("decline", false), "enqueue_job", enqueue)
	call(answer("accept", false), "retry_job", retry)
	mu.Lock()
	early := len(hits)
	mu.Unlock()
	if early != 0 {
		t.Fatalf("the server was called without approval: %v", hits)
	}

	call(answer("accept", true), "enqueue_job", enqueue)
	call(answer("accept", true), "retry_job", retry)
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 2 || hits[0] != "POST /admin/tenants/demo/jobs" || hits[1] != "POST /admin/tenants/demo/jobs/job-1/retry" {
		t.Fatalf("approved calls: %v", hits)
	}
}

func TestShellCommand(t *testing.T) {
	got := shellCommand("supacraft", "jobs", "enqueue", "it's", "--args", `{"a": 1}`, "--server=https://api.example.com")
	want := `supacraft jobs enqueue 'it'\''s' --args '{"a": 1}' --server=https://api.example.com`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
