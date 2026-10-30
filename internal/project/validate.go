package project

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/attisvalentin/supacraft/internal/pipeline"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/robfig/cron/v3"
)

// Issue is one validation finding, addressed by a JSON path into supacraft.json.
type Issue struct {
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "error" | "warning"
}

func (i Issue) String() string { return fmt.Sprintf("%s %s: %s", i.Severity, i.Path, i.Message) }

// ValidationError wraps the issues that blocked an apply.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	var lines []string
	for _, i := range e.Issues {
		if i.Severity == "error" {
			lines = append(lines, "  "+i.String())
		}
	}
	return "invalid " + FileName + ":\n" + strings.Join(lines, "\n")
}

// HasErrors reports whether any issue is an error (warnings don't block).
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

var (
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	// privilegedColumn flags users columns that usually grant access.
	privilegedColumn = regexp.MustCompile(`(?i)^(is_)?(admin|role|roles|permissions?|staff|superuser|verified|plan|tier|credits?|balance)$`)
	envKeyRe         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	assignRe         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	columnTypes      = setOf("primary", "uuid", "int", "bigint", "smallint", "tinyint", "bigserial",
		"decimal", "numeric", "real", "double", "float", "varchar", "char", "text", "boolean",
		"timestamp", "timestamptz", "date", "time", "interval", "json", "jsonb", "bytea",
		"blob", "binary", "enum")
	relationTypes = setOf("one-to-one", "one-to-many", "many-to-many")
	httpMethods   = setOf("GET", "POST", "PUT", "PATCH", "DELETE", "PRIVATE")
	stepTypes     = setOf("db.select", "db.aggregate", "db.insert", "db.update", "db.delete", "if", "foreach",
		"transaction", "try", "fail",
		"set_variable", "transform.sum", "transform.count", "transform.map", "transform.filter",
		"transform.math", "transform.datetime", "response", "function.call", "http.request", "email.send")
	storageDrivers = setOf("local", "s3", "ftp", "sftp", "gcs", "azure")
)

type validator struct {
	issues    []Issue
	tables    map[string]map[string]bool // table → columns
	functions map[string]bool
	templates map[string]bool
	relations [][2]string // table pairs, from → to
}

func (v *validator) related(a, b string) bool {
	for _, r := range v.relations {
		if (r[0] == a && r[1] == b) || (r[0] == b && r[1] == a) {
			return true
		}
	}
	return false
}

func (v *validator) errorf(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...), Severity: "error"})
}

func (v *validator) warnf(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...), Severity: "warning"})
}

// Validate checks supacraft.json for structural and referential errors: column
// types, relations and api_settings pointing at real tables, step types and
// their required fields, function.call targets, cron expressions and
// transform.math syntax. It also confirms the compiled config decodes into
// the server's types.
func (p *Project) Validate() []Issue {
	v := &validator{
		tables:    map[string]map[string]bool{},
		functions: map[string]bool{},
		templates: map[string]bool{},
	}
	raw := p.Raw

	if strings.TrimSpace(p.Name()) == "" {
		v.errorf("name", "project name is required")
	} else if id := p.TenantID(); len(id) > MaxTenantIDLength {
		v.errorf("name", "too long: its id %q must be at most %d characters", id, MaxTenantIDLength)
	}
	for _, key := range []string{"schema", "api_settings", "functions", "cronjobs"} {
		if val, ok := raw[key]; ok && val != nil {
			if _, isMap := val.(map[string]any); !isMap {
				if arr, isArr := val.([]any); !(isArr && len(arr) == 0) {
					v.errorf(key, "must be an object")
				}
			}
		}
	}

	v.schema(asMap(raw["schema"]), p.AuthEnabled())
	for name := range asMap(raw["functions"]) {
		v.functions[name] = true
	}
	settings := asMap(raw["api_settings"])
	for name := range asMap(asMap(settings["email"])["templates"]) {
		v.templates[name] = true
	}
	v.apiSettings(settings)
	v.functionsSection(asMap(raw["functions"]))
	v.cronjobs(asMap(raw["cronjobs"]))

	if !HasErrors(v.issues) {
		v.decodes(p)
	}

	sort.SliceStable(v.issues, func(i, j int) bool {
		return v.issues[i].Severity == "error" && v.issues[j].Severity != "error"
	})
	return v.issues
}

