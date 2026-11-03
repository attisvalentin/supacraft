package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
)

// Messages belong to users and are private to them; news is public.
const realtimeProject = `{
  "name": "chat",
  "auth_enabled": true,
  "schema": {"tables": [
    {"name": "messages", "columns": [
      {"name": "id", "type": "uuid", "primary": true},
      {"name": "room", "type": "varchar"},
      {"name": "body", "type": "varchar"},
      {"name": "owner_id", "type": "uuid"},
      {"name": "secret", "type": "varchar", "readable": false}]},
    {"name": "news", "columns": [
      {"name": "id", "type": "uuid", "primary": true},
      {"name": "title", "type": "varchar"}]}]},
  "api_settings": {
    "crud_endpoints": {"query": true, "create": true, "update": true, "delete": true},
    "tables": {
      "messages": {
        "query": {"enabled": true, "auth": true, "row_policy": "owner_id = $auth.id"},
        "create": {"enabled": true, "auth": true},
        "update": {"enabled": true, "auth": true, "row_policy": "owner_id = $auth.id"},
        "delete": {"enabled": true, "auth": true, "row_policy": "owner_id = $auth.id"}},
      "news": {"query": {"enabled": true}, "create": {"enabled": true}}
    },
    "realtime": {"tables": ["messages", "news"]}
  }
}`

type rtClient struct {
	t  *testing.T
	ws *websocket.Conn
}

// serve runs the API on a real listener (WebSockets need one).
func (a *testAPI) serve() string {
	srv := httptest.NewServer(a.handler)
	a.t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialRT(t *testing.T, base, query string) *rtClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, base+"/realtime"+query, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &rtClient{t: t, ws: ws}
}

func (c *rtClient) send(msg map[string]any) {
	c.t.Helper()
	if err := wsjson.Write(context.Background(), c.ws, msg); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// next reads the next message (a timeout fails the test: it would close
// the socket anyway).
func (c *rtClient) next() map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg map[string]any
	if err := wsjson.Read(ctx, c.ws, &msg); err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return msg
}

func (c *rtClient) expect(typ string) map[string]any {
	c.t.Helper()
	msg := c.next()
	if msg["type"] != typ {
		c.t.Fatalf("got %v, want a %s message", msg, typ)
	}
	return msg
}

func (c *rtClient) auth(token string) {
	c.t.Helper()
	c.send(map[string]any{"type": "auth", "token": token})
	c.expect("authed")
}

func (c *rtClient) subscribe(id, table string, extra map[string]any) map[string]any {
	c.t.Helper()
	msg := map[string]any{"type": "subscribe", "id": id, "table": table}
	for k, v := range extra {
		msg[k] = v
	}
	c.send(msg)
	return c.next()
}

// closeCode waits for the server to close the connection.
func (c *rtClient) closeCode() websocket.StatusCode {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.ws.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

type rtUser struct{ id, token string }

func (a *testAPI) rtUser(email string) rtUser {
	id := a.register(email)
	return rtUser{id: id, token: a.login(email)["token"].(string)}
}

func (a *testAPI) createMessage(u rtUser, room, body string) string {
	a.t.Helper()
	code, out, raw := a.do("POST", "/create",
		fmt.Sprintf(`{"table_id":"messages","data":{"room":%q,"body":%q,"owner_id":%q,"secret":"s3cret"}}`, room, body, u.id),
		"Authorization", "Bearer "+u.token)
	if code != http.StatusCreated {
		a.t.Fatalf("create message: %d %s", code, raw)
	}
	return out["data"].(map[string]any)["id"].(string)
}

func TestRealtime_RowPolicyAndColumns(t *testing.T) {
	a := newTestAPIWith(t, realtimeProject)
	base := a.serve()
	alice, bob := a.rtUser("alice@example.com"), a.rtUser("bob@example.com")

	ca, cb := dialRT(t, base, ""), dialRT(t, base, "")
	ca.auth(alice.token)
	cb.auth(bob.token)
	if msg := ca.subscribe("m", "messages", nil); msg["type"] != "subscribed" {
		t.Fatalf("subscribe: %v", msg)
	}
	cb.subscribe("m", "messages", nil)

	id := a.createMessage(alice, "r1", "hi")
	a.createMessage(bob, "r1", "hello")

	got := ca.expect("change")
	row := got["row"].(map[string]any)
	if got["event"] != "insert" || got["table"] != "messages" || got["id"] != "m" || row["id"] != id || row["body"] != "hi" {
		t.Fatalf("alice got %v", got)
	}
	if _, leaked := row["secret"]; leaked {
		t.Fatal("a non-readable column was sent")
	}
	// Bob's first change is his own message: alice's never reached him.
	if row := cb.expect("change")["row"].(map[string]any); row["body"] != "hello" {
		t.Fatalf("bob got someone else's row: %v", row)
	}

	// Update and delete reach the owner; the deleted row still matches
	// the policy.
	if code, _, raw := a.do("POST", "/update", fmt.Sprintf(`{"table_id":"messages","where":{"id":%q},"data":{"body":"edited"}}`, id), "Authorization", "Bearer "+alice.token); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, raw)
	}
	if code, _, raw := a.do("POST", "/delete", fmt.Sprintf(`{"table_id":"messages","where":{"id":%q}}`, id), "Authorization", "Bearer "+alice.token); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if got := ca.expect("change"); got["event"] != "update" || got["row"].(map[string]any)["body"] != "edited" {
		t.Fatalf("update event: %v", got)
	}
	if got := ca.expect("change"); got["event"] != "delete" || got["row"].(map[string]any)["id"] != id {
		t.Fatalf("delete event: %v", got)
	}
}

