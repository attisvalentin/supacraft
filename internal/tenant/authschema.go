package tenant

import (
	"context"
	"database/sql"
	"fmt"
)

// AuthTokensDDL creates the table of one-time tokens behind password reset
// and email verification links; only their SHA-256 is stored.
var AuthTokensDDL = []string{
	`CREATE TABLE IF NOT EXISTS "_supacraft_auth_tokens" (
		"token_hash" TEXT PRIMARY KEY,
		"user_id"    TEXT NOT NULL,
		"purpose"    TEXT NOT NULL,
		"expires_at" INTEGER NOT NULL,
		"created_at" INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS "_supacraft_auth_tokens_user" ON "_supacraft_auth_tokens" ("user_id", "purpose")`,
}

// authColumns are the built-in users columns the auth handlers query that
// the first users table didn't have (project.authUserColumns declares them).
var authColumns = []string{"is_active", "email_verified", "session_version"}

// ensureAuthSchema adds what the auth handlers need to a database applied
// by an older version, so a server upgrade works before the next deploy.
// Additive only; a missing users table is left to apply.
func ensureAuthSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT "name" FROM pragma_table_info('users')`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(have) == 0 {
		return err
	}
	for _, col := range authColumns {
		if !have[col] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE "users" ADD COLUMN "`+col+`" INTEGER`); err != nil {
				return fmt.Errorf("add users.%s: %w", col, err)
			}
		}
	}
	for _, stmt := range AuthTokensDDL {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
