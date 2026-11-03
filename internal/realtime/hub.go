// Package realtime streams table changes to subscribers. Every change of a
// streamed table is logged in the tenant database (_supacraft_changes, see
// tenant.EmitChanges), so changes made by another process (the CLI) and
// changes a client missed while reconnecting reach it too. The hub tails
// that log for each tenant with subscribers and sends every subscription
// the changed rows its access rule lets it read.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

const (
	// poll is how often the log is read for changes the hub wasn't told
	// about (written by another process).
	poll = 500 * time.Millisecond
	// readBatch is how many logged changes one read takes.
	readBatch = 500
)

var (
	// ErrResync: the changes after the requested seq are no longer kept;
	// the client reloads with /query and subscribes again.
	ErrResync = errors.New("the changes since then are no longer kept; reload and subscribe again")
	// ErrBadSince: since is not a seq this server sent.
	ErrBadSince = errors.New("since must be a seq this server sent")
	// ErrSlow: the subscriber didn't keep up and lost its place.
	ErrSlow = errors.New("the client did not keep up")
	// ErrGone: the tenant was removed or disabled.
	ErrGone = errors.New("the tenant is no longer served")
	// ErrClosed: the server is shutting down.
	ErrClosed = errors.New("the server is shutting down")
)

// Event is one change a subscription receives.
type Event struct {
	Seq   int64
	Table string
	Op    string
	Row   map[string]any
}

// Filter is what a subscription may receive under the config in force:
// rows for which Cond (SQL over the row's columns, "" for every row) holds,
// with only Columns.
type Filter struct {
	Cond    string
	Args    []any
	Columns map[string]bool
}

// Subscription is one client's interest in a table's changes.
type Subscription struct {
	Table string
	Ops   map[string]bool // insert, update, delete
	// Filter builds the access rule for cfg. It runs for every batch of
	// changes, so a new config or role applies at once; an error ends the
	// subscription.
	Filter func(cfg *tenant.TenantConfig) (Filter, error)
	// Send hands an event to the client without blocking; false means its
	// buffer is full, which ends the subscription with ErrSlow.
	Send func(Event) bool
	// Started is called with the seq the subscription starts after,
	// before any event; it must not block.
	Started func(seq int64)
	// Ended is called once when the hub ends the subscription.
	Ended func(error)

	cursor int64 // last seq handled; only the feed goroutine touches it after Subscribe
}

// Hub tails the change logs of the tenants with subscribers.
type Hub struct {
	store tenant.Store
	pools *tenant.PoolManager

	mu     sync.Mutex
	feeds  map[string]*feed // by tenant id
	closed bool
	done   chan struct{}
}

type feed struct {
	tenantID string
	dbName   string
	subs     map[*Subscription]bool
	wake     chan struct{}
}

// NewHub creates a hub reading tenants from store.
func NewHub(store tenant.Store, pools *tenant.PoolManager) *Hub {
	return &Hub{store: store, pools: pools, feeds: map[string]*feed{}, done: make(chan struct{})}
}

// Done is closed when the hub shuts down.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Close ends every feed; connections watch Done and close.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
}

// Notify tells the hub that the database dbName logged changes.
func (h *Hub) Notify(dbName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.feeds {
		if f.dbName == dbName {
			wake(f)
		}
	}
}

func wake(f *feed) {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// Subscribe starts s on a tenant: from the changes after *since, or from
// now when since is nil.
func (h *Hub) Subscribe(ctx context.Context, tenantID string, s *Subscription, since *int64) error {
	cfg, err := h.store.GetTenantConfig(ctx, tenantID)
	if err != nil {
		return err
	}
	if _, err := s.Filter(cfg); err != nil {
		return err
	}
	db, err := h.pools.GetPool(ctx, cfg)
	if err != nil {
		return err
	}
	oldest, last, err := tenant.ChangeBounds(ctx, db)
	if err != nil {
		return err
	}
	s.cursor = last
	if since != nil {
		switch {
		case *since < 0 || *since > last:
			return fmt.Errorf("%w (0-%d)", ErrBadSince, last)
		case *since+1 < oldest:
			return ErrResync
		}
		s.cursor = *since
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	f := h.feeds[tenantID]
	if f == nil {
		f = &feed{tenantID: tenantID, dbName: cfg.DBName, subs: map[*Subscription]bool{}, wake: make(chan struct{}, 1)}
		h.feeds[tenantID] = f
		go h.run(f)
	}
	s.Started(s.cursor)
	f.subs[s] = true
	if since != nil {
		wake(f)
	}
	return nil
}

// Unsubscribe stops s; Ended is not called.
func (h *Hub) Unsubscribe(tenantID string, s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f := h.feeds[tenantID]; f != nil {
		delete(f.subs, s)
	}
}

// end removes s and tells its client why.
func (h *Hub) end(f *feed, s *Subscription, err error) {
	h.mu.Lock()
	_, live := f.subs[s]
	delete(f.subs, s)
	h.mu.Unlock()
	if live {
		s.Ended(err)
	}
}

// run reads one tenant's log until it has no subscribers.
func (h *Hub) run(f *feed) {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			h.endAll(f, ErrClosed)
			return
		case <-tick.C:
		case <-f.wake:
		}
		h.mu.Lock()
		if len(f.subs) == 0 {
			delete(h.feeds, f.tenantID)
			h.mu.Unlock()
			return
		}
		subs := slices.Collect(maps.Keys(f.subs))
		h.mu.Unlock()
		if err := h.dispatch(f, subs); err != nil {
			slog.Warn("realtime: read changes", "tenant", f.tenantID, "err", err)
		}
	}
}