func TestRealtime_FiltersAndRefusals(t *testing.T) {
	a := newTestAPIWith(t, realtimeProject)
	base := a.serve()
	alice := a.rtUser("alice@example.com")

	anon := dialRT(t, base, "")
	if msg := anon.subscribe("m", "messages", nil); msg["code"] != "unauthorized" || msg["id"] != "m" {
		t.Fatalf("anonymous subscribe to a private table: %v", msg)
	}
	if msg := anon.subscribe("x", "users", nil); msg["code"] != "not_found" {
		t.Fatalf("unstreamed table: %v", msg)
	}
	if msg := anon.subscribe("w", "news", map[string]any{"where": map[string]any{"nope": 1}}); msg["code"] != "bad_request" {
		t.Fatalf("unknown where column: %v", msg)
	}
	if msg := anon.subscribe("e", "news", map[string]any{"events": []string{"upsert"}}); msg["code"] != "bad_request" {
		t.Fatalf("unknown event: %v", msg)
	}
	anon.send(map[string]any{"type": "auth", "token": "garbage"})
	if msg := anon.expect("error"); msg["code"] != "unauthorized" {
		t.Fatalf("bad token: %v", msg)
	}
	// Public tables need no sign-in; where and events narrow the stream.
	if msg := anon.subscribe("n", "news", map[string]any{"where": map[string]any{"title": "b"}, "events": []string{"insert"}}); msg["type"] != "subscribed" {
		t.Fatalf("public subscribe: %v", msg)
	}
	for _, title := range []string{"a", "b"} {
		if code, _, raw := a.do("POST", "/create", `{"table_id":"news","data":{"title":"`+title+`"}}`); code != http.StatusCreated {
			t.Fatalf("create news: %d %s", code, raw)
		}
	}
	if got := anon.expect("change"); got["row"].(map[string]any)["title"] != "b" {
		t.Fatalf("where ignored: %v", got)
	}
	anon.send(map[string]any{"type": "unsubscribe", "id": "n"})
	anon.expect("unsubscribed")
	anon.send(map[string]any{"type": "ping"})
	anon.expect("pong")

	// One connection, one user.
	c := dialRT(t, base, "")
	c.auth(alice.token)
	bob := a.rtUser("bob@example.com")
	c.send(map[string]any{"type": "auth", "token": bob.token})
	if code := c.closeCode(); code != rtCloseAuth {
		t.Fatalf("another user's token: close %d", code)
	}
}

