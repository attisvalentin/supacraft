package tenant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTenantFile(t *testing.T, dataDir, id string) {
	t.Helper()
	dir := filepath.Join(dataDir, "tenants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"db_name": "` + id + `", "display_name": "` + id + `", "enabled": true}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{id + ".db", id + ".db-wal"} {
		if err := os.WriteFile(filepath.Join(dataDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDisableEnableRemove(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	storage := filepath.Join(data, "storage")
	writeTenantFile(t, data, "shop")
	writeTenantFile(t, data, "blog")
	if err := os.MkdirAll(filepath.Join(storage, "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{dir: filepath.Join(data, "tenants")}

	if err := SetDisabled(data, "shop", true); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.GetTenantConfig(ctx, "shop"); cfg.Enabled {
		t.Fatal("disabled tenant still enabled")
	}
	list, err := ListTenants(ctx, data)
	if err != nil || len(list) != 2 || list[0].ID != "blog" || !list[1].Disabled {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := SetDisabled(data, "shop", false); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.GetTenantConfig(ctx, "shop"); !cfg.Enabled {
		t.Fatal("re-enabled tenant still disabled")
	}
	if err := SetDisabled(data, "../tenants/shop", true); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("path traversal in disable: %v", err)
	}
	if err := SetDisabled(data, "nope", true); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("disable unknown tenant: %v", err)
	}

	// Without purge the database stays.
	if _, err := RemoveTenant(ctx, data, storage, "blog", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "blog.db")); err != nil {
		t.Fatal("remove without purge deleted the database")
	}

	if err := os.MkdirAll(filepath.Join(data, "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "env", "shop.env"), []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveTenant(ctx, data, storage, "shop", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"tenants/shop.json", "shop.db", "shop.db-wal", "storage/shop", "env/shop.env"} {
		if _, err := os.Stat(filepath.Join(data, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived purge", p)
		}
	}
	if _, err := RemoveTenant(ctx, data, storage, "../blog", true); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("path traversal not rejected: %v", err)
	}
}

func TestFileStore_DBNameMustMatchTenant(t *testing.T) {
	data := t.TempDir()
	writeTenantFile(t, data, "shop")
	// Point blog at shop's database.
	if err := os.WriteFile(filepath.Join(data, "tenants", "blog.json"), []byte(`{"db_name": "shop", "enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{dir: filepath.Join(data, "tenants")}
	if _, err := store.GetTenantConfig(context.Background(), "blog"); err == nil {
		t.Fatal("tenant sharing another tenant's database was accepted")
	}
	if _, err := RemoveTenant(context.Background(), data, "", "blog", true); err == nil {
		t.Fatal("purge of a tenant pointing at another database succeeded")
	}
	if _, err := os.Stat(filepath.Join(data, "shop.db")); err != nil {
		t.Fatal("shop's database was deleted")
	}
}
