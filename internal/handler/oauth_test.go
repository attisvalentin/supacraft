package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/attisvalentin/supacraft/internal/oauth"
)

// fakeProvider is an OpenID Connect provider (and GitHub's API) that
// signs in whoever the test says, checking PKCE like a real one.
type fakeProvider struct {
	*httptest.Server
	mu         sync.Mutex
	challenges map[string]string // code → code_challenge
	userinfo   map[string]any    // what /userinfo answers
	ghEmails   []map[string]any  // what GitHub's /user/emails answers
}

func newFakeProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{challenges: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize",
			"token_endpoint": f.URL + "/token", "userinfo_endpoint": f.URL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		challenge := f.challenges[r.Form.Get("code")]
		delete(f.challenges, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at","token_type":"Bearer"}`))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(f.userinfo) })
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id": 4242}`)) })
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(f.ghEmails) })
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// approve plays the user at the provider: it reads the authorization URL
// and returns the callback query the provider would redirect to.
func (f *fakeProvider) approve(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("no PKCE in %s", authURL)
	}
	code := "code-" + q.Get("state")[len(q.Get("state"))-8:]
	f.mu.Lock()
	f.challenges[code] = q.Get("code_challenge")
	f.mu.Unlock()
	return url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

const oauthProject = `{
  "name": "demo",
  "auth_enabled": true,
  "api_settings": {"auth": {"oauth": {
    "public_url": "https://api.example.com",
    "redirect_urls": ["https://app.example.com/done"],
    "providers": {
      "sso": {"issuer": "ISSUER", "client_id": "cid", "client_secret": "secret"},
      "github": {"client_id": "gid", "client_secret": "gsecret"}
    }
  }}},
  "functions": {"me": {"route": {"path": "/fn/me", "method": "GET", "auth_required": true},
                       "steps": [{"type": "response", "data": {"email": "{{auth.email}}"}}]}}
}`

// appVerifier is the app's PKCE verifier in the tests that don't care;
// startQuery carries a verifier's challenge.
const appVerifier = "app-verifier-0123456789-0123456789-0123456789"

func startQuery(extra string) string { return startQueryFor(appVerifier, extra) }

func startQueryFor(verifier, extra string) string {
	return "redirect_to=" + url.QueryEscape("https://app.example.com/done") + "&code_challenge=" + pkceChallenge(verifier) + "&code_challenge_method=S256" + extra
}

// signIn runs start → provider → callback as a browser with its own
// verifier, and returns where it lands at the app plus that verifier.
func signIn(t *testing.T, a *testAPI, f *fakeProvider, provider string) url.Values {
	t.Helper()
	back, _ := signInWith(t, a, f, provider, randomHex(24))
	return back
}

