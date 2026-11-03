package project

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Seed is the "seed" section: development data for the local database and
// for every test run. It is never deployed.
type Seed struct {
	// Users are auth users: "email" is required; "password" (literal or
	// "${NAME}" from .env; random when omitted) and "role" are optional,
	// other keys fill creatable users columns.
	Users []map[string]any `json:"users,omitempty"`
	// Tables maps a table name to its rows.
	Tables map[string][]map[string]any `json:"tables,omitempty"`
}

// Test is one entry of the "tests" section: a request against a fresh,
// seeded database and what its response and the database must look like.
// Tests are never deployed.
type Test struct {
	// Call is "METHOD /path", e.g. "POST /fn/checkout" or "POST /query".
	Call string `json:"call"`
	// As is the email of a seed user to send the request as.
	As    string            `json:"as,omitempty"`
	Body  any               `json:"body,omitempty"`
	Query map[string]string `json:"query,omitempty"`
	// HTTPMocks answers http.request steps by URL prefix; a call that
	// matches none fails the test instead of reaching the network.
	HTTPMocks map[string]HTTPMock `json:"http_mocks,omitempty"`
	// RunJobs runs the jobs the request queued (and those they queue),
	// ignoring their delay, before the expectations are checked.
	RunJobs bool   `json:"run_jobs,omitempty"`
	Expect  Expect `json:"expect"`
}

// HTTPMock is a canned response to an http.request step.
type HTTPMock struct {
	Status int `json:"status,omitempty"` // default 200
	Body   any `json:"body,omitempty"`
}

// Expect is what a test checks. Body matches when every field it lists is
// equal in the response (other fields are ignored; arrays must have the
// same length).
type Expect struct {
	Status int                 `json:"status,omitempty"`
	Body   any                 `json:"body,omitempty"`
	DB     map[string]DBExpect `json:"db,omitempty"`
	// Emails is how many emails the request sent (captured, never sent).
	Emails *int `json:"emails,omitempty"`
	// Jobs are the queued jobs in order, each matched like Body against
	// {function, args, status, attempts, last_error}.
	Jobs []any `json:"jobs,omitempty"`
}

// DBExpect counts the rows of a table matching Where after the request.
type DBExpect struct {
	Where map[string]any `json:"where,omitempty"`
	Count int            `json:"count"`
}

// SeedData returns the seed section (empty when absent).
func (p *Project) SeedData() (*Seed, error) {
	var s Seed
	if err := decodeSection(p.Raw["seed"], &s); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return &s, nil
}

// TestCases returns the tests section (empty when absent).
func (p *Project) TestCases() (map[string]Test, error) {
	tests := map[string]Test{}
	if err := decodeSection(p.Raw["tests"], &tests); err != nil {
		return nil, fmt.Errorf("tests: %w", err)
	}
	return tests, nil
}

