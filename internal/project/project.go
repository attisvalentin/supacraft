// Package project is the local, file-based source of truth for a SupaCraft
// backend. A project is a directory holding supacraft.json (schema, api_settings,
// functions, cronjobs) plus an optional .env for secrets. Runtime state — the
// compiled tenant config and the SQLite database — lives under .supacraft/.
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// FileName is the project config file at the project root.
	FileName = "supacraft.json"
	// StateDir holds compiled tenant configs, SQLite files and storage.
	StateDir = ".supacraft"
)

// ErrNotFound is returned when no supacraft.json exists in the directory.
var ErrNotFound = errors.New("no " + FileName + " found (run: supacraft init)")

// Project is a loaded supacraft.json. Raw keeps the file contents verbatim so
// fields SupaCraft doesn't model in Go (e.g. enum_values, length) round-trip.
type Project struct {
	Dir string
	Raw map[string]any
}

// Load reads supacraft.json from dir.
func Load(dir string) (*Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(abs, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return &Project{Dir: abs, Raw: raw}, nil
}

// Save writes Raw back to supacraft.json atomically, pretty-printed.
func (p *Project) Save() error {
	data, err := marshalOrdered(p.Raw)
	if err != nil {
		return err
	}
	path := filepath.Join(p.Dir, FileName)
	tmp, err := os.CreateTemp(p.Dir, ".supacraft-*.json")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// topLevelOrder keeps supacraft.json readable: identity first, then sections.
var topLevelOrder = []string{"name", "auth_enabled", "schema", "api_settings", "functions", "cronjobs"}

// marshalOrdered pretty-prints raw with the top-level keys in topLevelOrder
// (unknown keys follow, alphabetically). Nested objects use Go's sorted order.
func marshalOrdered(raw map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, k := range topLevelOrder {
		if _, ok := raw[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range raw {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		vb, err := json.MarshalIndent(raw[k], "  ", "  ")
		if err != nil {
			return nil, err
		}
		buf.WriteString("  ")
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(vb)
		if i < len(keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// Name is the project's display name.
func (p *Project) Name() string {
	s, _ := p.Raw["name"].(string)
	return s
}

// TenantID is the id the API server addresses this project by. It doubles as
// the SQLite file name.
func (p *Project) TenantID() string {
	return Slug(p.Name())
}

// AuthEnabled reports whether the built-in users/auth endpoints are on.
func (p *Project) AuthEnabled() bool {
	b, _ := p.Raw["auth_enabled"].(bool)
	return b
}

// DataDir is where compiled configs and SQLite databases live.
func (p *Project) DataDir() string {
	return filepath.Join(p.Dir, StateDir)
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9_]+`)

// Slug turns a display name into a safe identifier: lowercase, [a-z0-9_].
func Slug(name string) string {
	s := slugInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "app"
	}
	return s
}

// New returns a minimal project skeleton.
func New(dir, name string, authEnabled bool) *Project {
	return &Project{
		Dir: dir,
		Raw: map[string]any{
			"name":         name,
			"auth_enabled": authEnabled,
			"schema":       map[string]any{"tables": []any{}, "relations": []any{}},
			"api_settings": map[string]any{
				"crud_endpoints": map[string]any{"query": false, "create": false, "update": false, "delete": false},
				"storage":        map[string]any{"driver": "local"},
			},
			"functions": map[string]any{},
			"cronjobs":  map[string]any{},
		},
	}
}
