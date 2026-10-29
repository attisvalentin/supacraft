package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// studioMaxBody caps the request body the studio HMAC middleware will buffer.
const studioMaxBody = 1 << 20 // 1 MiB

// studioSkewSeconds is the allowed clock skew between the operator's CLI and the
// API server when validating the signed timestamp (replay protection).
const studioSkewSeconds = 300

// StudioAuth verifies the operator HMAC on /admin/studio requests. It mirrors the
// log-ingestion signing scheme: X-Signature = "sha256=" + hex(HMAC-SHA256(secret,
// "<unix-ts>.<raw-body>")), with the timestamp in X-Timestamp. When secret is
// empty the endpoints are disabled (every request is rejected).
func StudioAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				writeStudioErr(w, http.StatusForbidden, "studio endpoints are disabled (STUDIO_SECRET not set)")
				return
			}

			sig := r.Header.Get("X-Signature")
			tsStr := r.Header.Get("X-Timestamp")
			if sig == "" || tsStr == "" {
				writeStudioErr(w, http.StatusUnauthorized, "missing signature or timestamp")
				return
			}
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				writeStudioErr(w, http.StatusUnauthorized, "invalid timestamp")
				return
			}
			if diff := time.Now().Unix() - ts; diff > studioSkewSeconds || diff < -studioSkewSeconds {
				writeStudioErr(w, http.StatusUnauthorized, "stale or future timestamp")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, studioMaxBody))
			if err != nil {
				writeStudioErr(w, http.StatusBadRequest, "cannot read body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			expected := studioSign(secret, ts, body)
			if !hmac.Equal([]byte(sig), []byte(expected)) {
				writeStudioErr(w, http.StatusUnauthorized, "invalid signature")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func studioSign(secret string, ts int64, body []byte) string {
	payload := strconv.FormatInt(ts, 10) + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func writeStudioErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