func signInWith(t *testing.T, a *testAPI, f *fakeProvider, provider, verifier string) (url.Values, string) {
	t.Helper()
	rec := a.raw("GET", "/auth/oauth/"+provider+"/start?"+startQueryFor(verifier, ""), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	rec = a.raw("GET", "/auth/oauth/"+provider+"/callback?"+f.approve(t, rec.Header().Get("Location")), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	back, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || back.Host != "app.example.com" {
		t.Fatalf("callback went to %q", rec.Header().Get("Location"))
	}
	q := back.Query()
	q.Set("verifier", verifier)
	return q, verifier
}

// rawClients spreads raw requests over client addresses: a sign-in takes
// two /auth requests, and /auth allows 20 a minute per address.
var rawClients atomic.Int32

func (a *testAPI) raw(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", rawClients.Add(1)%250+1)
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func TestOAuth(t *testing.T) {
	f := newFakeProvider(t)
	a := newTestAPIWith(t, strings.Replace(oauthProject, "ISSUER", f.URL, 1))
	exchange := func(back url.Values) map[string]any {
		t.Helper()
		status, out, raw := a.do("POST", "/auth/oauth/exchange", `{"code":"`+back.Get("code")+`","code_verifier":"`+back.Get("verifier")+`"}`)
		if status != http.StatusOK {
			t.Fatalf("exchange: %d %s", status, raw)
		}
		return out["data"].(map[string]any)
	}

	// A new account creates a user who can call auth-only routes.
	f.userinfo = map[string]any{"sub": "u-1", "email": "new@example.com", "email_verified": true}
	back := signIn(t, a, f, "sso")
	tokens := exchange(back)
	if status, out, raw := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+tokens["token"].(string)); status != http.StatusOK || out["email"] != "new@example.com" {
		t.Fatalf("me: %d %s", status, raw)
	}
	// The code and the state work once.
	if status, _, _ := a.do("POST", "/auth/oauth/exchange", `{"code":"`+back.Get("code")+`","code_verifier":"`+back.Get("verifier")+`"}`); status != http.StatusBadRequest {
		t.Fatalf("code reused: %d", status)
	}
	// Login CSRF: the attacker's code, carried to the victim's browser,
	// fails with the verifier of the victim's own sign-in.
	f.userinfo = map[string]any{"sub": "attacker-1", "email": "mallory@example.com", "email_verified": true}
	attacker, _ := signInWith(t, a, f, "sso", "attackers-verifier-0123456789-0123456789-0123")
	f.userinfo = map[string]any{"sub": "victim-1", "email": "victim@example.com", "email_verified": true}
	victim, _ := signInWith(t, a, f, "sso", "victims-verifier-0123456789-0123456789-012345")
	if status, _, _ := a.do("POST", "/auth/oauth/exchange", `{"code":"`+attacker.Get("code")+`","code_verifier":"`+victim.Get("verifier")+`"}`); status != http.StatusBadRequest {
		t.Fatalf("a code was exchanged with another sign-in's verifier: %d", status)
	}
	exchange(victim)
	// An OAuth-only user has no password to log in with.
	if status, _, _ := a.do("POST", "/auth/login", `{"email":"new@example.com","password":"!oauth"}`); status != http.StatusUnauthorized {
		t.Fatalf("password login for an OAuth user: %d", status)
	}

	// A password user is linked when the provider verified the email; as
	// ann never verified hers, whoever registered it loses the password
	// and every session (account pre-hijacking).
	id := a.register("ann@example.com")
	preSession := a.login("ann@example.com")
	f.userinfo = map[string]any{"sub": "u-2", "email": "ann@example.com", "email_verified": "true"}
	exchange(signIn(t, a, f, "sso"))
	if status, _, _ := a.do("POST", "/auth/login", `{"email":"ann@example.com","password":"Secret123!"}`); status != http.StatusUnauthorized {
		t.Fatalf("the pre-registered password still works: %d", status)
	}
	if status, _, _ := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+preSession["token"].(string)); status != http.StatusUnauthorized {
		t.Fatalf("a session from before the link still works: %d", status)
	}
	// The next sign-in finds the link, whatever the email says now.
	f.userinfo = map[string]any{"sub": "u-2", "email": "changed@example.com", "email_verified": false}
	tokens = exchange(signIn(t, a, f, "sso"))
	if _, out, _ := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+tokens["token"].(string)); out["email"] != "ann@example.com" {
		t.Fatalf("linked account signed in as %v (user %s)", out["email"], id)
	}

	// An unverified email creates no account (it could be someone else's)...
	f.userinfo = map[string]any{"sub": "u-3", "email": "carol@example.com", "email_verified": false}
	if back := signIn(t, a, f, "sso"); back.Get("error") != "email_not_verified" {
		t.Fatalf("unverified new account: %v", back)
	}
	// ...and never takes over an existing one.
	a.register("bob@example.com")
	f.userinfo = map[string]any{"sub": "attacker", "email": "bob@example.com", "email_verified": false}
	if back := signIn(t, a, f, "sso"); back.Get("error") != "email_not_verified" || back.Get("code") != "" {
		t.Fatalf("unverified email linked: %v", back)
	}

	// GitHub: the primary email and its verified flag.
	oauth.GitHubAuthURL, oauth.GitHubTokenURL, oauth.GitHubAPI = f.URL+"/authorize", f.URL+"/token", f.URL
	t.Cleanup(func() {
		oauth.GitHubAuthURL, oauth.GitHubTokenURL, oauth.GitHubAPI = "https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com"
	})
	f.ghEmails = []map[string]any{{"email": "other@example.com", "primary": false, "verified": true}, {"email": "gh@example.com", "primary": true, "verified": true}}
	tokens = exchange(signIn(t, a, f, "github"))
	if _, out, _ := a.do("GET", "/fn/me", "", "Authorization", "Bearer "+tokens["token"].(string)); out["email"] != "gh@example.com" {
		t.Fatalf("github user: %v", out)
	}
}

