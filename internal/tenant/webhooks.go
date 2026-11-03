package tenant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Webhook sends the changes of some tables to a URL. Events are
// "<table>.insert", "<table>.update", "<table>.delete" or "<table>.*".
// Secret (a "${NAME}" reference) signs each delivery.
type Webhook struct {
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Secret      string   `json:"secret,omitempty"`
	MaxAttempts int      `json:"max_attempts,omitempty"`
}

// Table change operations.
const (
	OpInsert = "insert"
	OpUpdate = "update"
	OpDelete = "delete"
)

// WebhookJobPrefix marks a job that delivers a webhook rather than running
// a function; function names can't contain ':'.
const WebhookJobPrefix = "webhook:"

// webhookBatch is how many rows one delivery carries.
const webhookBatch = 100

// Wants reports whether the webhook subscribes to op on table.
func (w Webhook) Wants(table, op string) bool {
	for _, e := range w.Events {
		if e == table+"."+op || e == table+".*" {
			return true
		}
	}
	return false
}

// WantsWebhook reports whether any webhook of cfg subscribes to op on
// table, so callers only read rows they need to send.
func (cfg *TenantConfig) WantsWebhook(table, op string) bool {
	if cfg == nil {
		return false
	}
	for _, w := range cfg.APISettings.Webhooks {
		if w.Wants(table, op) {
			return true
		}
	}
	return false
}

// EmitWebhooks queues a delivery to every webhook subscribed to op on
// table, carrying rows (readable columns only, 100 per delivery). q may be
// a transaction: the deliveries then exist only if it commits.
func EmitWebhooks(ctx context.Context, q Querier, cfg *TenantConfig, table, op string, rows []map[string]any) error {
	if len(rows) == 0 || !cfg.WantsWebhook(table, op) {
		return nil
	}
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
	out := make([]any, len(rows))
	for i, row := range rows {
		r := map[string]any{}
		for k, v := range row {
			if readable[k] {
				r[k] = v
			}
		}
		out[i] = r
	}
	for name, w := range cfg.APISettings.Webhooks {
		if !w.Wants(table, op) {
			continue
		}
		for start := 0; start < len(out); start += webhookBatch {
			end := min(start+webhookBatch, len(out))
			args := map[string]any{
				"id":         uuid.NewString(),
				"event":      table + "." + op,
				"table":      table,
				"created_at": time.Now().UTC().Format(time.RFC3339),
				"rows":       out[start:end],
			}
			if _, err := EnqueueJob(ctx, q, cfg.DBName, WebhookJobPrefix+name, args, 0, w.MaxAttempts); err != nil {
				return fmt.Errorf("webhook %s: %w", name, err)
			}
		}
	}
	return nil
}

// WebhookName returns the webhook a job delivers, if it is a delivery.
func WebhookName(function string) (string, bool) {
	return strings.CutPrefix(function, WebhookJobPrefix)
}

// WriteWithWebhooks runs write (which changes table and returns the
// changed rows) and queues the webhooks for them atomically: when q is a
// plain pool and a webhook wants op on table, both run in one transaction,
// so a delivery exists exactly when the change commits. Inside a caller's
// transaction they simply join it.
func WriteWithWebhooks(ctx context.Context, q Querier, cfg *TenantConfig, table, op string, write func(Querier) ([]map[string]any, error)) ([]map[string]any, error) {
	db, isPool := q.(*DB)
	if !isPool || !cfg.WantsWebhook(table, op) {
		rows, err := write(q)
		if err != nil {
			return nil, err
		}
		return rows, EmitWebhooks(ctx, q, cfg, table, op, rows)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := write(tx)
	if err != nil {
		return nil, err
	}
	if err := EmitWebhooks(ctx, tx, cfg, table, op, rows); err != nil {
		return nil, err
	}
	return rows, tx.Commit()
}
