package middleware

import (
	"net/http/httptest"
	"testing"
)

// OAuth redirects carry the tenant in the URL; other routes never do.
func TestOAuthTenant(t *testing.T) {
	for path, want := range map[string]string{
		"/auth/oauth/google/start?tenant=shop&redirect_to=x":   "shop",
		"/auth/oauth/google/callback?state=shop.abc123&code=c": "shop",
		"/auth/oauth/google/callback?state=nodot":              "",
		"/query?tenant=shop":                                   "",
		"/fn/x?state=shop.abc":                                 "",
	} {
		if got := oauthTenant(httptest.NewRequest("GET", path, nil)); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
