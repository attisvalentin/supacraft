package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/attisvalentin/supacraft/internal/studiosign"
)

func TestSignedAuth_RejectsOversizedBody(t *testing.T) {
	secret := strings.Repeat("s", MinAdminSecretLen)
	reached := false
	h := AdminAuth(secret)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	body := []byte(strings.Repeat("x", studioMaxBody+10))
	// Sign only the first studioMaxBody bytes, as a truncating reader would see them.
	ts := time.Now().Unix()
	nonce := studiosign.NewNonce()
	req := httptest.NewRequest("PUT", "/admin/tenants/shop", strings.NewReader(string(body)))
	req.Header.Set(studiosign.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(studiosign.HeaderNonce, nonce)
	req.Header.Set(studiosign.HeaderSignature, studiosign.Sign(secret, ts, nonce, "PUT", "/admin/tenants/shop", body[:studioMaxBody]))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || reached {
		t.Fatalf("oversized body: %d, handler reached: %v", rec.Code, reached)
	}
}
