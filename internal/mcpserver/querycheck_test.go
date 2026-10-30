package mcpserver

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCheckQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	rw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE api_config (jwt_secret TEXT)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT UNIQUE, password TEXT)`,
		`CREATE INDEX users_email_pw ON users (email, password)`,
		`CREATE TABLE posts (id TEXT PRIMARY KEY, title TEXT, author_id TEXT)`,
		`INSERT INTO api_config VALUES ('secret')`,
		`INSERT INTO users VALUES ('u1', 'a@b.co', 'hash')`,
	} {
		if _, err := rw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	rw.Close()

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	allowed := []string{
		`SELECT * FROM posts`,
		`SELECT id, email FROM users`,
		`SELECT count(*) FROM users`,
		`SELECT p.title, u.email FROM posts p JOIN users u ON u.id = p.author_id`,
		`SELECT name FROM pragma_table_info('users')`,
		`SELECT * FROM posts;`,
		`SELECT ';' AS semi -- trailing comment`,
	}
	for _, q := range allowed {
		if err := checkQuery(ctx, db, q); err != nil {
			t.Errorf("rejected %q: %v", q, err)
		}
	}

	refused := []string{
		`SELECT * FROM api_config`,
		`SELECT jwt_secret AS x FROM api_config`,
		`SELECT * FROM users`,
		`SELECT hex(password) FROM users`,
		`SELECT id FROM users WHERE password LIKE 'h%'`,
		`WITH c AS (SELECT password AS p FROM users) SELECT p FROM c`,
		`SELECT (SELECT jwt_secret FROM api_config) FROM posts`,
		`SELECT email FROM users INDEXED BY users_email_pw`,
		`SELECT data FROM sqlite_dbpage`,
		`SELECT * FROM "SQLITE_DBPAGE"`,
		`SELECT 1; SELECT * FROM api_config`,
	}
	for _, q := range refused {
		if err := checkQuery(ctx, db, q); err == nil {
			t.Errorf("allowed %q", q)
		}
	}
	if err := checkQuery(ctx, db, `SELECT password FROM users`); !errors.Is(err, errProtected) {
		t.Errorf("unexpected error: %v", err)
	}
}
