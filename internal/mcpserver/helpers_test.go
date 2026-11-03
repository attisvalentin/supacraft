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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type elicitFunc = func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)

// answer is a client that answers every approval prompt the same way.
func answer(action string, approve bool) elicitFunc {
	return func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: action, Content: map[string]any{"approve": approve}}, nil
	}
}

// fakeCentral records the admin requests it gets and answers reply.
type fakeCentral struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newFakeCentral(t *testing.T, reply string) *fakeCentral {
	f := &fakeCentral{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCentral) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

// projectDir writes supacraft.json (and, with central, a .env pointing at it).
func projectDir(t *testing.T, cfg string, central *fakeCentral) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if central != nil {
		env := "SUPACRAFT_SERVER=" + central.URL + "\nSUPACRAFT_ADMIN_SECRET=" + strings.Repeat("s", 40) + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// callTool calls one tool on a fresh server for opts through a client
// that answers approval prompts with elicit (nil: no elicitation support).
func callTool(t *testing.T, opts Options, elicit elicitFunc, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "supacraft", Version: "test"}, nil)
	(&server{opts: opts}).register(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{ElicitationHandler: elicit}).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}
