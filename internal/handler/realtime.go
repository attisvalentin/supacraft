package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/middleware"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/realtime"
	"github.com/attisvalentin/supacraft/internal/schema"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/time/rate"
)

// Realtime limits.
const (
	rtMaxSubs           = 50
	rtMaxConnsPerIP     = 20
	rtMaxConnsPerTenant = 1000
	rtReadLimit         = 64 << 10
	// A client may send rtMessageRate messages a second (bursts of
	// rtMessageBurst); subscribing costs database reads.
	rtMessageRate  = 20
	rtMessageBurst = 40
	// rtBuffer is how many messages may wait for a slow client before the
	// server drops it (it resumes with since).
	rtBuffer       = 256
	rtPing         = 25 * time.Second
	rtWriteTimeout = 10 * time.Second
)

// rtSessionCheck is how often a signed-in connection re-reads its user;
// tests shorten it.
var rtSessionCheck = time.Minute

// Close codes beyond the standard ones.
const (
	rtCloseAuth = websocket.StatusCode(4001) // token expired, session ended or another user's token
	rtCloseGone = websocket.StatusCode(4004) // the tenant was removed or disabled
	rtCloseSlow = websocket.StatusCode(4008) // the client did not keep up
)

// rtMessage is what a client sends.
type rtMessage struct {
	Type   string         `json:"type"`
	Token  string         `json:"token,omitempty"`
	ID     string         `json:"id,omitempty"`
	Table  string         `json:"table,omitempty"`
	Events []string       `json:"events,omitempty"`
	Where  map[string]any `json:"where,omitempty"`
	Since  *int64         `json:"since,omitempty"`
}

// rtError is a refusal sent to the client as {type: "error", code, message}.
type rtError struct{ code, msg string }

func (e *rtError) Error() string { return e.msg }

// HandleRealtime: GET /realtime upgrades to a WebSocket that streams the
// changes of api_settings.realtime tables (see the reference for the
// protocol).
func HandleRealtime(pool *tenant.PoolManager, hub *realtime.Hub, trustProxy bool) http.HandlerFunc {
	limits := &connLimits{count: map[string]int{}}
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := middleware.TenantFromContext(r.Context())
		if !cfg.RealtimeEnabled() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		tenantID := r.Header.Get("X-Tenant-ID")
		release, ok := limits.acquire(tenantID, middleware.ClientIP(r, trustProxy))
		if !ok {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many realtime connections"})
			return
		}
		defer release()

		// Browsers may connect from the tenant's CORS origin (or the API's
		// own); other clients send no Origin.
		opts := &websocket.AcceptOptions{}
		switch origin := cfg.APISettings.CORS.AllowedOrigin; origin {
		case "":
		case "*":
			opts.InsecureSkipVerify = true
		default:
			opts.OriginPatterns = []string{origin}
		}
		ws, err := websocket.Accept(w, r, opts)
		if err != nil {
			return // Accept answered the request
		}
		ws.SetReadLimit(rtReadLimit)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		c := &rtConn{
			ws: ws, hub: hub, pool: pool, cfg: cfg, tenantID: tenantID,
			out: make(chan any, rtBuffer), closing: make(chan rtClose, 1),
			subs: map[string]*realtime.Subscription{}, expiry: time.NewTimer(time.Hour),
		}
		c.expiry.Stop()
		go c.writeLoop(ctx, cancel)
		c.readLoop(ctx)
		c.mu.Lock()
		for _, s := range c.subs {
			hub.Unsubscribe(tenantID, s)
		}
		c.mu.Unlock()
	}
}

// connLimits counts open connections per tenant and per tenant + IP.
type connLimits struct {
	mu    sync.Mutex
	count map[string]int
}

func (l *connLimits) acquire(tenantID, ip string) (release func(), ok bool) {
	t, ti := "t:"+tenantID, "i:"+tenantID+"\x00"+ip
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count[t] >= rtMaxConnsPerTenant || l.count[ti] >= rtMaxConnsPerIP {
		return nil, false
	}
	l.count[t]++
	l.count[ti]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, k := range []string{t, ti} {
			if l.count[k]--; l.count[k] <= 0 {
				delete(l.count, k)
			}
		}
	}, true
}