func TestRealtime_ResumeAndExternalWrites(t *testing.T) {
	a := newTestAPIWith(t, realtimeProject)
	base := a.serve()
	c := dialRT(t, base, "")
	start := c.subscribe("n", "news", nil)["seq"].(float64)
	a.do("POST", "/create", `{"table_id":"news","data":{"title":"one"}}`)
	first := c.expect("change")
	c.ws.CloseNow()

	// Missed while away: one change from the API, one from another
	// process (like the CLI) writing to the database directly.
	a.do("POST", "/create", `{"table_id":"news","data":{"title":"two"}}`)
	dataDir, id := filepath.Dir(a.dbPath), strings.TrimSuffix(filepath.Base(a.dbPath), ".db")
	db, cfg, err := tenant.OpenTenantDB(context.Background(), dataDir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	externalWrite(t, db, cfg, "three")

	c = dialRT(t, base, "")
	if msg := c.subscribe("n", "news", map[string]any{"since": first["seq"]}); msg["seq"] != first["seq"] {
		t.Fatalf("resume: %v", msg)
	}
	for _, want := range []string{"two", "three"} {
		if got := c.expect("change"); got["row"].(map[string]any)["title"] != want {
			t.Fatalf("resumed %v, want %s", got, want)
		}
	}
	if first["seq"].(float64) != start+1 {
		t.Fatalf("seq %v after %v", first["seq"], start)
	}
	if msg := c.subscribe("bad", "news", map[string]any{"since": 99999}); msg["code"] != "bad_request" {
		t.Fatalf("since from the future: %v", msg)
	}
	// Changes older than the log: the client must reload.
	if _, err := db.Exec(`DELETE FROM "_supacraft_changes"`); err != nil {
		t.Fatal(err)
	}
	if msg := c.subscribe("old", "news", map[string]any{"since": first["seq"]}); msg["code"] != "resync" {
		t.Fatalf("pruned since: %v", msg)
	}
}

// externalWrite inserts a news row the way the CLI would: its own
// connection, change logged in the same transaction.
func externalWrite(t *testing.T, db *sql.DB, cfg *tenant.TenantConfig, title string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	row := map[string]any{"id": "ext-" + title, "title": title}
	if _, err := tx.ExecContext(ctx, `INSERT INTO "news" ("id", "title") VALUES (?, ?)`, row["id"], title); err != nil {
		t.Fatal(err)
	}
	if err := tenant.EmitChanges(ctx, &tenant.Tx{Tx: tx}, cfg, "news", tenant.OpInsert, []map[string]any{row}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRealtime_SessionEnds(t *testing.T) {
	old := rtSessionCheck
	rtSessionCheck = 50 * time.Millisecond
	t.Cleanup(func() { rtSessionCheck = old })

	a := newTestAPIWith(t, realtimeProject)
	base := a.serve()
	alice := a.rtUser("alice@example.com")
	c := dialRT(t, base, "")
	c.auth(alice.token)
	c.subscribe("m", "messages", nil)
	db, err := sql.Open("sqlite", a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE "users" SET "is_active" = 0 WHERE "id" = ?`, alice.id); err != nil {
		t.Fatal(err)
	}
	if code := c.closeCode(); code != rtCloseAuth {
		t.Fatalf("disabled user: close %d", code)
	}
}

func TestRealtime_CentralServerAndOff(t *testing.T) {
	a := newTestAPIServing(t, realtimeProject, false)
	base := a.serve()
	id := strings.TrimSuffix(filepath.Base(a.dbPath), ".db")
	c := dialRT(t, base, "?tenant="+id)
	if msg := c.subscribe("n", "news", nil); msg["type"] != "subscribed" {
		t.Fatalf("central subscribe: %v", msg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, base+"/realtime", nil); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no tenant: %v %v", resp, err)
	}

	// A project without realtime has no /realtime.
	off := newTestAPI(t)
	if _, resp, err := websocket.Dial(ctx, off.serve()+"/realtime", nil); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("realtime off: %v %v", resp, err)
	}
	// Another site's page can't connect.
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}
	if _, resp, err := websocket.Dial(ctx, base+"/realtime?tenant="+id, opts); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
}

func TestRealtime_ConnectionLimit(t *testing.T) {
	l := &connLimits{count: map[string]int{}}
	var releases []func()
	for range rtMaxConnsPerIP {
		r, ok := l.acquire("t", "1.2.3.4")
		if !ok {
			t.Fatal("refused below the limit")
		}
		releases = append(releases, r)
	}
	if _, ok := l.acquire("t", "1.2.3.4"); ok {
		t.Fatal("over the per-IP limit")
	}
	if _, ok := l.acquire("t", "5.6.7.8"); !ok {
		t.Fatal("another IP was refused")
	}
	releases[0]()
	if _, ok := l.acquire("t", "1.2.3.4"); !ok {
		t.Fatal("a released slot stayed taken")
	}
}

func TestChangesList_Admin(t *testing.T) {
	a := newTestAPIWith(t, realtimeProject)
	alice := a.rtUser("alice@example.com")
	a.createMessage(alice, "r1", "hi")
	a.do("POST", "/create", `{"table_id":"news","data":{"title":"one"}}`)

	dataDir, id := filepath.Dir(a.dbPath), strings.TrimSuffix(filepath.Base(a.dbPath), ".db")
	r := chi.NewRouter()
	r.Get("/admin/tenants/{tenantID}/changes", HandleChangesList(&config.Config{DataDir: dataDir, Central: true}))
	get := func(query string) (int, ChangesResponse) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/tenants/"+id+"/changes"+query, nil))
		var out ChangesResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := get("")
	if code != http.StatusOK || len(out.Data) != 2 || out.LastSeq != 2 {
		t.Fatalf("list: %d %+v", code, out)
	}
	if _, leaked := out.Data[0].Row["secret"]; leaked || out.Data[0].Row["body"] != "hi" {
		t.Fatalf("row: %+v", out.Data[0].Row)
	}
	if _, out := get("?table=news&since=0"); len(out.Data) != 1 || out.Data[0].Table != "news" {
		t.Fatalf("table filter: %+v", out)
	}
	if _, out := get("?limit=0"); len(out.Data) != 0 || out.LastSeq != 2 {
		t.Fatalf("limit 0: %+v", out)
	}
	if code, _ := get("?since=-1"); code != http.StatusBadRequest {
		t.Fatalf("negative since: %d", code)
	}
}
