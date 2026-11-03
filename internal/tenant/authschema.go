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
	// A pending OAuth sign-in: the state sent to the provider (hashed),
	// the PKCE verifier and where to return.
	// app_challenge is the app's own PKCE challenge: the login code is only
	// exchanged with its verifier, so it is useless in another browser.
	`CREATE TABLE IF NOT EXISTS "_supacraft_oauth_states" (
		"state_hash"    TEXT PRIMARY KEY,
		"provider"      TEXT NOT NULL,
		"verifier"      TEXT NOT NULL,
		"app_challenge" TEXT NOT NULL,
		"redirect_to"   TEXT NOT NULL,
		"expires_at"    INTEGER NOT NULL
	)`,
	// A login code from a callback, waiting for the app's exchange.
	`CREATE TABLE IF NOT EXISTS "_supacraft_oauth_codes" (
		"code_hash"     TEXT PRIMARY KEY,
		"user_id"       TEXT NOT NULL,
		"app_challenge" TEXT NOT NULL,
		"expires_at"    INTEGER NOT NULL
	)`,
	// Which provider account belongs to which user.
	`CREATE TABLE IF NOT EXISTS "_supacraft_identities" (
		"provider"   TEXT NOT NULL,
		"subject"    TEXT NOT NULL,
		"user_id"    TEXT NOT NULL,
		"email"      TEXT NOT NULL DEFAULT '',
		"created_at" INTEGER NOT NULL,
		PRIMARY KEY ("provider", "subject")
	)`,
}

// authColumns are the built-in users columns the auth handlers query that
// the first users table didn't have (project.authUserColumns declares them).
var authColumns = []struct{ name, sqlType string }{
	{"is_active", "INTEGER"},
	{"email_verified", "INTEGER"},
	{"session_version", "INTEGER"},
	{"role", "TEXT"},
}

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
		if !have[col.name] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE "users" ADD COLUMN "`+col.name+`" `+col.sqlType); err != nil {
				return fmt.Errorf("add users.%s: %w", col.name, err)
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