type rtClose struct {
	code   websocket.StatusCode
	reason string
}

// rtConn is one client connection.
type rtConn struct {
	ws       *websocket.Conn
	hub      *realtime.Hub
	pool     *tenant.PoolManager
	cfg      *tenant.TenantConfig // as of the upgrade; the hub reads the current one
	tenantID string

	out       chan any
	closing   chan rtClose
	closeOnce sync.Once

	mu     sync.Mutex
	claims *tenant.AuthClaims // nil until the client authenticates; replaced, never changed
	subs   map[string]*realtime.Subscription
	expiry *time.Timer
}

// close ends the connection with code; the first reason wins.
func (c *rtConn) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() { c.closing <- rtClose{code, reason} })
}

// send queues a message, waiting while the buffer is full.
func (c *rtConn) send(ctx context.Context, msg any) {
	select {
	case c.out <- msg:
	case <-ctx.Done():
	}
}

// trySend queues a message unless the buffer is full, which drops the
// client: it fell behind.
func (c *rtConn) trySend(msg any) bool {
	select {
	case c.out <- msg:
		return true
	default:
		c.close(rtCloseSlow, "too slow: reconnect and resume with since")
		return false
	}
}

func (c *rtConn) currentClaims() *tenant.AuthClaims {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claims
}

func (c *rtConn) writeLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	ping := time.NewTicker(rtPing)
	defer ping.Stop()
	check := time.NewTicker(rtSessionCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			c.ws.Close(websocket.StatusNormalClosure, "")
			return
		case <-c.hub.Done():
			c.ws.Close(websocket.StatusGoingAway, "server shutting down")
			return
		case cl := <-c.closing:
			c.ws.Close(cl.code, cl.reason)
			return
		case <-c.expiry.C:
			c.ws.Close(rtCloseAuth, "token expired: reconnect with a new one, or send it before expiry")
			return
		case <-check.C:
			if reason := c.checkSession(ctx); reason != "" {
				c.ws.Close(rtCloseAuth, reason)
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, rtWriteTimeout)
			err := c.ws.Ping(pctx)
			pcancel()
			if err != nil {
				c.ws.CloseNow()
				return
			}
		case msg := <-c.out:
			wctx, wcancel := context.WithTimeout(ctx, rtWriteTimeout)
			err := wsjson.Write(wctx, c.ws, msg)
			wcancel()
			if err != nil {
				c.ws.CloseNow()
				return
			}
		}
	}
}

// checkSession re-reads the signed-in user: a disabled user or a password
// reset ends the connection; a new role applies to the next changes.
func (c *rtConn) checkSession(ctx context.Context) string {
	claims := c.currentClaims()
	if claims == nil {
		return ""
	}
	db, err := c.pool.GetPool(ctx, c.cfg)
	if err != nil {
		return ""
	}
	sess, ok := loadSession(ctx, db, claims.UserID)
	if !ok || sess.version != claims.Version {
		return "session ended"
	}
	updated := *claims
	updated.Role = c.cfg.APISettings.Auth.EffectiveRole(sess.role)
	c.mu.Lock()
	c.claims = &updated
	c.mu.Unlock()
	return ""
}

func (c *rtConn) readLoop(ctx context.Context) {
	limit := rate.NewLimiter(rtMessageRate, rtMessageBurst)
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if !limit.Allow() {
			// Keep reading until the close handshake ends the loop, so the
			// client gets this close code.
			c.close(websocket.StatusPolicyViolation, "too many messages")
			continue
		}
		var msg rtMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.send(ctx, errorMsg("", "bad_request", "messages are JSON objects with a type"))
			continue
		}
		switch msg.Type {
		case "auth":
			c.auth(ctx, msg.Token)
		case "subscribe":
			c.subscribe(ctx, msg)
		case "unsubscribe":
			c.unsubscribe(ctx, msg.ID)
		case "ping":
			c.send(ctx, map[string]any{"type": "pong"})
		default:
			c.send(ctx, errorMsg(msg.ID, "bad_request", "type must be auth, subscribe, unsubscribe or ping"))
		}
	}
}