func TestOAuth_Refusals(t *testing.T) {
	f := newFakeProvider(t)
	a := newTestAPIWith(t, strings.Replace(oauthProject, "ISSUER", f.URL, 1))

	// Only listed redirect URLs, exactly, and the app's PKCE is required.
	for _, to := range []string{"https://evil.example.com/done", "https://app.example.com/done?x=1", ""} {
		if rec := a.raw("GET", "/auth/oauth/sso/start?code_challenge="+pkceChallenge(appVerifier)+"&code_challenge_method=S256&redirect_to="+url.QueryEscape(to), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("redirect_to %q: %d", to, rec.Code)
		}
	}
	if rec := a.raw("GET", "/auth/oauth/sso/start?redirect_to="+url.QueryEscape("https://app.example.com/done"), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("start without code_challenge: %d", rec.Code)
	}
	if rec := a.raw("GET", "/auth/oauth/nope/start?"+startQuery(""), ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider: %d", rec.Code)
	}

	// A state is good once, for its own provider only.
	rec := a.raw("GET", "/auth/oauth/sso/start?"+startQuery(""), "")
	query := f.approve(t, rec.Header().Get("Location"))
	if rec := a.raw("GET", "/auth/oauth/github/callback?"+query, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("state used at another provider: %d", rec.Code)
	}
	if rec := a.raw("GET", "/auth/oauth/sso/callback?"+query, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("state reused: %d", rec.Code)
	}

	// The user saying no at the provider ends at the app with an error.
	rec = a.raw("GET", "/auth/oauth/sso/start?"+startQuery(""), "")
	state, _ := url.Parse(rec.Header().Get("Location"))
	rec = a.raw("GET", "/auth/oauth/sso/callback?error=access_denied&state="+url.QueryEscape(state.Query().Get("state")), "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "https://app.example.com/done?error=access_denied") {
		t.Errorf("denied: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Without OAuth configured the endpoints don't exist.
	plain := newTestAPIWith(t, `{"name": "plain", "auth_enabled": true}`)
	if status, _, _ := plain.do("POST", "/auth/oauth/exchange", `{"code":"x"}`); status != http.StatusNotFound {
		t.Errorf("exchange without oauth: %d", status)
	}
}

// On a central server the browser can't send X-Tenant-ID: start takes
// ?tenant= and the callback finds the tenant in the state.
func TestOAuth_CentralServer(t *testing.T) {
	f := newFakeProvider(t)
	a := newTestAPIServing(t, strings.Replace(oauthProject, "ISSUER", f.URL, 1), false)
	f.userinfo = map[string]any{"sub": "c-1", "email": "c@example.com", "email_verified": true}

	rec := a.raw("GET", "/auth/oauth/sso/start?"+startQuery("&tenant=demo"), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	query := f.approve(t, rec.Header().Get("Location"))
	if !strings.Contains(query, "state=demo.") {
		t.Fatalf("state has no tenant prefix: %s", query)
	}
	rec = a.raw("GET", "/auth/oauth/sso/callback?"+query, "")
	back, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || back.Query().Get("code") == "" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	status, _, raw := a.do("POST", "/auth/oauth/exchange", `{"code":"`+back.Query().Get("code")+`","code_verifier":"`+appVerifier+`"}`, "X-Tenant-ID", "demo")
	if status != http.StatusOK {
		t.Fatalf("exchange: %d %s", status, raw)
	}
}
