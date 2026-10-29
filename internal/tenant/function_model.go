package tenant

import "encoding/json"

// StringMap is a map[string]string that gracefully handles empty JSON arrays ([])
// sent instead of JSON objects ({}).
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = nil
		return nil
	}
	// Treat any array as an empty map.
	if len(data) > 0 && data[0] == '[' {
		*m = StringMap{}
		return nil
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*m = StringMap(result)
	return nil
}

// Function represents a pipeline function definition from the compiled tenant config.
type Function struct {
	Name              string         `json:"name"`
	BypassSchemaFlags bool           `json:"bypass_schema_flags"`
	Route             FunctionRoute  `json:"route"`
	Steps             []FunctionStep `json:"steps"`
}

// FunctionRoute defines the HTTP route for a pipeline function.
type FunctionRoute struct {
	Path         string    `json:"path"`
	Method       string    `json:"method"`
	AuthRequired bool      `json:"auth_required"`
	URLParams    StringMap `json:"url_params,omitempty"`
}

// FunctionStep represents a single step in a pipeline function.
type FunctionStep struct {
	Type       string         `json:"type"`
	Table      string         `json:"table,omitempty"`
	Columns    []string       `json:"columns,omitempty"`
	Where      map[string]any `json:"where,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	Assign     string         `json:"assign,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	To         []string       `json:"to,omitempty"`
	CC         []string       `json:"cc,omitempty"`
	BCC        []string       `json:"bcc,omitempty"`
	Subject    string         `json:"subject,omitempty"`
	TemplateID string         `json:"template_id,omitempty"`
	HTML       string         `json:"html,omitempty"`
	Text       string         `json:"text,omitempty"`
	ReplyTo    string         `json:"reply_to,omitempty"`

	// Logic: if
	Condition string         `json:"condition,omitempty"`
	Then      []FunctionStep `json:"then,omitempty"`
	Else      []FunctionStep `json:"else,omitempty"`

	// Logic: foreach
	Items    string         `json:"items,omitempty"`
	As       string         `json:"as,omitempty"`
	SubSteps []FunctionStep `json:"steps,omitempty"`

	// Transform
	Input      string    `json:"input,omitempty"`
	Field      string    `json:"field,omitempty"`
	Fields     StringMap `json:"fields,omitempty"`
	Expression string    `json:"expression,omitempty"` // transform.math: arithmetic expression
	Operation  string    `json:"operation,omitempty"`  // transform.datetime: "add" | "subtract"
	Unit       string    `json:"unit,omitempty"`       // transform.datetime: "seconds" | "minutes" | "hours" | "days"

	// set_variable
	Value any `json:"value,omitempty"`

	// Response
	StatusCode int `json:"status_code,omitempty"`

	// function.call
	FunctionID string         `json:"function_id,omitempty"`
	Args       map[string]any `json:"args,omitempty"`

	// http.request
	URL         string    `json:"url,omitempty"`
	HTTPMethod  string    `json:"method,omitempty"` // GET | POST | PUT | PATCH | DELETE
	Headers     StringMap `json:"headers,omitempty"`
	QueryParams StringMap `json:"query,omitempty"`
	Body        StringMap `json:"body,omitempty"`
	BodyRaw     string    `json:"body_raw,omitempty"` // raw templated request body (nested JSON ok); takes precedence over Body
}
