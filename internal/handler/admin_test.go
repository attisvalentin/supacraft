package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const adminSecret = "0123456789abcdef0123456789abcdef"

// newCentralAPI applies a project that references ${STRIPE_KEY} into a
// central data directory and serves it like serve --data-dir.
func newCentralAPI(t *testing.T, secret string, central bool) (*httptest.Server, string) {
	t.Helper()
	projDir, data := t.TempDir(), t.TempDir()
	cfgJSON := `{"name": "shop",
	  "api_settings": {"env": {"STRIPE_KEY": "${STRIPE_KEY}"}},
	  "functions": {"key": {"route": {"path": "/fn/key", "method": "GET"},
	    "steps": [{"type": "response", "data": {"key": "{{env.STRIPE_KEY}}"}}]}}}`
	if err := os.WriteFile(filepath.Join(projDir, project.FileName), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// A project .env must not leak into a central server.
	if err := os.WriteFile(filepath.Join(projDir, ".env"), []byte("STRIPE_KEY=from-laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(projDir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.ApplyTo(context.Background(), data, project.ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MissingEnv) != 1 || res.MissingEnv[0] != "STRIPE_KEY" {
		t.Fatalf("missing env: %v", res.MissingEnv)
	}
	raw, _ := os.ReadFile(filepath.Join(data, "tenants", "shop.json"))
	if !strings.Contains(string(raw), "${STRIPE_KEY}") || strings.Contains(string(raw), "from-laptop") {
		t.Fatalf("central tenant config must keep the reference: %s", raw)
	}

	cfg := &config.Config{DataDir: data, StoragePath: filepath.Join(data, "storage"), MaxFileSize: 1 << 20, MaxBodySize: 1 << 16, AdminSecret: secret, Central: central}
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	errLog, err := errorlog.Open(filepath.Join(data, "errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errLog.Close() })
	srv := httptest.NewServer(NewRouter(tenant.NewCachedStore(store), tenant.NewPoolManager(cfg), errLog, cfg))
	t.Cleanup(srv.Close)
	return srv, data
}

func getKey(t *testing.T, base string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/fn/key", nil)
	req.Header.Set("X-Tenant-ID", "shop")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestAdminSecrets_SetResolvesWithoutRestart(t *testing.T) {
	srv, data := newCentralAPI(t, adminSecret, true)
	ctx := context.Background()
	c, err := remote.New(srv.URL, adminSecret)
	if err != nil {
		t.Fatal(err)
	}

	var st SecretsStatus
	if err := c.Do(ctx, "GET", "/admin/tenants/shop/secrets", nil, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Missing) != 1 || st.Missing[0] != "STRIPE_KEY" || !st.Deployed {
		t.Fatalf("status before set: %+v", st)
	}

	if err := c.Do(ctx, "PUT", "/admin/tenants/shop/secrets/STRIPE_KEY", map[string]string{"value": "sk_live_1"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := getKey(t, srv.URL); !strings.Contains(got, "sk_live_1") {
		t.Fatalf("secret not used by the function: %s", got)
	}
	if err := c.Do(ctx, "GET", "/admin/tenants/shop/secrets", nil, &st); err != nil || len(st.Missing) != 0 || st.Set[0] != "STRIPE_KEY" {
		t.Fatalf("status after set: %+v %v", st, err)
	}

	// The tenant config on disk still holds only the reference.
	raw, _ := os.ReadFile(filepath.Join(data, "tenants", "shop.json"))
	if strings.Contains(string(raw), "sk_live_1") {
		t.Fatal("secret value written into the tenant config")
	}

	if err := c.Do(ctx, "DELETE", "/admin/tenants/shop/secrets/STRIPE_KEY", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := getKey(t, srv.URL); strings.Contains(got, "sk_live_1") {
		t.Fatalf("unset secret still served: %s", got)
	}
}

func TestAdminSecrets_RequireSignatureAndCentralServer(t *testing.T) {
	srv, _ := newCentralAPI(t, adminSecret, true)
	ctx := context.Background()

	// Unsigned and wrongly signed requests are refused.
	resp, err := http.Post(srv.URL+"/admin/tenants/shop/secrets/X", "application/json", strings.NewReader(`{"value":"v"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		t.Fatalf("unsigned request accepted: %d", resp.StatusCode)
	}
	wrong, _ := remote.New(srv.URL, strings.Repeat("x", 32))
	if err := wrong.Do(ctx, "PUT", "/admin/tenants/shop/secrets/X", map[string]string{"value": "v"}, nil); err == nil {
		t.Fatal("wrong secret accepted")
	}
	good, _ := remote.New(srv.URL, adminSecret)
	if err := good.Do(ctx, "PUT", "/admin/tenants/..%2Fx/secrets/X", map[string]string{"value": "v"}, nil); err == nil {
		t.Fatal("path traversal in tenant id accepted")
	}

	// A short admin secret disables the endpoints.
	weak, _ := newCentralAPI(t, "short", true)
	c, _ := remote.New(weak.URL, "short")
	if err := c.Do(ctx, "GET", "/admin/tenants/shop/secrets", nil, nil); err == nil {
		t.Fatal("short admin secret enabled the endpoints")
	}

	// A single-project server has no admin secrets.
	single, _ := newCentralAPI(t, adminSecret, false)
	c, _ = remote.New(single.URL, adminSecret)
	if err := c.Do(ctx, "GET", "/admin/tenants/shop/secrets", nil, nil); err == nil {
		t.Fatal("admin secrets served by a non-central server")
	}
}

func TestRemote_RefusesPlainHTTP(t *testing.T) {
	if _, err := remote.New("http://api.example.com", adminSecret); err == nil {
		t.Fatal("plain http to a remote host accepted")
	}
	if _, err := remote.New("https://user:pass@api.example.com", adminSecret); err == nil {
		t.Fatal("credentials in the server URL accepted")
	}
	for _, ok := range []string{"https://api.example.com", "http://localhost:8080", "http://127.0.0.1:8080"} {
		if _, err := remote.New(ok, adminSecret); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}
