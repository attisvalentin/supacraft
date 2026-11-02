// Package testrun loads a project's seed data and runs its tests (the
// "seed" and "tests" sections of supacraft.json).
package testrun

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/attisvalentin/supacraft/internal/envref"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/query"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// SeedResult says what a seed added.
type SeedResult struct {
	Users int `json:"users_added"`
	// Rows counts added rows per table.
	Rows map[string]int `json:"rows_added"`
	// Skipped lists tables without a primary key that already had rows
	// (their seed rows are only added to an empty table).
	Skipped []string `json:"skipped,omitempty"`
}

// Seed adds the seed data to db. It can run again: users are matched by
// email and rows by primary key (a row without one gets an id derived from
// its table and position), so existing data is never duplicated or
// changed. env resolves "${NAME}" passwords.
func Seed(ctx context.Context, db *sql.DB, cfg *tenant.TenantConfig, seed *project.Seed, env func(string) (string, bool)) (*SeedResult, error) {
	res := &SeedResult{Rows: map[string]int{}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, u := range seed.Users {
		added, err := seedUser(ctx, tx, cfg, u, env)
		if err != nil {
			return nil, fmt.Errorf("seed user %v: %w", u["email"], err)
		}
		if added {
			res.Users++
		}
	}

	tables := make([]string, 0, len(seed.Tables))
	for name := range seed.Tables {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	for _, name := range tables {
		table := findTable(cfg, name)
		if table == nil {
			return nil, fmt.Errorf("seed: unknown table %q", name)
		}
		pk := primaryKey(table)
		if pk == "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+query.QuoteIdent(name)).Scan(&n); err != nil {
				return nil, err
			}
			if n > 0 {
				res.Skipped = append(res.Skipped, name)
				continue
			}
		}
		for i, row := range seed.Tables[name] {
			data := make(map[string]any, len(row)+1)
			for k, v := range row {
				data[k] = sqlValue(v)
			}
			if pk != "" && data[pk] == nil {
				data[pk] = uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("supacraft-seed/%s/%d", name, i))).String()
			}
			stmt, args := query.BuildInsert(name, data)
			r, err := tx.ExecContext(ctx, strings.Replace(stmt, "INSERT INTO", "INSERT OR IGNORE INTO", 1), args...)
			if err != nil {
				return nil, fmt.Errorf("seed %s row %d: %w", name, i, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Rows[name]++
			}
		}
	}
	return res, tx.Commit()
}

func seedUser(ctx context.Context, tx *sql.Tx, cfg *tenant.TenantConfig, u map[string]any, env func(string) (string, bool)) (bool, error) {
	email, _ := u["email"].(string)
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "users" WHERE "email" = ?`, email).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		return false, nil
	}
	password, _ := u["password"].(string)
	if m := envref.Ref.FindStringSubmatch(password); m != nil {
		v, ok := env(m[1])
		if !ok {
			return false, fmt.Errorf("password: ${%s} is not set in .env", m[1])
		}
		password = v
	}
	if password == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return false, err
		}
		password = hex.EncodeToString(b)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	data := map[string]any{}
	for k, v := range u {
		data[k] = sqlValue(v)
	}
	data["id"] = uuid.NewString()
	data["password"] = string(hash)
	if _, set := data["email_verified"]; !set {
		data["email_verified"] = true
	}
	if role, _ := data["role"].(string); role == "" {
		if def := cfg.APISettings.Auth.DefaultRoleName(); def != "" {
			data["role"] = def
		} else {
			delete(data, "role")
		}
	}
	stmt, args := query.BuildInsert("users", data)
	_, err = tx.ExecContext(ctx, stmt, args...)
	return err == nil, err
}

// sqlValue stores objects and arrays as JSON text, like the API does.
func sqlValue(v any) any {
	switch v.(type) {
	case map[string]any, []any:
		data, _ := json.Marshal(v)
		return string(data)
	}
	return v
}

func findTable(cfg *tenant.TenantConfig, name string) *tenant.SchemaTable {
	for i := range cfg.Schema.Tables {
		if cfg.Schema.Tables[i].Name == name {
			return &cfg.Schema.Tables[i]
		}
	}
	return nil
}

// primaryKey is the table's primary key column, "" when it has none.
func primaryKey(t *tenant.SchemaTable) string {
	for _, c := range t.Columns {
		if c.Primary || strings.EqualFold(c.Type, "primary") {
			return c.Name
		}
	}
	return ""
}

// SeedLocal applies the project to its own .supacraft database and adds the
// seed data. With reset it first deletes every row of the project's tables
// (users and their tokens included; uploaded files stay), so the database
// holds exactly the seed. A running dev server keeps working.
func SeedLocal(ctx context.Context, p *project.Project, reset bool) (*SeedResult, error) {
	if _, err := p.Apply(ctx); err != nil {
		return nil, err
	}
	cfg, err := p.TenantConfig()
	if err != nil {
		return nil, err
	}
	seed, err := p.SeedData()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(p.DataDir(), p.TenantID()+".db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if reset {
		if err := clearData(ctx, db, cfg); err != nil {
			return nil, fmt.Errorf("reset: %w", err)
		}
	}
	env := p.Env()
	return Seed(ctx, db, cfg, seed, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
}

// clearData deletes the rows of every schema table and the auth tokens in one
// transaction; foreign keys are checked at commit, when all rows are gone.
func clearData(ctx context.Context, db *sql.DB, cfg *tenant.TenantConfig) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	tables := []string{}
	for _, t := range cfg.Schema.Tables {
		tables = append(tables, t.Name)
	}
	if cfg.AuthEnabled {
		tables = append(tables, "_supacraft_auth_tokens")
	}
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+query.QuoteIdent(t)); err != nil && !strings.Contains(err.Error(), "no such table") {
			return err
		}
	}
	return tx.Commit()
}
