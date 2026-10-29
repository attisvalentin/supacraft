package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/joho/godotenv"
)

// authUserColumns mirrors the users table created by the auth migration. They
// are added to the schema when auth is enabled so the API knows about them
// even if supacraft.json doesn't declare a users table.
var authUserColumns = []map[string]any{
	{"name": "id", "type": "uuid", "readable": true, "creatable": false, "updatable": false},
	{"name": "email", "type": "varchar"},
	{"name": "password", "type": "varchar", "readable": false, "creatable": true, "updatable": true},
	{"name": "is_active", "type": "boolean"},
	{"name": "created_at", "type": "timestamp", "readable": true, "creatable": false, "updatable": false},
	{"name": "updated_at", "type": "timestamp", "readable": true, "creatable": false, "updatable": false},
}

// envRef matches a string that is entirely an env reference: "${NAME}".
var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Env returns the variables visible to the project: .env in the project dir,
// overridden by the process environment.
func (p *Project) Env() map[string]string {
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

	env := p.Env()
	missing := map[string]struct{}{}
	apiSettings := resolveEnv(asMap(raw["api_settings"]), env, missing).(map[string]any)

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
		existing := map[string]bool{}
		for _, c := range asSlice(tm["columns"]) {
			if n, ok := asMap(c)["name"].(string); ok {
				existing[n] = true
			}
		}
		var cols []any
		for _, c := range authUserColumns {
			if !existing[c["name"].(string)] {
				cols = append(cols, copyMap(c))
			}
		}
		tm["columns"] = append(cols, asSlice(tm["columns"])...)
		return tables
	}
	var cols []any
	for _, c := range authUserColumns {
		cols = append(cols, copyMap(c))
	}
	return append([]any{map[string]any{"name": "users", "exposed": true, "columns": cols}}, tables...)
}

func resolveEnv(v any, env map[string]string, missing map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = resolveEnv(child, env, missing)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = resolveEnv(child, env, missing)
		}
		return t
	case string:
		m := envRef.FindStringSubmatch(t)
		if m == nil {
			return t
		}
		if val, ok := env[m[1]]; ok {
			return val
		}
		missing[m[1]] = struct{}{}
		return ""
	default:
		return v
	}
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