func (v *validator) schema(schema map[string]any, authEnabled bool) {
	if authEnabled {
		v.tables["users"] = map[string]bool{}
		for _, c := range authUserColumns {
			v.tables["users"][c["name"].(string)] = true
		}
	}
	for i, t := range asSlice(schema["tables"]) {
		path := fmt.Sprintf("schema.tables[%d]", i)
		tm, ok := t.(map[string]any)
		if !ok {
			v.errorf(path, "must be an object")
			continue
		}
		name, _ := tm["name"].(string)
		if !identRe.MatchString(name) {
			v.errorf(path+".name", "%q is not a valid table name (letters, digits, _; max 63)", name)
			continue
		}
		if _, dup := v.tables[name]; dup && !(authEnabled && name == "users") {
			v.errorf(path+".name", "duplicate table %q", name)
		}
		if v.tables[name] == nil {
			v.tables[name] = map[string]bool{}
		}
		cols := asSlice(tm["columns"])
		if len(cols) == 0 && !(authEnabled && name == "users") {
			v.errorf(path+".columns", "table %q has no columns", name)
		}
		seen := map[string]bool{}
		for j, c := range cols {
			cpath := fmt.Sprintf("%s.columns[%d]", path, j)
			cm, ok := c.(map[string]any)
			if !ok {
				v.errorf(cpath, "must be an object")
				continue
			}
			cname, _ := cm["name"].(string)
			if !identRe.MatchString(cname) {
				v.errorf(cpath+".name", "%q is not a valid column name", cname)
				continue
			}
			if seen[cname] {
				v.errorf(cpath+".name", "duplicate column %q in table %q", cname, name)
			}
			seen[cname] = true
			v.tables[name][cname] = true

			ctype, _ := cm["type"].(string)
			if !columnTypes[strings.ToLower(ctype)] {
				v.errorf(cpath+".type", "unknown column type %q (valid: %s)", ctype, keys(columnTypes))
			}
			if strings.EqualFold(ctype, "enum") && len(asSlice(cm["enum_values"])) == 0 {
				v.errorf(cpath+".enum_values", "enum column %q needs enum_values", cname)
			}
			if authEnabled && name == "users" && privilegedColumn.MatchString(cname) {
				if c, set := cm["creatable"].(bool); !set || c {
					v.warnf(cpath+".creatable", "users.%s looks like a permission; anyone can set it at /auth/register unless it has \"creatable\": false", cname)
				}
				if u, set := cm["updatable"].(bool); !set || u {
					v.warnf(cpath+".updatable", "users.%s looks like a permission; clients can change it through /update unless it has \"updatable\": false", cname)
				}
			}
		}
	}
	for i, r := range asSlice(schema["relations"]) {
		path := fmt.Sprintf("schema.relations[%d]", i)
		rm := asMap(r)
		from, _ := asMap(rm["from"])["table"].(string)
		to, _ := asMap(rm["to"])["table"].(string)
		v.relations = append(v.relations, [2]string{from, to})
		for _, end := range []string{"from", "to"} {
			em := asMap(rm[end])
			table, _ := em["table"].(string)
			column, _ := em["column"].(string)
			if cols, ok := v.tables[table]; !ok {
				v.errorf(path+"."+end+".table", "unknown table %q", table)
			} else if !cols[column] {
				v.errorf(path+"."+end+".column", "unknown column %q in table %q", column, table)
			}
		}
		if t, _ := rm["type"].(string); !relationTypes[t] {
			v.errorf(path+".type", "unknown relation type %q (valid: %s)", t, keys(relationTypes))
		}
	}
}

func (v *validator) apiSettings(s map[string]any) {
	for table := range asMap(s["tables"]) {
		if _, ok := v.tables[table]; !ok {
			v.errorf("api_settings.tables."+table, "unknown table %q", table)
		}
	}
	for key, val := range asMap(s["env"]) {
		if !envKeyRe.MatchString(key) {
			v.errorf("api_settings.env."+key, "invalid env key")
		}
		if str, _ := val.(string); str != "" && !envRef.MatchString(str) && looksSecret(key) {
			v.warnf("api_settings.env."+key, "looks like a secret stored in plain text; use \"${%s}\" and put the value in .env", key)
		}
	}
	if storage := asMap(s["storage"]); len(storage) > 0 {
		if d, _ := storage["driver"].(string); !storageDrivers[d] {
			v.errorf("api_settings.storage.driver", "unknown storage driver %q (valid: %s)", d, keys(storageDrivers))
		}
	}
	if n, ok := asMap(s["auth"])["password_min_length"].(float64); ok && n > tenant.MaxPasswordBytes {
		v.errorf("api_settings.auth.password_min_length", "must be at most %d (bcrypt uses only the first %d bytes)", tenant.MaxPasswordBytes, tenant.MaxPasswordBytes)
	}
	if tz, _ := s["timezone"].(string); tz != "" && tz != "UTC" {
		if tenant.LoadTimezone(tz).String() == "UTC" {
			v.errorf("api_settings.timezone", "unknown IANA timezone %q", tz)
		}
	}
	if origin, _ := asMap(s["cors"])["allowed_origin"].(string); origin != "" && origin != "*" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			v.errorf("api_settings.cors.allowed_origin", "must be \"*\" or scheme://host[:port] with no path")
		}
	}
}

