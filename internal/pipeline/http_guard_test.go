package pipeline

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTemplatedHost(t *testing.T) {
	for url, want := range map[string]bool{
		"https://api.example.com/users/{{request.id}}": false,
		"https://api.example.com/?q={{request.q}}":     false,
		"https://{{request.host}}/x":                   true,
		"https://api.{{env.REGION}}.example.com/":      true,
		"{{request.url}}":                              true,
		"{{env.SCHEME}}://api.example.com/":            true,
	} {
		if got := templatedHost(url); got != want {
			t.Errorf("templatedHost(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestGuardedClients(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// A host fixed in config may be on the local machine.
	resp, err := httpClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("fixed-host client blocked loopback: %v", err)
	}
	resp.Body.Close()

	// A templated host may not.
	if _, err := strictHTTPClient.Get(srv.URL); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("strict client reached loopback: %v", err)
	}

	// Cloud metadata is never reachable.
	if _, err := httpClient.Get("http://169.254.169.254/latest/meta-data/"); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("metadata address not blocked: %v", err)
	}
}
