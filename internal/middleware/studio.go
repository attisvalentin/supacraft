package middleware

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/attisvalentin/supacraft/internal/studiosign"
)

// studioMaxBody caps the request body the studio HMAC middleware will buffer.
const studioMaxBody = 1 << 20 // 1 MiB

// StudioAuth verifies the operator signature on /admin/studio requests (see
// internal/studiosign): a fresh timestamp, an unused nonce, and an HMAC over
// both plus the method, URI and body. When secret is empty the endpoints are
// disabled (every request is rejected).
func StudioAuth(secret string) func(http.Handler) http.Handler {
	return signedAuth(secret, "studio endpoints are disabled (STUDIO_SECRET not set)")
}

// MinAdminSecretLen is the shortest SUPACRAFT_ADMIN_SECRET accepted: it
// guards writes to every tenant of a server, so it must not be guessable.
const MinAdminSecretLen = 32

// AdminAuth verifies signed requests to /admin/tenants (the same scheme as
// the studio, keyed by SUPACRAFT_ADMIN_SECRET). A missing or short secret
// disables the endpoints.
func AdminAuth(secret string) func(http.Handler) http.Handler {
	if len(secret) < MinAdminSecretLen {
		secret = ""
	}
	return signedAuth(secret, "admin endpoints are disabled (set SUPACRAFT_ADMIN_SECRET to at least 32 characters)")
}

func signedAuth(secret, disabledMsg string) func(http.Handler) http.Handler {
	nonces := studiosign.NewNonceCache()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				writeStudioErr(w, http.StatusForbidden, disabledMsg)
				return
			}

			sig := r.Header.Get(studiosign.HeaderSignature)
			tsStr := r.Header.Get(studiosign.HeaderTimestamp)
			nonce := r.Header.Get(studiosign.HeaderNonce)
			if sig == "" || tsStr == "" || !studiosign.ValidNonce(nonce) {
				writeStudioErr(w, http.StatusUnauthorized, "missing signature, timestamp or nonce")
				return
			}
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				writeStudioErr(w, http.StatusUnauthorized, "invalid timestamp")
				return
			}
			now := time.Now()
			if d := now.Sub(time.Unix(ts, 0)); d > studiosign.MaxSkew || d < -studiosign.MaxSkew {
				writeStudioErr(w, http.StatusUnauthorized, "stale or future timestamp")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, studioMaxBody))
			if err != nil {
				writeStudioErr(w, http.StatusBadRequest, "cannot read body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			expected := studiosign.Sign(secret, ts, nonce, r.Method, r.URL.RequestURI(), body)
			if !hmac.Equal([]byte(sig), []byte(expected)) {
				writeStudioErr(w, http.StatusUnauthorized, "invalid signature")
				return
			}
			// Only a correctly signed request consumes its nonce, so garbage
			// requests cannot fill the cache.
			if !nonces.Use(nonce, now) {
				writeStudioErr(w, http.StatusUnauthorized, "request already used")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeStudioErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