func (v *validator) functionsSection(fns map[string]any) {
	routes := map[string]string{}
	for name, f := range fns {
		path := "functions." + name
		fm, ok := f.(map[string]any)
		if !ok {
			v.errorf(path, "must be an object")
			continue
		}
		route := asMap(fm["route"])
		method := strings.ToUpper(fmt.Sprint(route["method"]))
		if !httpMethods[method] {
			v.errorf(path+".route.method", "unknown method %q (valid: %s)", route["method"], keys(httpMethods))
		}
		if method != "PRIVATE" {
			rp, _ := route["path"].(string)
			if !strings.HasPrefix(rp, "/fn/") {
				v.errorf(path+".route.path", "must start with /fn/ (got %q)", rp)
			}
			if _, err := pipeline.RouteParams(rp); err != nil {
				v.errorf(path+".route.path", "%v", err)
			}
			key := method + " " + pipeline.RouteShape(rp)
			if other, dup := routes[key]; dup {
				v.errorf(path+".route", "%s is also served by function %q", key, other)
			}
			routes[key] = name
		}
		v.input(path+".route.input", asMap(route["input"]))
		v.steps(path+".steps", asSlice(fm["steps"]), stepScope{})
	}
}

func (v *validator) input(path string, input map[string]any) {
	for name, raw := range input {
		p := path + "." + name
		if !identRe.MatchString(name) {
			v.errorf(p, "%q is not a valid field name", name)
		}
		f, ok := raw.(map[string]any)
		if !ok {
			v.errorf(p, "must be an object like {\"type\": \"string\"}")
			continue
		}
		typ, _ := f["type"].(string)
		if !pipeline.InputTypes[typ] {
			v.errorf(p+".type", "unknown type %q (valid: %s)", f["type"], keys(pipeline.InputTypes))
		}
		if pat, ok := f["pattern"].(string); ok {
			if _, err := regexp.Compile(pat); err != nil {
				v.errorf(p+".pattern", "invalid regular expression: %v", err)
			}
			if typ != "string" {
				v.errorf(p+".pattern", "only applies to strings")
			}
		}
		if format, ok := f["format"].(string); ok {
			if !pipeline.InputFormats[format] {
				v.errorf(p+".format", "unknown format %q (valid: %s)", format, keys(pipeline.InputFormats))
			}
			if typ != "string" {
				v.errorf(p+".format", "only applies to strings")
			}
		}
		minV, hasMin := f["min"].(float64)
		maxV, hasMax := f["max"].(float64)
		if (hasMin || hasMax) && typ != "string" && typ != "number" && typ != "integer" && typ != "array" {
			v.errorf(p, "min/max only apply to string, number, integer and array")
		}
		if hasMin && hasMax && minV > maxV {
			v.errorf(p, "min is greater than max")
		}
		if e, ok := f["enum"]; ok && len(asSlice(e)) == 0 {
			v.errorf(p+".enum", "must be a non-empty array")
		}
		if f["required"] == true && f["default"] != nil {
			v.warnf(p, "a required field with a default never fails; drop one of them")
		}
	}
}

// stepScope is where a list of steps runs.
type stepScope struct {
	inCron, inTx bool
}

func (v *validator) cronjobs(jobs map[string]any) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	for name, j := range jobs {
		path := "cronjobs." + name
		jm, ok := j.(map[string]any)
		if !ok {
			v.errorf(path, "must be an object")
			continue
		}
		sched, _ := jm["schedule"].(string)
		if _, err := parser.Parse(sched); err != nil {
			v.errorf(path+".schedule", "invalid cron expression %q: %v", sched, err)
		}
		v.steps(path+".steps", asSlice(jm["steps"]), stepScope{inCron: true})
	}
}

