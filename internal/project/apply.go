package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/sqlitedb"
	"github.com/attisvalentin/supacraft/internal/sqlitedb/ddl"
)

// ApplyResult describes what Apply changed.
type ApplyResult struct {
	TenantID   string   `json:"tenant_id"`
	Database   string   `json:"database"`
	Created    bool     `json:"database_created"`
	Migrations []string `json:"migrations"`
	MissingEnv []string `json:"missing_env,omitempty"`
}

// Apply validates the project, migrates its SQLite database (additive only:
// new tables and columns) and writes the compiled tenant config the API
// server reads. A running server picks the change up on its next cache miss.
func (p *Project) Apply(ctx context.Context) (*ApplyResult, error) {
	return p.ApplyTo(ctx, "", false)
}

// ErrTenantTaken is returned when a shared data directory already has a
// tenant with this project's id but a different project name.
var ErrTenantTaken = errors.New("tenant id is used by another project")

// ApplyTo is Apply into dataDir, a central data directory shared by many
// projects ("" = the project's own .supacraft). Only the project's own
// directory is pruned of other tenants' configs. In a shared directory a
// tenant with the same id but another project name is only replaced when
// replace is set, so two projects can't silently overwrite each other.
func (p *Project) ApplyTo(ctx context.Context, dataDir string, replace bool) (*ApplyResult, error) {
	own := dataDir == ""
	if own {
		dataDir = p.DataDir()
	} else if !replace {
		other, err := existingName(dataDir, p.TenantID())
		if err != nil {
			return nil, fmt.Errorf("check tenant %q (pass --replace to overwrite): %w", p.TenantID(), err)
		}
		if other != "" && other != p.Name() {
			return nil, fmt.Errorf("%w: %q already serves project %q (rename this project or pass --replace)", ErrTenantTaken, p.TenantID(), other)
		}
	}
	if issues := p.Validate(); HasErrors(issues) {
		return nil, &ValidationError{Issues: issues}
	}
	compiled, err := p.Compile()
	if err != nil {
		return nil, err
	}

	cfg := &config.Config{DataDir: dataDir}
	prov := sqlitedb.New(cfg)
	dbName := p.TenantID()
	res := &ApplyResult{
		TenantID:   dbName,
		Database:   filepath.Join(cfg.DataDir, dbName+".db"),
		MissingEnv: compiled.MissingEnv,
		Migrations: []string{},
	}

	if res.Created, err = prov.EnsureTenant(ctx, dbName); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if p.AuthEnabled() {
		if err := prov.RunTenantMigrations(ctx, dbName); err != nil {
			return nil, fmt.Errorf("auth migrations: %w", err)
		}
		if err := prov.EnsureJWTSecret(ctx, dbName); err != nil {
			return nil, fmt.Errorf("jwt secret: %w", err)
		}
	}

	tables, err := ddl.ParseSchema(compiled.Payload["schema"])
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if len(tables) > 0 {
		exec := ddl.NewExecutor(cfg)
		conn, err := exec.ConnectTenant(ctx, dbName)
		if err != nil {
			return nil, err
		}
		ops, err := ddl.PlanMigration(ctx, conn, tables)
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("plan migration: %w", err)
		}
		if len(ops) > 0 {
			if err := exec.ExecuteOperations(ctx, dbName, ops); err != nil {
				return nil, fmt.Errorf("migrate: %w", err)
			}
		}
		for _, op := range ops {
			res.Migrations = append(res.Migrations, op.Desc)
		}
	}

	if err := writeTenant(cfg.DataDir, dbName, compiled.Payload, own); err != nil {
		return nil, err
	}
	return res, nil
}

// existingName is the project name of tenant id in dataDir, or "" if there
// is no such tenant.
func existingName(dataDir, id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "tenants", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse existing tenant config: %w", err)
	}
	return cfg.DisplayName, nil
}

// writeTenant writes {dataDir}/tenants/{id}.json atomically. With prune it
// removes every other tenant file, so renaming a project doesn't leave a
// stale tenant in the project's own directory. Never prune a shared one.
func writeTenant(dataDir, id string, payload map[string]any, prune bool) error {
	dir := filepath.Join(dataDir, "tenants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, id+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, id+".json")); err != nil {
		return err
	}

	if !prune {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && e.Name() != id+".json" {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}
