package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

var (
	// ErrUserNotFound: no user has that email.
	ErrUserNotFound = errors.New("no user with that email")
	// ErrNoRoles: the tenant has no auth or defines no roles.
	ErrNoRoles = errors.New("the project defines no roles (set api_settings.auth.roles and auth_enabled)")
)

// UserInfo is a user as the operator sees it; never the password hash.
type UserInfo struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Role     string `json:"role"` // effective role; "" when it has none
	Active   bool   `json:"active"`
	Verified bool   `json:"verified"`
	Created  string `json:"created_at"`
}

// UnknownRoleError: the role is not in api_settings.auth.roles.
type UnknownRoleError struct {
	Role  string
	Roles []string
}

func (e *UnknownRoleError) Error() string {
	return fmt.Sprintf("unknown role %q (defined: %s)", e.Role, strings.Join(e.Roles, ", "))
}

// openUsers is OpenTenantDB for a tenant with auth, returning its auth
// settings.
func openUsers(ctx context.Context, dataDir, id string) (*sql.DB, AuthSettings, error) {
	db, cfg, err := OpenTenantDB(ctx, dataDir, id)
	if err != nil {
		return nil, AuthSettings{}, err
	}
	if !cfg.AuthEnabled {
		db.Close()
		return nil, AuthSettings{}, ErrNoRoles
	}
	// The role column may be newer than the database.
	if err := ensureAuthSchema(ctx, db); err != nil {
		db.Close()
		return nil, AuthSettings{}, err
	}
	return db, cfg.APISettings.Auth, nil
}

// OpenTenantDB opens a tenant's database in dataDir (a central data
// directory or a project's .supacraft) for an operator command, with its
// config. A running server can keep serving meanwhile: SQLite WAL lets a
// second writer wait its turn.
func OpenTenantDB(ctx context.Context, dataDir, id string) (*sql.DB, *TenantConfig, error) {
	if !ValidTenantID(id) {
		return nil, nil, ErrTenantNotFound
	}
	store := &FileStore{dir: filepath.Join(dataDir, "tenants")}
	cfg, err := store.GetTenantConfig(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(dataDir, cfg.DBName+".db"), RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	return db, cfg, nil
}

const userColumns = `"id", "email", "role", COALESCE("is_active", 1), COALESCE("email_verified", 0), COALESCE("created_at", '')`

func scanUser(row interface{ Scan(...any) error }, auth AuthSettings) (UserInfo, error) {
	var u UserInfo
	var role *string
	err := row.Scan(&u.ID, &u.Email, &role, &u.Active, &u.Verified, &u.Created)
	u.Role = auth.EffectiveRole(role)
	return u, err
}

// ListUsers returns a page of a tenant's users, oldest first, and the total.
func ListUsers(ctx context.Context, dataDir, id string, limit, offset int) ([]UserInfo, int, error) {
	db, auth, err := openUsers(ctx, dataDir, id)
	if err != nil {
		if errors.Is(err, ErrNoRoles) {
			return []UserInfo{}, 0, nil
		}
		return nil, 0, err
	}
	defer db.Close()
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "users"`).Scan(&total); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return []UserInfo{}, 0, nil
		}
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+userColumns+` FROM "users" ORDER BY "created_at", "email" LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		u, err := scanUser(rows, auth)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// SetUserRole gives the user with email one of the tenant's roles. Tokens
// already issued carry the new role from their next request.
func SetUserRole(ctx context.Context, dataDir, id, email, role string) (*UserInfo, error) {
	db, auth, err := openUsers(ctx, dataDir, id)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !auth.RolesEnabled() {
		return nil, ErrNoRoles
	}
	if !auth.HasRole(role) {
		return nil, &UnknownRoleError{Role: role, Roles: auth.Roles}
	}
	res, err := db.ExecContext(ctx, `UPDATE "users" SET "role" = ?, "updated_at" = datetime('now') WHERE "email" = ?`, role, email)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrUserNotFound
	}
	u, err := scanUser(db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM "users" WHERE "email" = ?`, email), auth)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