func (v *validator) steps(path string, steps []any, scope stepScope) {
	inCron := scope.inCron
	if len(steps) == 0 {
		v.warnf(path, "no steps")
	}
	for i, s := range steps {
		p := fmt.Sprintf("%s[%d]", path, i)
		sm, ok := s.(map[string]any)
		if !ok {
			v.errorf(p, "must be an object")
			continue
		}
		typ, _ := sm["type"].(string)
		if !stepTypes[typ] {
			v.errorf(p+".type", "unknown step type %q (valid: %s)", typ, keys(stepTypes))
			continue
		}
		str := func(k string) string { s, _ := sm[k].(string); return s }
		require := func(fields ...string) {
			for _, f := range fields {
				if sm[f] == nil || sm[f] == "" {
					v.errorf(p+"."+f, "%s requires %q", typ, f)
				}
			}
		}
		if scope.inTx && !pipeline.StepAllowedInTransaction(typ) {
			v.errorf(p, "%s is not allowed inside a transaction (it can't be rolled back); move it after the transaction step", typ)
		}
		if a := str("assign"); a != "" && !assignRe.MatchString(a) {
			v.errorf(p+".assign", "%q is not a valid variable name", a)
		}

		switch typ {
		case "db.select", "db.aggregate", "db.insert", "db.update", "db.delete":
			require("table")
			table := str("table")
			cols, ok := v.tables[table]
			if table != "" && !ok {
				v.errorf(p+".table", "unknown table %q", table)
			}
			if ok {
				for _, c := range asSlice(sm["columns"]) {
					if cs, _ := c.(string); !cols[cs] {
						v.errorf(p+".columns", "unknown column %q in table %q", c, table)
					}
				}
				for _, field := range []string{"data", "where"} {
					for c := range asMap(sm[field]) {
						if !cols[c] && !strings.HasPrefix(c, "$") {
							v.errorf(p+"."+field+"."+c, "unknown column %q in table %q", c, table)
						}
					}
				}
			}
			if typ == "db.select" || typ == "db.aggregate" {
				v.selectOptions(p, sm, table, cols, ok)
			}
			if typ == "db.insert" || typ == "db.update" {
				require("data")
			}
			if typ == "db.update" || typ == "db.delete" {
				if len(asMap(sm["where"])) == 0 {
					v.warnf(p+".where", "%s without where affects every row", typ)
				}
			}
		case "if":
			require("condition")
			v.steps(p+".then", asSlice(sm["then"]), scope)
			if e := asSlice(sm["else"]); len(e) > 0 {
				v.steps(p+".else", e, scope)
			}
		case "foreach":
			require("items", "as")
			v.steps(p+".steps", asSlice(sm["steps"]), scope)
		case "transaction":
			inner := scope
			inner.inTx = true
			v.steps(p+".steps", asSlice(sm["steps"]), inner)
		case "try":
			v.steps(p+".steps", asSlice(sm["steps"]), scope)
			if c := asSlice(sm["catch"]); len(c) > 0 {
				v.steps(p+".catch", c, scope)
			}
			if a := str("as"); a != "" && !assignRe.MatchString(a) {
				v.errorf(p+".as", "%q is not a valid variable name", a)
			}
		case "fail":
			if code, ok := sm["status_code"].(float64); ok && (code < 400 || code > 599) {
				v.errorf(p+".status_code", "must be between 400 and 599")
			}
			if inCron {
				v.warnf(p, "fail in a cronjob only stops the job and logs the message")
			}
		case "set_variable":
			require("assign")
		case "transform.sum", "transform.count", "transform.filter":
			require("input", "assign")
			if typ == "transform.filter" {
				require("condition")
			}
		case "transform.map":
			require("input", "assign")
			if sm["field"] == nil && sm["fields"] == nil {
				v.errorf(p, "transform.map requires \"field\" or \"fields\"")
			}
		case "transform.math":
			require("expression", "assign")
			if expr := str("expression"); expr != "" {
				if err := pipeline.ValidateMathExpression(expr); err != nil {
					v.errorf(p+".expression", "%v", err)
				}
			}
		case "transform.datetime":
			require("value", "unit", "assign")
			if op := str("operation"); op != "add" && op != "subtract" {
				v.errorf(p+".operation", "must be \"add\" or \"subtract\"")
			}
			switch str("unit") {
			case "", "seconds", "minutes", "hours", "days":
			default:
				v.errorf(p+".unit", "must be seconds, minutes, hours or days")
			}
		case "function.call":
			require("function_id")
			if id := str("function_id"); id != "" && !v.functions[id] {
				v.errorf(p+".function_id", "unknown function %q", id)
			}
		case "http.request":
			require("url")
		case "email.send":
			if len(asSlice(sm["to"])) == 0 {
				v.errorf(p+".to", "email.send requires at least one recipient")
			}
			if str("mode") == "template" {
				if id := str("template_id"); !v.templates[id] {
					v.errorf(p+".template_id", "unknown email template %q (define it in api_settings.email.templates)", id)
				}
			}
		case "response":
			if inCron {
				v.warnf(p, "response steps are a no-op in cronjobs")
			}
		}
	}
}

