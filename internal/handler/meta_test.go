package handler

import (
	"net/http"
	"testing"
)

func TestOpenAPIEndpoint(t *testing.T) {
	a := newTestAPI(t)
	code, out, raw := a.do("GET", "/meta/openapi.json", "")
	if code != http.StatusOK || out["openapi"] != "3.1.0" {
		t.Fatalf("%d %s", code, raw)
	}
	paths := out["paths"].(map[string]any)
	if paths["/fn/posts/delete"] == nil || paths["/query"] == nil {
		t.Fatalf("paths: %v", paths)
	}
	// The table schema itself is never served.
	if code, _, raw := a.do("GET", "/meta/schema", ""); code == http.StatusOK {
		t.Fatalf("/meta/schema is served: %s", raw)
	}
}

func TestOpenAPIEndpoint_CanBeTurnedOff(t *testing.T) {
	a := newTestAPIWith(t, `{"name": "demo", "api_settings": {"openapi": false}}`)
	if code, _, raw := a.do("GET", "/meta/openapi.json", ""); code != http.StatusNotFound {
		t.Fatalf("%d %s", code, raw)
	}
}
