package pipeline

import (
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// Input field types and formats accepted in route.input.
var (
	InputTypes   = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true, "any": true}
	InputFormats = map[string]bool{"email": true, "uuid": true, "date": true, "datetime": true, "url": true}
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidateInput checks data against a route's input declaration and returns
// the cleaned values (defaults filled in) or one message per invalid field.
//
// fromQuery is set for GET query strings: values arrive as strings and are
// converted to the declared type, and undeclared parameters are dropped. A
// JSON body with an undeclared field is rejected instead, so a client can't
// slip extra columns into a step that writes the whole request.
func ValidateInput(spec map[string]tenant.InputField, data map[string]any, fromQuery bool) (map[string]any, map[string]string) {
	out := make(map[string]any, len(spec))
	errs := map[string]string{}

	if !fromQuery {
		for name := range data {
			if _, ok := spec[name]; !ok {
				errs[name] = "unknown field"
			}
		}
	}

	for name, f := range spec {
		v, present := data[name]
		if !present || v == nil || (fromQuery && v == "") {
			switch {
			case f.Default != nil:
				out[name] = f.Default
			case f.Required:
				errs[name] = "is required"
			case present && v == nil:
				out[name] = nil
			}
			continue
		}
		if fromQuery {
			if s, ok := v.(string); ok {
				converted, err := fromQueryString(s, f.Type)
				if err != nil {
					errs[name] = err.Error()
					continue
				}
				v = converted
			}
		}
		if msg := checkInputValue(v, f); msg != "" {
			errs[name] = msg
			continue
		}
		out[name] = v
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

func fromQueryString(s, typ string) (any, error) {
	switch typ {
	case "number":
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("must be a number")
		}
		return f, nil
	case "integer":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return float64(n), nil
	case "boolean":
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("must be true or false")
		}
		return b, nil
	case "array":
		// A repeated parameter arrives as an array; a single one is a string.
		return []any{s}, nil
	}
	return s, nil
}

func checkInputValue(v any, f tenant.InputField) string {
	var size float64
	hasSize := false
	switch f.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "must be a string"
		}
		size, hasSize = float64(utf8.RuneCountInString(s)), true
		if f.Pattern != "" {
			re, err := regexp.Compile(f.Pattern)
			if err != nil || !re.MatchString(s) {
				return "has an invalid format"
			}
		}
		if f.Format != "" && !matchesFormat(s, f.Format) {
			return "must be a valid " + f.Format
		}
	case "number", "integer":
		n, ok := v.(float64)
		if !ok {
			return "must be a number"
		}
		if f.Type == "integer" && n != math.Trunc(n) {
			return "must be an integer"
		}
		size, hasSize = n, true
	case "boolean":
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return "must be an object"
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return "must be an array"
		}
		size, hasSize = float64(len(a)), true
	}

	if hasSize {
		unit := ""
		switch f.Type {
		case "string":
			unit = " characters"
		case "array":
			unit = " items"
		}
		if f.Min != nil && size < *f.Min {
			return fmt.Sprintf("must be at least %v%s", *f.Min, unit)
		}
		if f.Max != nil && size > *f.Max {
			return fmt.Sprintf("must be at most %v%s", *f.Max, unit)
		}
	}

	if len(f.Enum) > 0 {
		for _, e := range f.Enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				return ""
			}
		}
		allowed := make([]string, len(f.Enum))
		for i, e := range f.Enum {
			allowed[i] = fmt.Sprint(e)
		}
		return "must be one of: " + strings.Join(allowed, ", ")
	}
	return ""
}

func matchesFormat(s, format string) bool {
	switch format {
	case "email":
		addr, err := mail.ParseAddress(s)
		return err == nil && addr.Address == s
	case "uuid":
		return uuidRe.MatchString(s)
	case "date":
		_, err := time.Parse("2006-01-02", s)
		return err == nil
	case "datetime":
		_, err := time.Parse(time.RFC3339, s)
		return err == nil
	case "url":
		u, err := url.Parse(s)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
	}
	return true
}