// selectOptions checks db.select / db.aggregate ordering, paging, include
// and aggregate fields. known reports whether the table exists.
func (v *validator) selectOptions(p string, sm map[string]any, table string, cols map[string]bool, known bool) {
	typ, _ := sm["type"].(string)
	outputs := cols
	if typ == "db.aggregate" {
		outputs = map[string]bool{}
		aggs := asMap(sm["aggregates"])
		if len(aggs) == 0 {
			v.errorf(p+".aggregates", "db.aggregate requires \"aggregates\", e.g. {\"total\": \"sum:amount\"}")
		}
		for alias, spec := range aggs {
			if !identRe.MatchString(alias) {
				v.errorf(p+".aggregates."+alias, "%q is not a valid alias", alias)
			}
			s, _ := spec.(string)
			_, col, err := pipeline.ParseAggregate(s)
			if err != nil {
				v.errorf(p+".aggregates."+alias, "%v", err)
			} else if known && col != "" && !cols[col] {
				v.errorf(p+".aggregates."+alias, "unknown column %q in table %q", col, table)
			}
			outputs[alias] = true
		}
		for _, g := range asSlice(sm["group_by"]) {
			gs, _ := g.(string)
			if known && !cols[gs] {
				v.errorf(p+".group_by", "unknown column %q in table %q", g, table)
			}
			outputs[gs] = true
		}
	} else {
		for _, field := range []string{"group_by", "aggregates"} {
			if sm[field] != nil {
				v.errorf(p+"."+field, "only db.aggregate supports %q", field)
			}
		}
	}

	var orders []any
	switch o := sm["order_by"].(type) {
	case string:
		orders = []any{o}
	case []any:
		orders = o
	case nil:
	default:
		v.errorf(p+".order_by", "must be a string or an array of strings")
	}
	for _, o := range orders {
		os, _ := o.(string)
		col, _, err := pipeline.ParseOrderBy(os)
		if err != nil {
			v.errorf(p+".order_by", "%v", err)
		} else if known && !outputs[col] {
			v.errorf(p+".order_by", "unknown column %q", col)
		}
	}

	for _, field := range []string{"limit", "offset"} {
		switch n := sm[field].(type) {
		case nil, string:
		case float64:
			if n < 0 || n != float64(int(n)) {
				v.errorf(p+"."+field, "must be a non-negative integer")
			}
		default:
			v.errorf(p+"."+field, "must be a number or a {{template}}")
		}
	}

	for i, inc := range asSlice(sm["include"]) {
		ip := fmt.Sprintf("%s.include[%d]", p, i)
		if typ != "db.select" {
			v.errorf(ip, "only db.select supports include")
			break
		}
		im := asMap(inc)
		rel, _ := im["table"].(string)
		relCols, relOK := v.tables[rel]
		if !relOK {
			v.errorf(ip+".table", "unknown table %q", rel)
			continue
		}
		if known && !v.related(table, rel) {
			v.errorf(ip+".table", "no relation between %q and %q", table, rel)
		}
		for _, c := range asSlice(im["columns"]) {
			if cs, _ := c.(string); !relCols[cs] {
				v.errorf(ip+".columns", "unknown column %q in table %q", c, rel)
			}
		}
	}
}

// decodes checks the compiled payload against the server's Go types, which
// catches wrong value types (e.g. "enabled": "yes") the checks above miss.
func (v *validator) decodes(p *Project) {
	compiled, err := p.Compile()
	if err != nil {
		v.errorf("", "%v", err)
		return
	}
	data, err := json.Marshal(compiled.Payload)
	if err != nil {
		v.errorf("", "%v", err)
		return
	}
	var cfg tenant.TenantConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		v.errorf("", "config does not match the server's types: %v", err)
	}
	for _, name := range compiled.MissingEnv {
		v.warnf("api_settings", "${%s} is not set in .env or the environment", name)
	}
}

func looksSecret(key string) bool {
	k := strings.ToUpper(key)
	for _, s := range []string{"SECRET", "PASSWORD", "TOKEN", "API_KEY", "PRIVATE"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func keys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
