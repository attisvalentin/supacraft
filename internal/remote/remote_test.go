package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDo_DoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "PUT", "/admin/tenants/shop/secrets/K", map[string]string{"value": "secret"}, nil); err == nil {
		t.Fatal("redirect response treated as success")
	}
	if leaked.Load() {
		t.Fatal("signed request with the secret was resent to the redirect target")
	}
}
