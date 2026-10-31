package handler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/tenant"

	"net/http/httptest"
)

func newEmptyCentral(t *testing.T) (*remote.Client, string, string) {
	t.Helper()
	data := t.TempDir()
	cfg := &config.Config{DataDir: data, StoragePath: filepath.Join(data, "storage"), MaxFileSize: 1 << 20, MaxBodySize: 1 << 16, AdminSecret: adminSecret, Central: true}
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
	c, err := remote.New(srv.URL, adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.URL, data
}

func shopJSON(t *testing.T, extraColumn bool) map[string]any {
	t.Helper()
	cols := `{"name": "id", "type": "uuid", "primary": true}, {"name": "name", "type": "text"}`
	if extraColumn {
		cols += `, {"name": "price", "type": "real"}`
	}
	src := `{"name": "shop",
	  "schema": {"tables": [{"name": "products", "columns": [` + cols + `]}]},
	  "api_settings": {"env": {"STRIPE_KEY": "${STRIPE_KEY}"}},
	  "functions": {"key": {"route": {"path": "/fn/key", "method": "GET"},
	    "steps": [{"type": "response", "data": {"key": "{{env.STRIPE_KEY}}"}}]}}}`
	var raw map[string]any
	if err := json.Unmarshal([]byte(src), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func deploy(c *remote.Client, req DeployRequest) (*DeployResponse, error) {
	var res DeployResponse
	err := c.Do(context.Background(), "PUT", "/admin/tenants/shop", req, &res)
	return &res, err
}

func TestDeploy_FullFlow(t *testing.T) {
	c, base, data := newEmptyCentral(t)
	tenantFile := filepath.Join(data, "tenants", "shop.json")

	// Dry run plans but changes nothing.
	res, err := deploy(c, DeployRequest{Project: shopJSON(t, false), DryRun: true, AllowMissingSecrets: true})
	if err != nil || !res.Created || len(res.Migrations) != 1 || !res.DryRun {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(data, "shop.db")); !os.IsNotExist(err) {
		t.Fatal("dry run created the database")
	}

	// A missing secret stops the deploy before anything is written.
	_, err = deploy(c, DeployRequest{Project: shopJSON(t, false)})
	var rerr *remote.Error
	if !errors.As(err, &rerr) || len(rerr.MissingSecrets) != 1 || rerr.MissingSecrets[0] != "STRIPE_KEY" {
		t.Fatalf("missing secret not reported: %v", err)
	}
	if _, err := os.Stat(tenantFile); !os.IsNotExist(err) {
		t.Fatal("deploy with a missing secret wrote the tenant")
	}

	if err := c.Do(context.Background(), "PUT", "/admin/tenants/shop/secrets/STRIPE_KEY", map[string]string{"value": "sk_1"}, nil); err != nil {
		t.Fatal(err)
	}
	res, err = deploy(c, DeployRequest{Project: shopJSON(t, false)})
	if err != nil || !res.Created {
		t.Fatalf("deploy: %+v %v", res, err)
	}
	if got := getKey(t, base); !strings.Contains(got, "sk_1") {
		t.Fatalf("deployed function: %s", got)
	}

	// A second deploy migrates additively.
	res, err = deploy(c, DeployRequest{Project: shopJSON(t, true)})
	if err != nil || res.Created || len(res.Migrations) != 1 || !strings.Contains(res.Migrations[0], "price") {
		t.Fatalf("redeploy: %+v %v", res, err)
	}
}

func TestDeploy_Rejections(t *testing.T) {
	c, _, data := newEmptyCentral(t)
	ctx := context.Background()

	bad := shopJSON(t, false)
	bad["functions"] = map[string]any{"x": map[string]any{"route": map[string]any{"path": "/nope", "method": "GET"}, "steps": []any{}}}
	_, err := deploy(c, DeployRequest{Project: bad, AllowMissingSecrets: true})
	var rerr *remote.Error
	if !errors.As(err, &rerr) || rerr.Status != 422 || len(rerr.Issues) == 0 {
		t.Fatalf("invalid config: %v", err)
	}

	// The URL's tenant must be the project's.
	if err := c.Do(ctx, "PUT", "/admin/tenants/blog", DeployRequest{Project: shopJSON(t, false)}, nil); err == nil {
		t.Fatal("deploy under another tenant id accepted")
	}

	// Another project mapping to the same id is refused.
	if _, err := deploy(c, DeployRequest{Project: shopJSON(t, false), AllowMissingSecrets: true}); err != nil {
		t.Fatal(err)
	}
	other := shopJSON(t, false)
	other["name"] = "Shop!"
	if _, err := deploy(c, DeployRequest{Project: other, AllowMissingSecrets: true}); !errors.As(err, &rerr) || rerr.Status != 409 {
		t.Fatalf("clashing project: %v", err)
	}

	// The server's own .env (in its working directory) never fills a
	// deployed project's references.
	if err := os.WriteFile(filepath.Join(data, ".env"), []byte("STRIPE_KEY=leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(data)
	res, err := deploy(c, DeployRequest{Project: shopJSON(t, true), AllowMissingSecrets: true})
	if err != nil || len(res.MissingSecrets) != 1 {
		t.Fatalf("deploy: %+v %v", res, err)
	}
	raw, _ := os.ReadFile(filepath.Join(data, "tenants", "shop.json"))
	if strings.Contains(string(raw), "leak") {
		t.Fatal("server .env leaked into the tenant config")
	}
}