func decodeSection(raw, out any) error {
	if raw == nil {
		return nil
	}
	if arr, ok := raw.([]any); ok && len(arr) == 0 {
		return nil // validation accepts [] as an empty section
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// WithoutLocalSections returns the project without seed and tests, which
// only matter locally: deploys never send them to a server.
func (p *Project) WithoutLocalSections() map[string]any {
	out := make(map[string]any, len(p.Raw))
	for k, v := range p.Raw {
		if k != "seed" && k != "tests" {
			out[k] = v
		}
	}
	return out
}

var callRe = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) (/\S*)$`)

// ParseCall splits a test's call into method and path.
func ParseCall(call string) (method, path string, ok bool) {
	m := callRe.FindStringSubmatch(strings.TrimSpace(call))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// seedAndTests validates the seed and tests sections.
func (v *validator) seedAndTests(raw map[string]any, authEnabled bool) {
	seed := asMap(raw["seed"])
	emails := map[string]bool{}
	roles := map[string]bool{}
	for _, r := range asSlice(asMap(asMap(raw["api_settings"])["auth"])["roles"]) {
		if name, ok := r.(string); ok {
			roles[name] = true
		}
	}
	if users, set := seed["users"]; set && users != nil {
		if _, isList := users.([]any); !isList {
			v.errorf("seed.users", "must be a list of users")
		} else if len(asSlice(users)) > 0 && !authEnabled {
			v.errorf("seed.users", "needs auth_enabled: true")
		}
	}
	if tables, set := seed["tables"]; set && tables != nil {
		if _, isMap := tables.(map[string]any); !isMap {
			v.errorf("seed.tables", "must be an object of table name → rows")
		}
	}
	for i, u := range asSlice(seed["users"]) {
		path := fmt.Sprintf("seed.users[%d]", i)
		um, ok := u.(map[string]any)
		if !ok {
			v.errorf(path, "must be an object")
			continue
		}
		email, _ := um["email"].(string)
		switch {
		case email == "":
			v.errorf(path+".email", "is required")
		case emails[email]:
			v.errorf(path+".email", "duplicate seed user %q", email)
		}
		emails[email] = true
		if role, set := um["role"]; set && role != nil {
			if name, _ := role.(string); !roles[name] {
				v.errorf(path+".role", "unknown role %v (define it in api_settings.auth.roles)", role)
			}
		}
		if pw, set := um["password"]; set && pw != nil {
			if _, isStr := pw.(string); !isStr {
				v.errorf(path+".password", "must be a string")
			}
		}
		for col := range um {
			if col != "email" && col != "password" && col != "role" && !v.tables["users"][col] {
				v.errorf(path+"."+col, "unknown users column %q", col)
			}
		}
	}
	for table, rows := range asMap(seed["tables"]) {
		path := "seed.tables." + table
		cols, known := v.tables[table]
		if !known {
			v.errorf(path, "unknown table %q", table)
			continue
		}
		if table == "users" {
			v.errorf(path, "add users under seed.users (passwords are hashed there)")
			continue
		}
		list, ok := rows.([]any)
		if !ok {
			v.errorf(path, "must be a list of rows")
			continue
		}
		for i, row := range list {
			rm, ok := row.(map[string]any)
			if !ok {
				v.errorf(fmt.Sprintf("%s[%d]", path, i), "must be an object")
				continue
			}
			for col := range rm {
				if !cols[col] {
					v.errorf(fmt.Sprintf("%s[%d].%s", path, i, col), "unknown column %q", col)
				}
			}
		}
	}

	for name, t := range asMap(raw["tests"]) {
		path := "tests." + name
		tm, ok := t.(map[string]any)
		if !ok {
			v.errorf(path, "must be an object")
			continue
		}
		call, _ := tm["call"].(string)
		if _, p, ok := ParseCall(call); !ok {
			v.errorf(path+".call", "must be \"METHOD /path\", e.g. \"POST /fn/checkout\" (methods: GET, POST, PUT, PATCH, DELETE)")
		} else if strings.Contains(p, "?") {
			v.errorf(path+".call", "put query parameters in \"query\", not in the path")
		} else if _, err := url.ParseRequestURI(p); err != nil {
			v.errorf(path+".call", "invalid path %q", p)
		}
		if q, set := tm["query"]; set && q != nil {
			qm, isMap := q.(map[string]any)
			if !isMap {
				v.errorf(path+".query", "must be an object of string values")
			}
			for k, qv := range qm {
				if _, isStr := qv.(string); !isStr {
					v.errorf(path+".query."+k, "must be a string (write \"2\", not 2)")
				}
			}
		}
		if rj, set := tm["run_jobs"]; set && rj != nil {
			if _, isBool := rj.(bool); !isBool {
				v.errorf(path+".run_jobs", "must be true or false")
			}
		}
		if as, set := tm["as"]; set && as != nil {
			if email, _ := as.(string); !emails[email] {
				v.errorf(path+".as", "%v is not a seed user (add it to seed.users)", as)
			}
		}
		for prefix, mock := range asMap(tm["http_mocks"]) {
			if u, err := url.Parse(prefix); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				v.errorf(path+".http_mocks", "%q must be an http(s) URL (prefix)", prefix)
			}
			if _, isMap := mock.(map[string]any); !isMap {
				v.errorf(path+".http_mocks."+prefix, "must be an object {status?, body?}")
			} else if st, set := asMap(mock)["status"]; set {
				if n, isNum := st.(float64); !isNum || n < 100 || n > 599 || n != float64(int(n)) {
					v.errorf(path+".http_mocks."+prefix+".status", "must be an HTTP status code")
				}
			}
		}
		expect, ok := tm["expect"].(map[string]any)
		if !ok {
			v.errorf(path+".expect", "is required: say what the response (status, body) or the database (db) must look like")
			continue
		}
		if s, set := expect["status"]; set {
			if n, isNum := s.(float64); !isNum || n < 100 || n > 599 || n != float64(int(n)) {
				v.errorf(path+".expect.status", "must be an HTTP status code")
			}
		}
		if js, set := expect["jobs"]; set && js != nil {
			list, isList := js.([]any)
			if !isList {
				v.errorf(path+".expect.jobs", "must be a list of jobs ({function, args?, status?})")
			}
			for i, j := range list {
				jp := fmt.Sprintf("%s.expect.jobs[%d]", path, i)
				jm, isMap := j.(map[string]any)
				if !isMap {
					v.errorf(jp, "must be an object like {\"function\": \"send_invoice\", \"status\": \"done\"}")
					continue
				}
				if f, set := jm["function"]; set {
					if fn, isStr := f.(string); !isStr {
						v.errorf(jp+".function", "must be a function name")
					} else if !v.functions[fn] {
						v.errorf(jp+".function", "unknown function %q", fn)
					}
				}
			}
		}
		if n, set := expect["emails"]; set {
			if f, isNum := n.(float64); !isNum || f < 0 || f != float64(int(f)) {
				v.errorf(path+".expect.emails", "must be a count")
			}
		}
		for table, de := range asMap(expect["db"]) {
			if _, known := v.tables[table]; !known {
				v.errorf(path+".expect.db."+table, "unknown table %q", table)
			}
			if c, isNum := asMap(de)["count"].(float64); !isNum || c < 0 || c != float64(int(c)) {
				v.errorf(path+".expect.db."+table+".count", "must be a row count")
			}
			if w, set := asMap(de)["where"]; set && w != nil {
				wm, isMap := w.(map[string]any)
				if !isMap {
					v.errorf(path+".expect.db."+table+".where", "must be an object")
				}
				for col := range wm {
					if cols, known := v.tables[table]; known && !strings.HasPrefix(col, "$") && !cols[col] {
						v.errorf(path+".expect.db."+table+".where."+col, "unknown column %q", col)
					}
				}
			}
		}
	}
}