func (h *Hub) endAll(f *feed, err error) {
	h.mu.Lock()
	subs := slices.Collect(maps.Keys(f.subs))
	h.mu.Unlock()
	for _, s := range subs {
		h.end(f, s, err)
	}
}

// dispatch sends subs the changes after their cursors.
func (h *Hub) dispatch(f *feed, subs []*Subscription) error {
	ctx := context.Background()
	cfg, err := h.store.GetTenantConfig(ctx, f.tenantID)
	if errors.Is(err, tenant.ErrTenantNotFound) || (err == nil && !cfg.Enabled) {
		h.endAll(f, ErrGone)
		return nil
	}
	if err != nil {
		return err
	}
	db, err := h.pools.GetPool(ctx, cfg)
	if err != nil {
		return err
	}
	filters := map[*Subscription]Filter{}
	live := subs[:0]
	for _, s := range subs {
		flt, err := s.Filter(cfg)
		if err != nil {
			h.end(f, s, err)
			continue
		}
		filters[s] = flt
		live = append(live, s)
	}
	for len(live) > 0 {
		from := live[0].cursor
		for _, s := range live {
			from = min(from, s.cursor)
		}
		changes, err := tenant.ReadChanges(ctx, db, from, "", readBatch)
		if err != nil {
			return err
		}
		for _, c := range changes {
			live = h.deliver(ctx, f, db, c, live, filters)
		}
		if len(changes) < readBatch {
			return nil
		}
	}
	return nil
}

// deliver sends c to the subscriptions that want it and may see it, and
// returns the ones still live.
func (h *Hub) deliver(ctx context.Context, f *feed, db *tenant.DB, c tenant.Change, subs []*Subscription, filters map[*Subscription]Filter) []*Subscription {
	// Subscriptions with the same rule (e.g. the same user) share one check.
	verdict := map[string]bool{}
	live := subs[:0]
	for _, s := range subs {
		if s.cursor >= c.Seq {
			live = append(live, s)
			continue
		}
		s.cursor = c.Seq
		if s.Table != c.Table || !s.Ops[c.Op] {
			live = append(live, s)
			continue
		}
		flt := filters[s]
		ok := true
		if flt.Cond != "" {
			key, _ := json.Marshal(append([]any{flt.Cond}, flt.Args...))
			var seen bool
			if ok, seen = verdict[string(key)]; !seen {
				var err error
				if ok, err = matches(ctx, db, c, flt); err != nil {
					slog.Warn("realtime: check row", "tenant", f.tenantID, "table", c.Table, "err", err)
				}
				verdict[string(key)] = ok
			}
		}
		if ok && !s.Send(Event{Seq: c.Seq, Table: c.Table, Op: c.Op, Row: pick(c.Row, flt.Columns)}) {
			h.end(f, s, ErrSlow)
			continue
		}
		live = append(live, s)
	}
	return live
}

// matches evaluates flt.Cond on the changed row, as a one-row table named
// like the real one, so a rule reads exactly as in /query — also for a
// deleted row.
func matches(ctx context.Context, db *tenant.DB, c tenant.Change, flt Filter) (bool, error) {
	cols := slices.Sorted(maps.Keys(c.Row))
	if len(cols) == 0 {
		return false, nil
	}
	quoted := make([]string, len(cols))
	args := make([]any, 0, len(cols)+len(flt.Args))
	for i, col := range cols {
		quoted[i] = query.QuoteIdent(col)
		args = append(args, c.Row[col])
	}
	table := query.QuoteIdent(c.Table)
	stmt := fmt.Sprintf(`WITH %s(%s) AS (VALUES (%s)) SELECT 1 FROM %s WHERE %s`,
		table, strings.Join(quoted, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", "), table, flt.Cond)
	rows, err := db.Query(ctx, stmt, append(args, flt.Args...)...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func pick(row map[string]any, cols map[string]bool) map[string]any {
	out := make(map[string]any, len(cols))
	for k, v := range row {
		if cols[k] {
			out[k] = v
		}
	}
	return out
}
