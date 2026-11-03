package tenant

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RealtimeSettings streams the changes of some tables to WebSocket
// clients (GET /realtime). Each table needs query enabled: a subscriber
// gets the rows /query would show it.
type RealtimeSettings struct {
	Tables []string `json:"tables"`
}

// WantsRealtime reports whether changes of table are streamed.
func (cfg *TenantConfig) WantsRealtime(table string) bool {
	if cfg == nil || cfg.APISettings.Realtime == nil {
		return false
	}
	for _, t := range cfg.APISettings.Realtime.Tables {
		if t == table {
			return true
		}
	}
	return false
}

// RealtimeEnabled reports whether any table streams its changes.
func (cfg *TenantConfig) RealtimeEnabled() bool {
	return cfg != nil && cfg.APISettings.Realtime != nil && len(cfg.APISettings.Realtime.Tables) > 0
}

// ReadableColumns returns the columns of table a client may read.
func (cfg *TenantConfig) ReadableColumns(table string) map[string]bool {
	readable := map[string]bool{}
	for _, t := range cfg.Schema.Tables {
		if t.Name == table {
			for i := range t.Columns {
				if t.Columns[i].IsReadable() {
					readable[t.Columns[i].Name] = true
				}
			}
		}
	}
	return readable
}

// ChangesDDL creates the change log realtime streams from: every change
// of a streamed table, kept for ChangeRetention. AUTOINCREMENT keeps seq
// growing after old rows are pruned, so a client can resume from one.
var ChangesDDL = []string{
	`CREATE TABLE IF NOT EXISTS "_supacraft_changes" (
		"seq"        INTEGER PRIMARY KEY AUTOINCREMENT,
		"table_name" TEXT NOT NULL,
		"op"         TEXT NOT NULL,
		"row"        TEXT NOT NULL,
		"created_at" INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS "_supacraft_changes_created" ON "_supacraft_changes" ("created_at")`,
}

// ChangeRetention is how long changes stay in the log: a client that
// reconnects within it resumes where it left off.
const ChangeRetention = 15 * time.Minute

// Change is one changed row of a streamed table. Row has every column;
// callers send only the readable ones.
type Change struct {
	Seq       int64          `json:"seq"`
	Table     string         `json:"table"`
	Op        string         `json:"op"`
	Row       map[string]any `json:"row"`
	CreatedAt time.Time      `json:"created_at"`
}

// ChangesRecorded, when set, is told that a database logged changes, so
// realtime reads them now instead of on its next poll.
var ChangesRecorded func(dbName string)

// lastPrune is when each database last dropped expired changes (in this
// process), so pruning costs one statement a minute at most.
var lastPrune sync.Map

// EmitChanges records that op changed rows of table: it queues the
// webhooks subscribed to it and logs the rows for realtime. q may be a
// transaction: both then exist only if it commits.
func EmitChanges(ctx context.Context, q Querier, cfg *TenantConfig, table, op string, rows []map[string]any) error {
	if err := EmitWebhooks(ctx, q, cfg, table, op, rows); err != nil {
		return err
	}
	if len(rows) == 0 || !cfg.WantsRealtime(table) {
		return nil
	}
	for _, stmt := range ChangesDDL {
		if _, err := q.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	now := time.Now()
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("realtime: %w", err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO "_supacraft_changes" ("table_name", "op", "row", "created_at") VALUES (?, ?, ?, ?)`,
			table, op, string(data), now.Unix()); err != nil {
			return fmt.Errorf("realtime: %w", err)
		}
	}
	if last, ok := lastPrune.Load(cfg.DBName); !ok || now.Sub(last.(time.Time)) > time.Minute {
		if _, err := q.Exec(ctx, `DELETE FROM "_supacraft_changes" WHERE "created_at" < ?`, now.Add(-ChangeRetention).Unix()); err != nil {
			return fmt.Errorf("realtime: %w", err)
		}
		lastPrune.Store(cfg.DBName, now)
	}
	if ChangesRecorded != nil {
		ChangesRecorded(cfg.DBName)
	}
	return nil
}

// WantsChanges reports whether a change of op on table is sent anywhere.
func (cfg *TenantConfig) WantsChanges(table, op string) bool {
	return cfg.WantsWebhook(table, op) || cfg.WantsRealtime(table)
}

// ReadChanges returns up to limit logged changes after seq, oldest first;
// table, when set, keeps only that table's.
func ReadChanges(ctx context.Context, q Querier, after int64, table string, limit int) ([]Change, error) {
	stmt := `SELECT "seq", "table_name", "op", "row", "created_at" FROM "_supacraft_changes" WHERE "seq" > ?`
	args := []any{after}
	if table != "" {
		stmt += ` AND "table_name" = ?`
		args = append(args, table)
	}
	rows, err := q.Query(ctx, stmt+` ORDER BY "seq" LIMIT ?`, append(args, limit)...)
	if err != nil {
		if noTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var row string
		var created int64
		if err := rows.Scan(&c.Seq, &c.Table, &c.Op, &row, &created); err != nil {
			return nil, err
		}
		if c.Row, err = decodeRow(row); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// decodeRow reads a logged row, keeping whole numbers as integers so they
// compare like the stored values.
func decodeRow(data string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil {
		return nil, err
	}
	for k, v := range row {
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				row[k] = i
			} else {
				row[k], _ = n.Float64()
			}
		}
	}
	return row, nil
}

// ChangeBounds returns the oldest logged change still kept and the last
// one ever logged; oldest is last+1 when none are kept.
func ChangeBounds(ctx context.Context, q Querier) (oldest, last int64, err error) {
	err = q.QueryRow(ctx, `SELECT "seq" FROM "sqlite_sequence" WHERE "name" = '_supacraft_changes'`).Scan(&last)
	if err != nil {
		if noTable(err) || errors.Is(err, sql.ErrNoRows) {
			return 1, 0, nil
		}
		return 0, 0, err
	}
	var min *int64
	if err := q.QueryRow(ctx, `SELECT MIN("seq") FROM "_supacraft_changes"`).Scan(&min); err != nil {
		return 0, 0, err
	}
	if min == nil {
		return last + 1, last, nil
	}
	return *min, last, nil
}

func noTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// ListTenantChanges reads up to limit logged changes after seq of a tenant
// in dataDir (operator commands: CLI, MCP, admin endpoint), readable
// columns only, and the seq of the last change ever logged.
func ListTenantChanges(ctx context.Context, dataDir, id, table string, after int64, limit int) ([]Change, int64, error) {
	sqlDB, cfg, err := OpenTenantDB(ctx, dataDir, id)
	if err != nil {
		return nil, 0, err
	}
	defer sqlDB.Close()
	db := &DB{DB: sqlDB}
	_, last, err := ChangeBounds(ctx, db)
	if err != nil || limit <= 0 {
		return nil, last, err
	}
	list, err := ReadChanges(ctx, db, after, table, limit)
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		list[i].Row = cfg.ReadableRow(list[i].Table, list[i].Row)
	}
	return list, last, nil
}

// ReadableRow returns the readable columns of a row of table.
func (cfg *TenantConfig) ReadableRow(table string, row map[string]any) map[string]any {
	readable := cfg.ReadableColumns(table)
	out := make(map[string]any, len(readable))
	for k, v := range row {
		if readable[k] {
			out[k] = v
		}
	}
	return out
}
