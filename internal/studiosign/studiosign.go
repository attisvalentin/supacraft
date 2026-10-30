// Package studiosign is the request-signing scheme shared by the studio proxy
// client (internal/studio) and the API's /admin/studio middleware.
//
//	X-Signature = "sha256=" + hex(HMAC-SHA256(secret,
//	    "<unix-ts>.<nonce>.<METHOD>.<request-uri>.<body>"))
//
// The timestamp bounds how long a captured request stays valid, the nonce
// makes every request single-use, and binding the method and URI stops a
// signature from being replayed against another endpoint.
package studiosign

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

const (
	HeaderSignature = "X-Signature"
	HeaderTimestamp = "X-Timestamp"
	HeaderNonce     = "X-Nonce"

	// MaxSkew is the accepted clock difference between client and server.
	MaxSkew = 5 * time.Minute
)

// Sign returns the X-Signature value for a request.
func Sign(secret string, ts int64, nonce, method, requestURI string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "." + nonce + "." + method + "." + requestURI + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// NewNonce returns a random single-use request id.
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ValidNonce reports whether n has the shape NewNonce produces.
func ValidNonce(n string) bool {
	if len(n) != 32 {
		return false
	}
	_, err := hex.DecodeString(n)
	return err == nil
}

// maxRemembered bounds memory; beyond it new requests are refused until old
// nonces expire, which only a flood of validly signed requests can cause.
const maxRemembered = 100_000

// NonceCache remembers nonces for twice MaxSkew (a timestamp is accepted up
// to MaxSkew in either direction), so each signed request is accepted once.
type NonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewNonceCache() *NonceCache {
	return &NonceCache{seen: map[string]time.Time{}}
}

// Use records nonce and reports whether it was unused.
func (c *NonceCache) Use(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if exp, ok := c.seen[nonce]; ok && now.Before(exp) {
		return false
	}
	if len(c.seen) >= maxRemembered {
		for n, exp := range c.seen {
			if !now.Before(exp) {
				delete(c.seen, n)
			}
		}
		if len(c.seen) >= maxRemembered {
			return false
		}
	}
	c.seen[nonce] = now.Add(2 * MaxSkew)
	return true
}
