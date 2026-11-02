package testrun

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/golang-jwt/jwt/v5"
)

// Result is the outcome of one test.
type Result struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Status   int      `json:"status,omitempty"`
	Failures []string `json:"failures,omitempty"`
	// Body is the response body, kept for failed tests.
	Body string `json:"body,omitempty"`
	// ServerErrors are the internal errors the request logged (5xx).
	ServerErrors []string `json:"server_errors,omitempty"`
}

// Run runs the project's tests, each against its own fresh database with
// the seed data, in a temporary directory: the project's .supacraft is
// never touched. No email is sent and no network call is made. only, when
// not empty, picks the tests to run by name.
func Run(ctx context.Context, p *project.Project, only ...string) ([]Result, error) {
	tests, err := p.TestCases()
	if err != nil {
		return nil, err
	}
	seed, err := p.SeedData()
	if err != nil {
		return nil, err
	}
	for _, n := range only {
		if _, ok := tests[n]; !ok {
			return nil, fmt.Errorf("no test named %q", n)
		}
	}
	names := make([]string, 0, len(tests))
	for name := range tests {
		if len(only) == 0 || contains(only, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	results := make([]Result, 0, len(names))
	for _, name := range names {
		res, err := runOne(ctx, p, seed, name, tests[name])
		if err != nil {
			return nil, fmt.Errorf("test %q: %w", name, err)
		}
		results = append(results, *res)
	}
	return results, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func runOne(ctx context.Context, p *project.Project, seed *project.Seed, name string, t project.Test) (*Result, error) {
	dir, err := os.MkdirTemp("", "supacraft-test-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := p.ApplyTo(ctx, dir, project.ApplyOptions{Replace: true, ProjectEnv: true}); err != nil {
		return nil, err
	}
	id := p.TenantID()
	if err := localStorageOnly(dir, id); err != nil {
		return nil, err
	}
	cfg := &config.Config{
		DataDir:       dir,
		StoragePath:   filepath.Join(dir, "storage"),
		MaxFileSize:   10 << 20,
		MaxBodySize:   1 << 20,
		DefaultTenant: id,
	}
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		return nil, err
	}
	tcfg, err := store.GetTenantConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	errLog, err := errorlog.Open(filepath.Join(dir, "errors.db"))
	if err != nil {
		return nil, err
	}
	defer errLog.Close()
	pools := tenant.NewPoolManager(cfg)
	defer pools.Retain(nil)

	db, err := sql.Open("sqlite", filepath.Join(dir, id+".db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	env := p.Env()
	if _, err := Seed(ctx, db, tcfg, seed, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		return nil, err
	}

	res := &Result{Name: name}
	fail := func(format string, args ...any) { res.Failures = append(res.Failures, fmt.Sprintf(format, args...)) }

	method, path, ok := project.ParseCall(t.Call)
	if _, err := url.ParseRequestURI(path); !ok || err != nil {
		return nil, fmt.Errorf("invalid call %q", t.Call)
	}
	if len(t.Query) > 0 {
		q := url.Values{}
		for k, v := range t.Query {
			q.Set(k, v)
		}
		path += "?" + q.Encode()
	}
	var body io.Reader = http.NoBody
	if t.Body != nil {
		data, err := json.Marshal(t.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	var errMu sync.Mutex
	sb := &pipeline.Sandbox{Mailer: &mailbox{}, HTTP: &mocks{routes: t.HTTPMocks}, Errors: func(msg string) {
		errMu.Lock()
		defer errMu.Unlock()
		res.ServerErrors = append(res.ServerErrors, msg)
	}}
	req := httptest.NewRequestWithContext(pipeline.WithSandbox(ctx, sb), method, path, body)
	req.RemoteAddr = "192.0.2.1:1234"
	if t.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.As != "" {
		token, err := signIn(ctx, db, tcfg, t.As)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	handler.NewRouter(tenant.NewCachedStore(store), pools, errLog, cfg).ServeHTTP(rec, req)
	res.Status = rec.Code

	if t.Expect.Status != 0 && rec.Code != t.Expect.Status {
		fail("status %d, want %d", rec.Code, t.Expect.Status)
	}
	if t.Expect.Body != nil {
		var got any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			fail("response is not JSON: %s", oneLine(rec.Body.String()))
		} else {
			for _, diff := range match("body", t.Expect.Body, got) {
				fail("%s", diff)
			}
		}
	}
	if n := t.Expect.Emails; n != nil && len(sb.Mailer.(*mailbox).sent) != *n {
		fail("sent %d email(s), want %d", len(sb.Mailer.(*mailbox).sent), *n)
	}
	for _, msg := range sb.HTTP.(*mocks).unmatched {
		fail("%s", msg)
	}
	tables := make([]string, 0, len(t.Expect.DB))
	for table := range t.Expect.DB {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		want := t.Expect.DB[table]
		n, err := countRows(ctx, db, tcfg, table, want.Where)
		if err != nil {
			fail("db.%s: %v", table, err)
		} else if n != want.Count {
			fail("db.%s: %d matching row(s), want %d", table, n, want.Count)
		}
	}

	res.Passed = len(res.Failures) == 0
	if !res.Passed {
		res.Body = oneLine(rec.Body.String())
	}
	// .env values end up in URLs and error messages; results are shown to
	// agents, so mask them like the server error log does.
	for i := range res.Failures {
		res.Failures[i] = handler.MaskSecrets(res.Failures[i], env)
	}
	for i := range res.ServerErrors {
		res.ServerErrors[i] = handler.MaskSecrets(res.ServerErrors[i], env)
	}
	res.Body = handler.MaskSecrets(res.Body, env)
	return res, nil
}

// localStorageOnly points the test tenant's file storage at the temporary
// directory, so a test never reaches S3, FTP or any other remote storage
// (and its credentials leave the config).
func localStorageOnly(dir, id string) error {
	path := filepath.Join(dir, "tenants", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	settings, _ := cfg["api_settings"].(map[string]any)
	st, _ := settings["storage"].(map[string]any)
	if st == nil {
		return nil
	}
	st["driver"] = "local"
	delete(st, "config")
	if data, err = json.Marshal(cfg); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// signIn issues an access token for a seed user, as /auth/login would.
func signIn(ctx context.Context, db *sql.DB, cfg *tenant.TenantConfig, email string) (string, error) {
	var secret, userID string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT "jwt_secret" FROM "api_config" LIMIT 1`).Scan(&secret); err != nil {
		return "", fmt.Errorf("as %s: auth is not set up: %w", email, err)
	}
	err := db.QueryRowContext(ctx, `SELECT "id", COALESCE("session_version", 0) FROM "users" WHERE "email" = ?`, email).Scan(&userID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("as %s: not a seed user", email)
	}
	if err != nil {
		return "", err
	}
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID, "email": email, "ver": version,
		"iat": now.Unix(), "exp": now.Add(cfg.APISettings.Auth.AccessTTL()).Unix(),
	}).SignedString([]byte(secret))
}

// countRows counts the rows of table matching where (filter operators as
// in the API).
func countRows(ctx context.Context, db *sql.DB, cfg *tenant.TenantConfig, table string, where map[string]any) (int, error) {
	t, err := schema.FindTableByName(&cfg.Schema, table)
	if err != nil {
		return 0, err
	}
	whereSQL, args, err := query.BuildWhere(where, t, 0)
	if err != nil {
		return 0, err
	}
	stmt := `SELECT COUNT(*) FROM ` + query.QuoteIdent(table)
	if whereSQL != "" {
		stmt += " WHERE " + whereSQL
	}
	var n int
	err = db.QueryRowContext(ctx, stmt, args...).Scan(&n)
	return n, err
}

// match reports where got differs from want: objects match on the keys
// want lists, arrays element by element with the same length, and numbers
// by value.
func match(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got %s, want an object", path, show(got))}
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var diffs []string
		for _, k := range keys {
			gv, present := g[k]
			if !present {
				diffs = append(diffs, fmt.Sprintf("%s.%s: missing", path, k))
				continue
			}
			diffs = append(diffs, match(path+"."+k, w[k], gv)...)
		}
		return diffs
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got %s, want an array", path, show(got))}
		}
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s: %d item(s), want %d", path, len(g), len(w))}
		}
		var diffs []string
		for i := range w {
			diffs = append(diffs, match(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return diffs
	}
	if !reflect.DeepEqual(want, got) {
		return []string{fmt.Sprintf("%s: got %s, want %s", path, show(got), show(want))}
	}
	return nil
}

func show(v any) string {
	data, _ := json.Marshal(v)
	return oneLine(string(data))
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:297]) + "..."
	}
	return s
}

// mailbox records emails instead of sending them.
type mailbox struct {
	mu   sync.Mutex
	sent []pipeline.MailMessage
}

func (m *mailbox) Send(_ context.Context, _ tenant.SMTPSettings, msg pipeline.MailMessage) (*pipeline.MailSendMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return &pipeline.MailSendMeta{}, nil
}

// mocks answers http.request steps from a test's http_mocks; the longest
// matching URL prefix wins and anything else fails the test.
type mocks struct {
	routes    map[string]project.HTTPMock
	mu        sync.Mutex
	unmatched []string
}

func (m *mocks) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	best := ""
	for prefix := range m.routes {
		if strings.HasPrefix(u, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		msg := fmt.Sprintf("http.request %s %s has no http_mocks entry (tests never call the network)", req.Method, u)
		m.mu.Lock()
		m.unmatched = append(m.unmatched, msg)
		m.mu.Unlock()
		return nil, errors.New(msg)
	}
	mock := m.routes[best]
	status := mock.Status
	if status == 0 {
		status = http.StatusOK
	}
	var data []byte
	switch b := mock.Body.(type) {
	case nil:
	case string:
		data = []byte(b)
	default:
		data, _ = json.Marshal(b)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
		Request:    req,
	}, nil
}
