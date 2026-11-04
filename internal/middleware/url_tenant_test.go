package middleware

import (
	"net/http/httptest"
	"testing"
)

// OAuth redirects and WebSockets carry the tenant in the URL; other routes
// never do.
func TestURLTenant(t *testing.T) {
	for path, want := range map[string]string{
		"/auth/oauth/google/start?tenant=shop&redirect_to=x":   "shop",
		"/auth/oauth/google/callback?state=shop.abc123&code=c": "shop",
		"/auth/oauth/google/callback?state=nodot":              "",
		"/query?tenant=shop":                                   "",
		"/fn/x?state=shop.abc":                                 "",
		"/realtime?tenant=shop":                                "shop",
		"/realtime?state=shop.abc":                             "",
	} {
		if got := urlTenant(httptest.NewRequest("GET", path, nil)); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
