package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/remote"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

func keyStatus(t *testing.T, base string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/fn/key", nil)
	req.Header.Set("X-Tenant-ID", "shop")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAdminTenants_ListDisableErrorsRemove(t *testing.T) {
	srv, data := newCentralAPI(t, adminSecret, true)
	ctx := context.Background()
	c, err := remote.New(srv.URL, adminSecret)
	if err != nil {
		t.Fatal(err)
	}

	var list struct {
		Data []tenant.TenantInfo `json:"data"`
	}
	if err := c.Do(ctx, "GET", "/admin/tenants", nil, &list); err != nil || len(list.Data) != 1 || list.Data[0].ID != "shop" {
		t.Fatalf("list: %+v %v", list, err)
	}

	if err := c.Do(ctx, "POST", "/admin/tenants/shop/disable", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := keyStatus(t, srv.URL); got != http.StatusServiceUnavailable {
		t.Fatalf("disabled tenant answered %d", got)
	}
	if err := c.Do(ctx, "POST", "/admin/tenants/shop/enable", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := keyStatus(t, srv.URL); got != http.StatusOK {
		t.Fatalf("enabled tenant answered %d", got)
	}
	var rerr *remote.Error
	if err := c.Do(ctx, "POST", "/admin/tenants/nope/disable", nil, nil); err == nil || !errors.As(err, &rerr) || rerr.Status != http.StatusNotFound {
		t.Fatalf("disable unknown tenant: %v", err)
	}

	// Error messages are served with the tenant's secret values masked.
	if err := c.Do(ctx, "PUT", "/admin/tenants/shop/secrets/STRIPE_KEY", map[string]string{"value": "sk_live_secret"}, nil); err != nil {
		t.Fatal(err)
	}
	logDB, err := sql.Open("sqlite", filepath.Join(data, "errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logDB.Close()
	for _, row := range [][]string{{"shop", "upstream rejected key sk_live_secret"}, {"blog", "other tenant"}} {
		if _, err := logDB.Exec(`INSERT INTO error_logs (tenant_id, method, path, status_code, error_message) VALUES (?, 'GET', '/fn/pay', 500, ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	var errs struct {
		Data  []errorlog.LogEntry `json:"data"`
		Total int                 `json:"total"`
	}
	if err := c.Do(ctx, "GET", "/admin/tenants/shop/errors?limit=10", nil, &errs); err != nil {
		t.Fatal(err)
	}
	if errs.Total != 1 || len(errs.Data) != 1 || errs.Data[0].ErrorMessage != "upstream rejected key [secret STRIPE_KEY]" {
		t.Fatalf("errors: %+v", errs)
	}

	// Without purge the database stays; with it everything goes.
	if err := c.Do(ctx, "DELETE", "/admin/tenants/shop", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "shop.db")); err != nil {
		t.Fatal("remove without purge deleted the database")
	}
	if got := keyStatus(t, srv.URL); got == http.StatusOK {
		t.Fatal("removed tenant still served")
	}
	if err := c.Do(ctx, "DELETE", "/admin/tenants/shop?purge=true", nil, nil); err == nil || !errors.As(err, &rerr) || rerr.Status != http.StatusNotFound {
		t.Fatalf("purge of a removed tenant: %v", err)
	}
}

func TestAdminTenants_PurgeDeletesData(t *testing.T) {
	srv, data := newCentralAPI(t, adminSecret, true)
	ctx := context.Background()
	c, _ := remote.New(srv.URL, adminSecret)
	if err := c.Do(ctx, "PUT", "/admin/tenants/shop/secrets/STRIPE_KEY", map[string]string{"value": "sk_live_1"}, nil); err != nil {
		t.Fatal(err)
	}
	// Open the tenant's pool, so the purge has to close it.
	if got := keyStatus(t, srv.URL); got != http.StatusOK {
		t.Fatalf("tenant answered %d", got)
	}
	var res tenant.RemoveResult
	if err := c.Do(ctx, "DELETE", "/admin/tenants/shop?purge=true", nil, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Purged || res.ID != "shop" {
		t.Fatalf("result: %+v", res)
	}
	for _, p := range []string{"tenants/shop.json", "tenants/shop.disabled", "shop.db", "env/shop.json"} {
		if _, err := os.Stat(filepath.Join(data, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived the purge", p)
		}
	}
	// A request afterwards must not recreate the database.
	keyStatus(t, srv.URL)
	if _, err := os.Stat(filepath.Join(data, "shop.db")); !os.IsNotExist(err) {
		t.Fatal("database recreated after the purge")
	}
}

func TestMaskSecrets(t *testing.T) {
	vals := map[string]string{"A": "abcd", "AB": "abcdef", "SHORT": "xy"}
	got := MaskSecrets("abcdef abcd xy", vals)
	if got != "[secret AB] [secret A] xy" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(MaskSecrets("k=abcdef", vals), "abcdef") {
		t.Fatal("value not masked")
	}
}
