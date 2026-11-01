package handler

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/attisvalentin/supacraft/internal/storage"
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
	if err != nil || len(paths) != 1 {
		t.Fatalf("tracked: %v %v", paths, err)
	}

	if code, _, raw := a.do("DELETE", "/files/"+paths[0], "", "X-Tenant-ID", "demo"); code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if paths, _ := storage.TrackedUploads(t.Context(), db); len(paths) != 0 {
		t.Fatalf("deleted file still tracked: %v", paths)
	}
}
