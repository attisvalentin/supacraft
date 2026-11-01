package handler

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/attisvalentin/supacraft/internal/storage"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// Every upload is recorded in the tenant database so a purge can delete it
// from remote storage; a deleted file is forgotten again.
func TestFileUpload_IsTracked(t *testing.T) {
	a := newTestAPIWith(t, `{"name": "demo", "api_settings": {"storage": {"driver": "local"}}}`)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "note.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	req := httptest.NewRequest("POST", "/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Tenant-ID", "demo")
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}

	db, err := sql.Open("sqlite", a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	paths, err := storage.TrackedUploads(t.Context(), db)
	if err != nil || len(paths) != 1 || paths[0].Destination != "local" {
		t.Fatalf("tracked: %v %v", paths, err)
	}

	if code, _, raw := a.do("DELETE", "/files/"+paths[0].Path, "", "X-Tenant-ID", "demo"); code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if paths, _ := storage.TrackedUploads(t.Context(), db); len(paths) != 0 {
		t.Fatalf("deleted file still tracked: %v", paths)
	}
}

// flakyDriver fails Put after leaving a partial file; Delete can fail too.
type flakyDriver struct {
	files      map[string]bool
	deleteFail bool
}

func (d *flakyDriver) Put(_ context.Context, p string, _ io.Reader, _ int64) error {
	d.files[p] = true
	return errors.New("connection reset")
}
func (d *flakyDriver) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (d *flakyDriver) Exists(_ context.Context, p string) (bool, error)   { return d.files[p], nil }
func (d *flakyDriver) Delete(_ context.Context, p string) error {
	if d.deleteFail {
		return errors.New("access denied")
	}
	delete(d.files, p)
	return nil
}

func TestForgetFailedUpload_KeepsRecordOfLeftoverFile(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &flakyDriver{files: map[string]bool{}, deleteFail: true}
	for _, p := range []string{"a.png", "b.png"} {
		if err := storage.TrackUpload(ctx, db, p, "s3:x"); err != nil {
			t.Fatal(err)
		}
		_ = d.Put(ctx, p, nil, 0)
		forgetFailedUpload(ctx, d, db, p, "s3:x")
		d.deleteFail = false
	}
	// a.png couldn't be deleted, so its record stays; b.png is gone.
	paths, _ := storage.TrackedUploads(ctx, db)
	if len(paths) != 1 || paths[0].Path != "a.png" || d.files["b.png"] {
		t.Fatalf("records %v, files %v", paths, d.files)
	}

	// A driver that hangs is abandoned and the record kept.
	cleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { cleanupTimeout = 30 * time.Second })
	hang := &hangingDriver{release: make(chan struct{})}
	defer close(hang.release)
	if err := storage.TrackUpload(ctx, db, "c.png", "s3:x"); err != nil {
		t.Fatal(err)
	}
	forgetFailedUpload(ctx, hang, db, "c.png", "s3:x")
	if paths, _ := storage.TrackedUploads(ctx, db); len(paths) != 2 {
		t.Fatalf("record of a file with unknown state dropped: %v", paths)
	}
}

// hangingDriver blocks every call until released (like FTP ignoring ctx).
type hangingDriver struct{ release chan struct{} }

func (d *hangingDriver) Put(context.Context, string, io.Reader, int64) error { <-d.release; return nil }
func (d *hangingDriver) Get(context.Context, string) (io.ReadCloser, error) {
	<-d.release
	return nil, nil
}
func (d *hangingDriver) Exists(context.Context, string) (bool, error) { <-d.release; return true, nil }
func (d *hangingDriver) Delete(context.Context, string) error         { <-d.release; return nil }

func TestFileUpload_RefusedOnceTenantDisabled(t *testing.T) {
	a := newTestAPIWith(t, `{"name": "demo", "api_settings": {"storage": {"driver": "local"}}}`)
	if err := tenant.SetDisabled(filepath.Dir(a.dbPath), "demo", true); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "note.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	// The cached config still says enabled; the upload checks the disk.
	req := httptest.NewRequest("POST", "/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Tenant-ID", "demo")
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("upload to a disabled tenant: %d %s", rec.Code, rec.Body)
	}
}

// purgingDriver simulates a purge from another process that disables the
// tenant while a file is being written.
type purgingDriver struct {
	dataDir string
	files   map[string]bool
}

func (d *purgingDriver) Put(_ context.Context, p string, _ io.Reader, _ int64) error {
	d.files[p] = true
	return tenant.SetDisabled(d.dataDir, "demo", true)
}
func (d *purgingDriver) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (d *purgingDriver) Exists(_ context.Context, p string) (bool, error)   { return d.files[p], nil }
func (d *purgingDriver) Delete(_ context.Context, p string) error {
	delete(d.files, p)
	return nil
}

func TestFileUpload_RemovesItsFileWhenPurgedMeanwhile(t *testing.T) {
	a := newTestAPIWith(t, `{"name": "demo", "api_settings": {"storage": {"driver": "local"}}}`)
	d := &purgingDriver{dataDir: filepath.Dir(a.dbPath), files: map[string]bool{}}
	newStorageDriver = func(string, map[string]any, string) (storage.StorageDriver, error) { return d, nil }
	t.Cleanup(func() { newStorageDriver = storage.NewDriver })

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "note.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	req := httptest.NewRequest("POST", "/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Tenant-ID", "demo")
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	if rec.Code != 503 || len(d.files) != 0 {
		t.Fatalf("upload during a purge: %d, files left %v", rec.Code, d.files)
	}
}
