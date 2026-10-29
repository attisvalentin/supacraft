package errorlog

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// Global is set from main.go so both handler and middleware packages can log errors
// without circular imports.
var Global *ErrorLog

type ErrorLog struct {
	db *sql.DB
}

type LogEntry struct {
	ID           int64  `json:"id"`
	Timestamp    string `json:"timestamp"`
	TenantID     string `json:"tenant_id"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	StatusCode   int    `json:"status_code"`
	ErrorMessage string `json:"error_message"`
}

func Open(dbPath string) (*ErrorLog, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS error_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			tenant_id TEXT,
			method TEXT,
			path TEXT,
			status_code INTEGER,
			error_message TEXT
		)
	`); err != nil {
		db.Close()
		return nil, err
	}

	return &ErrorLog{db: db}, nil
}

func (el *ErrorLog) Log(tenantID, method, path string, statusCode int, errorMsg string) {
	go func() {
		_, err := el.db.Exec(
			`INSERT INTO error_logs (tenant_id, method, path, status_code, error_message) VALUES (?, ?, ?, ?, ?)`,
			tenantID, method, path, statusCode, errorMsg,
		)
		if err != nil {
			slog.Error("failed to write error log", "error", err)
		}
	}()
}

func (el *ErrorLog) Query(ctx context.Context, tenantID string, limit, offset int) ([]LogEntry, int, error) {
	var total int
	err := el.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM error_logs WHERE tenant_id = ?`, tenantID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := el.db.QueryContext(ctx,
		`SELECT id, timestamp, tenant_id, method, path, status_code, error_message FROM error_logs WHERE tenant_id = ? ORDER BY timestamp DESC LIMIT ? OFFSET ?`,
		tenantID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.TenantID, &e.Method, &e.Path, &e.StatusCode, &e.ErrorMessage); err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}

	if entries == nil {
		entries = []LogEntry{}
	}

	return entries, total, nil
}

func (el *ErrorLog) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := el.db.Exec(`DELETE FROM error_logs WHERE timestamp < datetime('now', '-7 days')`)
				if err != nil {
					slog.Error("failed to clean up error logs", "error", err)
				}
			}
		}
	}()
}

func (el *ErrorLog) Close() error {
	return el.db.Close()
}
