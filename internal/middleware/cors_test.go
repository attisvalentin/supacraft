package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSPreflight(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	t.Run("mirrors origin and echoes requested headers", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodOptions, "/query", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Headers", "x-tenant-id, authorization")
		rec := httptest.NewRecorder()

		CORSPreflight(next).ServeHTTP(rec, req)

		if called {
			t.Fatalf("expected preflight to short-circuit, but next was called")
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("unexpected ACAO: %q", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "x-tenant-id, authorization" {
			t.Fatalf("unexpected ACAH: %q", got)
		}
	})

	t.Run("falls back to default headers when none requested", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodOptions, "/query", nil)
		req.Header.Set("Origin", "https://app.example.com")
		rec := httptest.NewRecorder()

		CORSPreflight(next).ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-Tenant-ID" {
			t.Fatalf("unexpected default ACAH: %q", got)
		}
	})

	t.Run("non-OPTIONS requests pass through untouched", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodPost, "/query", nil)
		rec := httptest.NewRecorder()

		CORSPreflight(next).ServeHTTP(rec, req)

		if !called {
			t.Fatalf("expected next to be called for non-OPTIONS request")
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected no ACAO header, got %q", got)
		}
	})
}

func TestCorsAllowOriginHeader(t *testing.T) {
	tests := []struct {
		name          string
		allowedOrigin string
		requestOrigin string
		want          string
	}{
		{name: "wildcard allows any origin", allowedOrigin: "*", requestOrigin: "https://evil.com", want: "*"},
		{name: "exact match echoes origin", allowedOrigin: "https://app.example.com", requestOrigin: "https://app.example.com", want: "https://app.example.com"},
		{name: "mismatch sets nothing", allowedOrigin: "https://app.example.com", requestOrigin: "https://evil.com", want: ""},
		{name: "unset allowed_origin sets nothing", allowedOrigin: "", requestOrigin: "https://app.example.com", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := corsAllowOriginHeader(tt.allowedOrigin, tt.requestOrigin); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
