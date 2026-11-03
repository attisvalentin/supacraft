package jobs

import (
	"context"
	"crypto/hmac"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestDeliver(t *testing.T) {
	var got *http.Request
	var body []byte
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		if status == http.StatusFound {
			http.Redirect(w, r, "https://elsewhere.example.com/", status)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte("nope"))
	}))
	defer srv.Close()

	cfg := &tenant.TenantConfig{APISettings: tenant.APISettings{Webhooks: map[string]tenant.Webhook{
		"erp":      {URL: srv.URL + "/hook", Events: []string{"orders.*"}, Secret: "whsec"},
		"unsigned": {URL: srv.URL + "/hook", Events: []string{"orders.*"}},
	}}}
	job := tenant.Job{Function: tenant.WebhookJobPrefix + "erp", Attempts: 2,
		Args: map[string]any{"id": "evt-1", "event": "orders.insert", "rows": []any{map[string]any{"id": "o1"}}}}

	if err := deliver(context.Background(), cfg, "erp", job); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/hook" || got.Header.Get("X-SupaCraft-Event") != "orders.insert" ||
		got.Header.Get("X-SupaCraft-Delivery") != "evt-1" || got.Header.Get("X-SupaCraft-Attempt") != "2" {
		t.Fatalf("request: %s %s %v", got.Method, got.URL, got.Header)
	}
	// The receiver can verify the signature.
	sig := got.Header.Get("X-SupaCraft-Signature")
	tsPart, _, _ := strings.Cut(strings.TrimPrefix(sig, "t="), ",")
	ts, _ := strconv.ParseInt(tsPart, 10, 64)
	if !hmac.Equal([]byte(sig), []byte(Signature("whsec", ts, body))) || !strings.Contains(string(body), `"o1"`) {
		t.Fatalf("signature %q does not verify for %s", sig, body)
	}

	if err := deliver(context.Background(), cfg, "unsigned", job); err != nil || got.Header.Get("X-SupaCraft-Signature") != "" {
		t.Fatalf("unsigned delivery: %v %v", err, got.Header)
	}
	status = http.StatusInternalServerError
	if err := deliver(context.Background(), cfg, "erp", job); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("a 500 counted as delivered: %v", err)
	}
	status = http.StatusFound
	if err := deliver(context.Background(), cfg, "erp", job); err == nil {
		t.Fatal("a redirect counted as delivered")
	}
	if err := deliver(context.Background(), cfg, "gone", job); err == nil {
		t.Fatal("a removed webhook was delivered")
	}
}
