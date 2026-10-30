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

func TestStudioAuth(t *testing.T) {
	const secret = "s3cret"
	h := StudioAuth(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	type req struct {
		method, uri, signURI, body, nonce string
		ts                                int64
	}
	send := func(q req) int {
		r := httptest.NewRequest(q.method, q.uri, strings.NewReader(q.body))
		r.Header.Set(studiosign.HeaderTimestamp, strconv.FormatInt(q.ts, 10))
		r.Header.Set(studiosign.HeaderNonce, q.nonce)
		r.Header.Set(studiosign.HeaderSignature, studiosign.Sign(secret, q.ts, q.nonce, q.method, q.signURI, []byte(q.body)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	now := time.Now().Unix()
	ok := req{"GET", "/admin/studio/tenants", "/admin/studio/tenants", "", studiosign.NewNonce(), now}

	if c := send(ok); c != http.StatusOK {
		t.Fatalf("valid request: %d", c)
	}
	if c := send(ok); c != http.StatusUnauthorized {
		t.Fatalf("replayed request: %d", c)
	}

	other := ok
	other.nonce = studiosign.NewNonce()
	other.uri = "/admin/studio/tenants/demo/schema"
	if c := send(other); c != http.StatusUnauthorized {
		t.Fatalf("signature reused on another path: %d", c)
	}

	stale := ok
	stale.nonce = studiosign.NewNonce()
	stale.ts = now - int64(studiosign.MaxSkew/time.Second) - 10
	if c := send(stale); c != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: %d", c)
	}

	noNonce := ok
	noNonce.nonce = ""
	if c := send(noNonce); c != http.StatusUnauthorized {
		t.Fatalf("missing nonce: %d", c)
	}
}
