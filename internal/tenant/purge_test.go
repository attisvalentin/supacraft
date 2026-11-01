package tenant

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/attisvalentin/supacraft/internal/storage"
)

// fakeBucket is a remote storage driver backed by a map.
type fakeBucket struct {
	files   map[string]bool
	failing string // Delete of this path fails and the file stays
}

func (b *fakeBucket) Put(context.Context, string, io.Reader, int64) error { return nil }
func (b *fakeBucket) Get(context.Context, string) (io.ReadCloser, error)  { return nil, nil }
func (b *fakeBucket) Exists(_ context.Context, p string) (bool, error)    { return b.files[p], nil }
func (b *fakeBucket) Delete(_ context.Context, p string) error {
	if p == b.failing {
		return errors.New("access denied")
	}
	if !b.files[p] {
		return errors.New("no such key")
	}
	delete(b.files, p)
	return nil
}

func TestRemoveTenant_PurgesTrackedRemoteFiles(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "tenants"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"db_name": "shop", "enabled": true, "api_settings": {"storage": {"driver": "s3", "config": {"bucket": "b"}}}}`
	if err := os.WriteFile(filepath.Join(data, "tenants", "shop.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	// gone.png was already deleted from the bucket by hand; old.png went
	// to the bucket the tenant used before.
	dest := storage.Destination("s3", map[string]any{"bucket": "b"})
	for _, p := range []string{"a.png", "docs/b.pdf", "gone.png"} {
		if err := storage.TrackUpload(ctx, db, p, dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.TrackUpload(ctx, db, "old.png", storage.Destination("s3", map[string]any{"bucket": "old"})); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Another app's file in the same bucket is never touched.
	bucket := &fakeBucket{files: map[string]bool{"a.png": true, "docs/b.pdf": true, "other-app.txt": true}, failing: "docs/b.pdf"}
	newStorageDriver = func(name string, _ map[string]any, _ string) (storage.StorageDriver, error) {
		if name != "s3" {
			t.Fatalf("driver %q", name)
		}
		return bucket, nil
	}
	t.Cleanup(func() { newStorageDriver = storage.NewDriver })

	// A failing delete stops the purge before anything local is deleted,
	// and leaves the tenant disabled.
	if _, err := RemoveTenant(ctx, data, "", "shop", true); err == nil {
		t.Fatal("purge succeeded although a file could not be deleted")
	}
	for _, p := range []string{"shop.db", "tenants/shop.json", "tenants/shop.disabled"} {
		if _, err := os.Stat(filepath.Join(data, p)); err != nil {
			t.Errorf("%s missing after a failed purge: %v", p, err)
		}
	}

	// The retry finishes the job.
	bucket.failing = ""
	res, err := RemoveTenant(ctx, data, "", "shop", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesDriver != "s3" || res.FilesDeleted != 3 || res.FilesLeft != 1 || len(res.FilesLeftIn) != 1 || res.FilesLeftIn[0] != "s3" {
		t.Fatalf("result: %+v", res)
	}
	if len(bucket.files) != 1 || !bucket.files["other-app.txt"] {
		t.Fatalf("bucket after purge: %v", bucket.files)
	}
	for _, p := range []string{"shop.db", "tenants/shop.json", "tenants/shop.disabled"} {
		if _, err := os.Stat(filepath.Join(data, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived the purge", p)
		}
	}
}
