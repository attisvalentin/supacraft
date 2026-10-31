package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/attisvalentin/supacraft/internal/envref"
	"github.com/joho/godotenv"
)

// authUserColumns mirrors the users table created by the auth migration. They
// are added to the schema when auth is enabled so the API knows about them
// even if supacraft.json doesn't declare a users table.
var authUserColumns = []map[string]any{
	{"name": "id", "type": "uuid", "readable": true, "creatable": false, "updatable": false},
	{"name": "email", "type": "varchar"},
	{"name": "password", "type": "varchar", "readable": false, "creatable": true, "updatable": true},
	// Clients must not activate themselves; flip it from a function or cron.
	{"name": "is_active", "type": "boolean", "creatable": false, "updatable": false},
	{"name": "created_at", "type": "timestamp", "readable": true, "creatable": false, "updatable": false},
	{"name": "updated_at", "type": "timestamp", "readable": true, "creatable": false, "updatable": false},
}

// Env returns the variables visible to the project: .env in the project dir,
// overridden by the process environment.
func (p *Project) Env() map[string]string {
	if p.remote {
		return map[string]string{}
	}
	env, _ := godotenv.Read(filepath.Join(p.Dir, ".env"))
	if env == nil {
		env = map[string]string{}
	}
	for k := range env {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	return env
}

// Compiled is the tenant config the API server reads, plus the env references
// that could not be resolved.
type Compiled struct {
	Payload    map[string]any
	MissingEnv []string
}

// Compile turns supacraft.json into the tenant config payload the API server
// reads. Entities are identified by name, so table/column UUIDs are set to
// their names, and api_settings.tables / function_id keep using names.
// "${NAME}" strings anywhere in api_settings are replaced from Env().
func (p *Project) Compile() (*Compiled, error) {
	env := p.Env()
	return p.compile(func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	})
}

// CompileKeepingRefs compiles for a central server: "${NAME}" references stay
// in the payload and the server resolves them from its own secret store when
// it loads the config, so values never pass through the project or the
// compiled file. MissingEnv lists references that have no name in available.
func (p *Project) CompileKeepingRefs(available map[string]bool) (*Compiled, error) {
	out, err := p.compile(nil)
	if err != nil {
		return nil, err
	}
	for _, name := range envref.Names(out.Payload["api_settings"]) {
		if !available[name] {
			out.MissingEnv = append(out.MissingEnv, name)
		}
	}
	return out, nil
}

// compile builds the payload; lookup resolves references (nil keeps them).
func (p *Project) compile(lookup func(string) (string, bool)) (*Compiled, error) {
	raw, err := deepCopy(p.Raw)
	if err != nil {
		return nil, err
	}

	schema := asMap(raw["schema"])
	tables := asSlice(schema["tables"])
	if p.AuthEnabled() {
		tables = ensureAuthUsers(tables)
	}
	for _, t := range tables {
		tm := asMap(t)
		setDefault(tm, "uuid", tm["name"])
		for _, c := range asSlice(tm["columns"]) {
			cm := asMap(c)
			setDefault(cm, "uuid", cm["name"])
		}
	}
	schema["tables"] = tables
	if schema["relations"] == nil {
		schema["relations"] = []any{}
	}

	functions := asMap(raw["functions"])
	for name, fn := range functions {
		setDefault(asMap(fn), "name", name)
	}
	cronjobs := asMap(raw["cronjobs"])
	for name, job := range cronjobs {
		jm := asMap(job)
		setDefault(jm, "name", name)
		setDefault(jm, "enabled", true)
	}

	missing := map[string]struct{}{}
	apiSettings := asMap(raw["api_settings"])
	if lookup != nil {
		envref.Resolve(apiSettings, lookup, missing)
	}

	out := &Compiled{
		Payload: map[string]any{
			"db_name":      p.TenantID(),
			"display_name": p.Name(),
			"enabled":      true,
			"auth_enabled": p.AuthEnabled(),
			"schema":       schema,
			"api_settings": apiSettings,
			"functions":    functions,
			"cronjobs":     cronjobs,
			"updated_at":   time.Now().UTC().Format(time.RFC3339),
		},
	}
	for k := range missing {
		out.MissingEnv = append(out.MissingEnv, k)
	}
	sort.Strings(out.MissingEnv)
	return out, nil
}

func ensureAuthUsers(tables []any) []any {
	for _, t := range tables {
		tm := asMap(t)
		if tm["name"] != "users" {
			continue
		}
		// The built-in auth columns always win over a declared column of the
		// same name, so a declaration cannot make password readable or
		// is_active client-settable.
		builtin := map[string]bool{}
		var cols []any
		for _, c := range authUserColumns {
			builtin[c["name"].(string)] = true
			cols = append(cols, copyMap(c))
		}
		for _, c := range asSlice(tm["columns"]) {
			if n, _ := asMap(c)["name"].(string); !builtin[n] {
				cols = append(cols, c)
			}
		}
		tm["columns"] = cols
		return tables
	}
	var cols []any
	for _, c := range authUserColumns {
		cols = append(cols, copyMap(c))
	}
	return append([]any{map[string]any{"name": "users", "exposed": true, "columns": cols}}, tables...)
}

func deepCopy(m map[string]any) (map[string]any, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("copy config: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("copy config: %w", err)
	}
	return out, nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func setDefault(m map[string]any, key string, value any) {
	if m == nil {
		return
	}
	if v, ok := m[key]; !ok || v == nil || v == "" {
		m[key] = value
	}
}

// asMap returns v as a map, or an empty map when it isn't one. Empty arrays ([])
// are treated as empty maps.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