func errorMsg(id, code, message string) map[string]any {
	m := map[string]any{"type": "error", "code": code, "message": message}
	if id != "" {
		m["id"] = id
	}
	return m
}

// auth signs the connection in with an access token, or renews it (same
// user) before it expires.
func (c *rtConn) auth(ctx context.Context, token string) {
	if !c.cfg.AuthEnabled {
		c.send(ctx, errorMsg("", "bad_request", "this API has no sign-in"))
		return
	}
	claims, exp, err := tokenClaims(ctx, c.cfg, c.pool, token)
	if err != nil {
		c.send(ctx, errorMsg("", "unauthorized", "invalid or expired token"))
		return
	}
	c.mu.Lock()
	if c.claims != nil && c.claims.UserID != claims.UserID {
		c.mu.Unlock()
		c.close(rtCloseAuth, "a connection stays with one user")
		return
	}
	c.claims = claims
	c.expiry.Reset(time.Until(exp))
	c.mu.Unlock()
	c.send(ctx, map[string]any{"type": "authed", "user_id": claims.UserID, "expires_at": exp.UTC().Format(time.RFC3339)})
}

var rtOps = map[string]bool{tenant.OpInsert: true, tenant.OpUpdate: true, tenant.OpDelete: true}

func (c *rtConn) subscribe(ctx context.Context, msg rtMessage) {
	id := msg.ID
	if id == "" || len(id) > 64 {
		c.send(ctx, errorMsg("", "bad_request", "subscribe needs an id (at most 64 characters) of your choice"))
		return
	}
	ops := map[string]bool{}
	for _, e := range msg.Events {
		if !rtOps[e] {
			c.send(ctx, errorMsg(id, "bad_request", "events are insert, update and delete"))
			return
		}
		ops[e] = true
	}
	if len(ops) == 0 {
		ops = rtOps
	}
	table, where := msg.Table, msg.Where
	s := &realtime.Subscription{
		Table: table,
		Ops:   ops,
		Filter: func(cfg *tenant.TenantConfig) (realtime.Filter, error) {
			return realtimeFilter(cfg, c.currentClaims(), table, where)
		},
		Started: func(seq int64) {
			c.trySend(map[string]any{"type": "subscribed", "id": id, "seq": seq})
		},
		Send: func(e realtime.Event) bool {
			return c.trySend(map[string]any{"type": "change", "id": id, "seq": e.Seq, "table": e.Table, "event": e.Op, "row": e.Row})
		},
	}
	s.Ended = func(err error) {
		c.mu.Lock()
		if c.subs[id] == s {
			delete(c.subs, id)
		}
		c.mu.Unlock()
		var rerr *rtError
		switch {
		case errors.Is(err, realtime.ErrSlow):
			c.close(rtCloseSlow, "too slow: reconnect and resume with since")
		case errors.Is(err, realtime.ErrGone):
			c.close(rtCloseGone, "tenant no longer served")
		case errors.Is(err, realtime.ErrClosed):
			c.close(websocket.StatusGoingAway, "server shutting down")
		case errors.As(err, &rerr):
			c.trySend(errorMsg(id, rerr.code, rerr.msg))
		default:
			c.trySend(errorMsg(id, "server_error", "subscription ended"))
		}
	}

	c.mu.Lock()
	switch {
	case c.subs[id] != nil:
		c.mu.Unlock()
		c.send(ctx, errorMsg(id, "bad_request", "a subscription with this id exists"))
		return
	case len(c.subs) >= rtMaxSubs:
		c.mu.Unlock()
		c.send(ctx, errorMsg(id, "bad_request", "too many subscriptions on one connection"))
		return
	}
	c.subs[id] = s
	c.mu.Unlock()

	if err := c.hub.Subscribe(ctx, c.tenantID, s, msg.Since); err != nil {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
		var rerr *rtError
		switch {
		case errors.As(err, &rerr):
			c.send(ctx, errorMsg(id, rerr.code, rerr.msg))
		case errors.Is(err, realtime.ErrResync):
			c.send(ctx, errorMsg(id, "resync", err.Error()))
		case errors.Is(err, realtime.ErrBadSince):
			c.send(ctx, errorMsg(id, "bad_request", err.Error()))
		default:
			c.send(ctx, errorMsg(id, "server_error", "could not subscribe"))
		}
	}
}

