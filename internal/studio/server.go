package studio

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Server serves the studio JSON API plus the embedded web UI.
type Server struct {
	src          DataSource
	mux          *http.ServeMux
	secret       string
	loginLimiter *loginLimiter
}

// NewServer wires the routes for the given data source. secret gates access
// to the data routes via a login-issued session cookie (or a Basic Auth
// header, kept for curl/scripts) — the same STUDIO_SECRET also signs
// outbound requests in --proxy mode (proxy_source.go), so changing it
// affects both paths.
func NewServer(src DataSource, secret string) *Server {
	s := &Server{src: src, mux: http.NewServeMux(), secret: secret, loginLimiter: newLoginLimiter()}

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/tenants", s.requireAuth(s.handleTenants))
	s.mux.HandleFunc("GET /api/tenants/{id}/schema", s.requireAuth(s.handleSchema))
	s.mux.HandleFunc("GET /api/tenants/{id}/tables/{table}/rows", s.requireAuth(s.handleRows))

	sub, _ := fs.Sub(webFS, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	return s
}

// Handler exposes the mux for http.Server. Auth is enforced per-route (see
// requireAuth) rather than wrapped here, since the SPA shell, /healthz, and
// the login/logout endpoints must stay reachable without credentials.
func (s *Server) Handler() http.Handler { return s.mux }

const (
	sessionCookieName = "supacraft_studio_session"
	sessionTTL        = 30 * 24 * time.Hour
)

// sessionToken derives a token bound to a specific expiry, so a captured
// cookie value stops working once exp passes — rotating STUDIO_SECRET is
// still the only way to invalidate it before then (there's no server-side
// session store to revoke individual logins early).
func sessionToken(secret string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("supacraft-studio-session|" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func validSessionCookie(secret, value string) bool {
	expStr, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sig), []byte(sessionToken(secret, exp))) == 1
}

// authorized accepts only a valid session cookie. A Basic Auth fallback was
// tried here and removed: browsers cache Basic Auth credentials per-origin
// independent of cookies, so a browser that had ever authenticated via the
// old native-popup flow would silently keep re-authenticating on every
// request — including right after an explicit logout, making logout a
// no-op. Cookie-only auth means logout (which only clears the cookie)
// actually logs you out. It never sends WWW-Authenticate, which is the
// literal trigger for the browser's native auth popup — unauthorized
// requests just get a plain JSON 401.
func (s *Server) authorized(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	return err == nil && validSessionCookie(s.secret, c.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

const (
	maxLoginFailures = 5
	loginLockout     = 3 * time.Minute
)

type loginAttempt struct {
	failures  int
	lockUntil time.Time
}

// loginLimiter tracks failed Studio login attempts per client IP, purely
// in-memory — this is a per-invocation CLI subcommand, not a long-lived
// service, so there's no need for restart persistence.
type loginLimiter struct {
	mu   sync.Mutex
	byIP map[string]*loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: make(map[string]*loginAttempt)}
}

// allow reports whether ip may attempt a login right now, and if not, how
// much longer the lockout lasts.
func (l *loginLimiter) allow(ip string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.byIP[ip]
	if a == nil || !time.Now().Before(a.lockUntil) {
		return true, 0
	}
	return false, time.Until(a.lockUntil)
}

// recordFailure counts a wrong-password attempt and locks the IP out for
// loginLockout once it hits maxLoginFailures, resetting the counter so the
// next window starts fresh once the lockout expires.
func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.byIP[ip]
	if a == nil {
		a = &loginAttempt{}
		l.byIP[ip] = a
	}
	a.failures++
	if a.failures >= maxLoginFailures {
		a.lockUntil = time.Now().Add(loginLockout)
		a.failures = 0
	}
}

func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, retryAfter := s.loginLimiter.allow(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": fmt.Sprintf("too many failed attempts — try again in %s", retryAfter.Round(time.Second)),
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.secret)) != 1 {
		s.loginLimiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect password"})
		return
	}
	s.loginLimiter.recordSuccess(ip)
	exp := time.Now().Add(sessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    strconv.FormatInt(exp, 10) + "." + sessionToken(s.secret, exp),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := s.src.ListTenants(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tenants)
}

func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := s.src.GetSchema(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, statusForErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, schema)
}

func (s *Server) handleRows(w http.ResponseWriter, r *http.Request) {
	opts, err := parseQueryOptions(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.src.QueryRows(r.Context(), r.PathValue("id"), r.PathValue("table"), opts)
	if err != nil {
		writeErr(w, statusForErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// parseQueryOptions reads limit/offset/order_by/desc and repeated filter params.
// A filter is "column:op:value" (value may contain ':').
func parseQueryOptions(r *http.Request) (QueryOptions, error) {
	q := r.URL.Query()
	opts := QueryOptions{}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return opts, fmt.Errorf("invalid limit")
		}
		opts.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return opts, fmt.Errorf("invalid offset")
		}
		opts.Offset = n
	}
	opts.OrderBy = q.Get("order_by")
	opts.Desc = q.Get("desc") == "1" || strings.EqualFold(q.Get("desc"), "true")

	for _, raw := range q["filter"] {
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) != 3 {
			return opts, fmt.Errorf("invalid filter %q (want column:op:value)", raw)
		}
		opts.Filters = append(opts.Filters, Filter{Column: parts[0], Op: parts[1], Value: parts[2]})
	}
	return opts, nil
}

func statusForErr(err error) int {
	if err == nil {
		return http.StatusOK
	}
	// Proxy mode: honor the upstream API's status code directly.
	var se *statusError
	if errors.As(err, &se) {
		return se.status
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "unknown table or column"),
		strings.Contains(msg, "unsupported filter"),
		strings.Contains(msg, "invalid identifier"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