func (c *rtConn) unsubscribe(ctx context.Context, id string) {
	c.mu.Lock()
	s := c.subs[id]
	delete(c.subs, id)
	c.mu.Unlock()
	if s == nil {
		c.send(ctx, errorMsg(id, "not_found", "no subscription with this id"))
		return
	}
	c.hub.Unsubscribe(c.tenantID, s)
	c.send(ctx, map[string]any{"type": "unsubscribed", "id": id})
}

// realtimeFilter is a subscription's access rule: the table's query
// settings (auth, roles, row_policy) plus the client's where, over its
// readable columns — the rows /query would return to this caller.
func realtimeFilter(cfg *tenant.TenantConfig, claims *tenant.AuthClaims, table string, where map[string]any) (realtime.Filter, error) {
	var f realtime.Filter
	settings, ok := cfg.APISettings.Tables[table]
	if !ok || !settings.Query.Enabled || !cfg.WantsRealtime(table) {
		return f, &rtError{"not_found", "table not found or not streamed"}
	}
	q := settings.Query
	if q.NeedsAuth() && claims == nil {
		return f, &rtError{"unauthorized", "sign in first: send {\"type\": \"auth\", \"token\": ...}"}
	}
	if q.NeedsAuth() && !tenant.RoleAllowed(q.Roles, claims.Role) {
		return f, &rtError{"forbidden", "your role may not read this table"}
	}
	tbl, err := schema.FindTable(&cfg.Schema, table)
	if err != nil {
		return f, &rtError{"not_found", "table not found or not streamed"}
	}
	cond, args, err := query.BuildWhere(where, tbl, 0)
	if err != nil {
		return f, &rtError{"bad_request", err.Error()}
	}
	policy, policyArgs, err := query.BuildRowPolicy(q.RowPolicy, claims, 0)
	if err != nil {
		return f, &rtError{"forbidden", err.Error()}
	}
	if policy != "" {
		if cond != "" {
			cond = "(" + cond + ") AND (" + policy + ")"
		} else {
			cond = "(" + policy + ")"
		}
		args = append(args, policyArgs...)
	}
	return realtime.Filter{Cond: cond, Args: args, Columns: cfg.ReadableColumns(table)}, nil
}

// ChangesResponse is the answer of GET /admin/tenants/{id}/changes.
type ChangesResponse struct {
	Data    []tenant.Change `json:"data"`
	LastSeq int64           `json:"last_seq"`
}

// HandleChangesList: GET /admin/tenants/{tenantID}/changes?since=0&table=&limit=100
// reads a tenant's realtime change log (readable columns, oldest first)
// and the last seq; limit=0 returns only the seq, to follow from now.
func HandleChangesList(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := adminTenant(w, r, cfg)
		if !ok {
			return
		}
		var since int64
		if v := r.URL.Query().Get("since"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since must be a seq (0 or more)"})
				return
			}
			since = n
		}
		limit := 100
		if r.URL.Query().Has("limit") {
			limit = queryInt(r, "limit", 100, 500)
		}
		list, last, err := tenant.ListTenantChanges(r.Context(), cfg.DataDir, id, r.URL.Query().Get("table"), since, limit)
		if err != nil {
			writeTenantErr(w, r, err)
			return
		}
		if list == nil {
			list = []tenant.Change{}
		}
		writeJSON(w, http.StatusOK, ChangesResponse{Data: list, LastSeq: last})
	}
}
