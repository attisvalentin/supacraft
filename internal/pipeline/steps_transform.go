package pipeline

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

// ExecuteTransformSum sums a numeric field across array items.
// Input format: "variable.field" (e.g., "orders.total") resolves to the
// "orders" array in context, collects the "total" field from each item, and sums.
func ExecuteTransformSum(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	values, err := resolveInputToArray(step.Input, pCtx)
	if err != nil {
		return fmt.Errorf("transform.sum: %w", err)
	}

	var sum float64
	for _, v := range values {
		f, ok := toFloat64(v)
		if !ok {
			continue
		}
		sum += f
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, sum)
	}
	return nil
}

// ExecuteTransformCount counts items in an array.
func ExecuteTransformCount(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	values, err := resolveInputToArray(step.Input, pCtx)
	if err != nil {
		return fmt.Errorf("transform.count: %w", err)
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, len(values))
	}
	return nil
}

// ExecuteTransformMap extracts/transforms fields from array items.
// Fields is a map of output_key -> input_path (e.g., {"name": "full_name", "total": "order_total"}).
// If Fields is empty and Field is set, extracts a single field from each item.
func ExecuteTransformMap(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	items, err := resolveInputToArray(step.Input, pCtx)
	if err != nil {
		return fmt.Errorf("transform.map: %w", err)
	}

	// Single field extraction
	if step.Field != "" && len(step.Fields) == 0 {
		result := make([]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				if v, ok := m[step.Field]; ok {
					result = append(result, v)
				}
			}
		}
		if step.Assign != "" {
			pCtx.Set(step.Assign, result)
		}
		return nil
	}

	// Multi-field mapping
	result := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mapped := make(map[string]any)
		for outKey, inKey := range step.Fields {
			if v, ok := m[inKey]; ok {
				mapped[outKey] = v
			}
		}
		result = append(result, mapped)
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, result)
	}
	return nil
}

// ExecuteTransformFilter filters array items by a condition.
func ExecuteTransformFilter(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	items, err := resolveInputToArray(step.Input, pCtx)
	if err != nil {
		return fmt.Errorf("transform.filter: %w", err)
	}

	var filtered []any
	for _, item := range items {
		// Temporarily set the item in context for condition evaluation
		pCtx.Set("_item", item)
		result, err := EvaluateCondition(step.Condition, pCtx)
		if err != nil {
			return fmt.Errorf("transform.filter: evaluate condition: %w", err)
		}
		if result {
			filtered = append(filtered, item)
		}
	}
	// Clean up temp variable
	pCtx.Set("_item", nil)

	if step.Assign != "" {
		pCtx.Set(step.Assign, filtered)
	}
	return nil
}

// ExecuteTransformMath evaluates an arithmetic expression with {{variable}} substitution.
// The expression may contain {{dot.path}} tokens that are resolved before evaluation.
// Supports +, -, *, / with standard operator precedence.
func ExecuteTransformMath(ctx context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, deps *StepDeps) error {
	if step.Expression == "" {
		return fmt.Errorf("transform.math: expression is required")
	}

	// Resolve all {{...}} tokens in the expression.
	resolved, err := ResolveString(step.Expression, pCtx)
	if err != nil {
		return fmt.Errorf("transform.math: resolve expression: %w", err)
	}

	var result float64
	// If the resolved value is already numeric (single {{ref}} covering full expression),
	// use it directly. Otherwise evaluate the resulting string as arithmetic.
	if f, ok := toFloat64(resolved); ok {
		result = f
	} else {
		exprStr := fmt.Sprintf("%v", resolved)
		result, err = evalMath(exprStr)
		if err != nil {
			return fmt.Errorf("transform.math: %w", err)
		}
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, result)
	}
	return nil
}

// evalMath parses and evaluates an arithmetic string expression.
// Supports + - * / % and ** with standard operator precedence, parentheses,
// and built-in math functions (abs, ceil, floor, round, pow, min, max).
// All tokens must be numeric; returns an error for any non-numeric token,
// division/modulo by zero, unknown function, or wrong arity.
func evalMath(expr string) (float64, error) {
	p := &mathParser{expr: expr}
	result, err := p.parseAddSub()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos < len(p.expr) {
		return 0, fmt.Errorf("unexpected token at position %d: %q", p.pos, p.expr[p.pos:])
	}
	return result, nil
}

type mathParser struct {
	expr string
	pos  int
}

func (p *mathParser) skipSpace() {
	for p.pos < len(p.expr) && (p.expr[p.pos] == ' ' || p.expr[p.pos] == '\t') {
		p.pos++
	}
}

func (p *mathParser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.expr) {
			break
		}
		op := p.expr[p.pos]
		if op != '+' && op != '-' {
			break
		}
		p.pos++
		right, err := p.parseMulDiv()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

func (p *mathParser) parseMulDiv() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.expr) {
			break
		}
		op := p.expr[p.pos]
		if op != '*' && op != '/' && op != '%' {
			break
		}
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			left = math.Mod(left, right)
		}
	}
	return left, nil
}

// parseUnary handles a leading minus, then delegates to parsePower.
// Unary minus binds weaker than ** on the left (so -2**3 = -(2**3) = -8),
// matching common language conventions.
func (p *mathParser) parseUnary() (float64, error) {
	p.skipSpace()
	if p.pos < len(p.expr) && p.expr[p.pos] == '-' {
		p.pos++
		v, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -v, nil
	}
	return p.parsePower()
}

// parsePower handles ** (right-associative). The RHS goes through parseUnary
// so 2**-3 is allowed and means 2**(-3).
func (p *mathParser) parsePower() (float64, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos+1 < len(p.expr) && p.expr[p.pos] == '*' && p.expr[p.pos+1] == '*' {
		p.pos += 2
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return math.Pow(left, right), nil
	}
	return left, nil
}

// parsePrimary handles numbers, parenthesized subexpressions, and function calls.
func (p *mathParser) parsePrimary() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.expr) {
		return 0, fmt.Errorf("expected expression but reached end")
	}
	ch := p.expr[p.pos]
	if ch == '(' {
		p.pos++
		v, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.pos >= len(p.expr) || p.expr[p.pos] != ')' {
			return 0, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return v, nil
	}
	if mathIsAlpha(ch) {
		return p.parseFunctionCall()
	}
	return p.parseNumber()
}

func (p *mathParser) parseFunctionCall() (float64, error) {
	start := p.pos
	for p.pos < len(p.expr) && mathIsAlnum(p.expr[p.pos]) {
		p.pos++
	}
	name := p.expr[start:p.pos]
	p.skipSpace()
	if p.pos >= len(p.expr) || p.expr[p.pos] != '(' {
		return 0, fmt.Errorf("expected '(' after function name %q at position %d", name, p.pos)
	}
	p.pos++

	var args []float64
	p.skipSpace()
	if p.pos < len(p.expr) && p.expr[p.pos] == ')' {
		p.pos++
	} else {
		for {
			v, err := p.parseAddSub()
			if err != nil {
				return 0, err
			}
			args = append(args, v)
			p.skipSpace()
			if p.pos >= len(p.expr) {
				return 0, fmt.Errorf("unterminated function call %q", name)
			}
			if p.expr[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.expr[p.pos] == ')' {
				p.pos++
				break
			}
			return 0, fmt.Errorf("expected ',' or ')' in function call %q at position %d", name, p.pos)
		}
	}

	spec, ok := mathFunctions[name]
	if !ok {
		return 0, fmt.Errorf("unknown function %q", name)
	}
	if len(args) < spec.minArgs || (spec.maxArgs >= 0 && len(args) > spec.maxArgs) {
		var expected string
		switch {
		case spec.maxArgs == -1:
			expected = fmt.Sprintf("at least %d", spec.minArgs)
		case spec.minArgs == spec.maxArgs:
			expected = fmt.Sprintf("%d", spec.minArgs)
		default:
			expected = fmt.Sprintf("%d to %d", spec.minArgs, spec.maxArgs)
		}
		return 0, fmt.Errorf("function %q expects %s argument(s), got %d", name, expected, len(args))
	}
	return spec.fn(args)
}

func (p *mathParser) parseNumber() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.expr) {
		return 0, fmt.Errorf("expected number but reached end of expression")
	}
	start := p.pos
	if !mathIsDigit(p.expr[p.pos]) && p.expr[p.pos] != '.' {
		return 0, fmt.Errorf("expected number at position %d in %q", start, p.expr)
	}
	for p.pos < len(p.expr) && (mathIsDigit(p.expr[p.pos]) || p.expr[p.pos] == '.') {
		p.pos++
	}
	token := p.expr[start:p.pos]
	f, err := strconv.ParseFloat(token, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q at position %d", token, start)
	}
	return f, nil
}

func mathIsDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func mathIsAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func mathIsAlnum(b byte) bool {
	return mathIsAlpha(b) || mathIsDigit(b)
}

// mathFunctions lists the built-in functions usable inside transform.math expressions.
// maxArgs == -1 marks a variadic function with no upper limit.
var mathFunctions = map[string]struct {
	minArgs, maxArgs int
	fn               func([]float64) (float64, error)
}{
	"abs":   {1, 1, func(a []float64) (float64, error) { return math.Abs(a[0]), nil }},
	"ceil":  {1, 1, func(a []float64) (float64, error) { return math.Ceil(a[0]), nil }},
	"floor": {1, 1, func(a []float64) (float64, error) { return math.Floor(a[0]), nil }},
	"round": {1, 1, func(a []float64) (float64, error) { return math.Round(a[0]), nil }},
	"pow":   {2, 2, func(a []float64) (float64, error) { return math.Pow(a[0], a[1]), nil }},
	"min": {1, -1, func(a []float64) (float64, error) {
		m := a[0]
		for _, v := range a[1:] {
			if v < m {
				m = v
			}
		}
		return m, nil
	}},
	"max": {1, -1, func(a []float64) (float64, error) {
		m := a[0]
		for _, v := range a[1:] {
			if v > m {
				m = v
			}
		}
		return m, nil
	}},
}

// ExecuteTransformDatetime computes a datetime by adding or subtracting a duration
// from a base time. When Input is empty, uses the built-in {{now}} variable.
// Stores the result as time.Time in the pipeline context.
//
// Example: subtract 5 minutes from now:
//
//	{ "type": "transform.datetime", "operation": "subtract", "value": 5, "unit": "minutes", "assign": "threshold" }
//
// Example: add 7 days to an existing datetime:
//
//	{ "type": "transform.datetime", "input": "{{slot.created_at}}", "operation": "add", "value": 7, "unit": "days", "assign": "expiry" }
func ExecuteTransformDatetime(_ context.Context, step *tenant.FunctionStep, pCtx *PipelineContext, _ *StepDeps) error {
	// Resolve base time
	var base time.Time
	if step.Input != "" {
		val, err := ResolveString(step.Input, pCtx)
		if err != nil {
			return fmt.Errorf("transform.datetime: resolve input: %w", err)
		}
		t, ok := val.(time.Time)
		if !ok {
			return fmt.Errorf("transform.datetime: input %q resolved to %T, expected time.Time", step.Input, val)
		}
		base = t
	} else {
		nowVal, ok := pCtx.Get("now")
		if !ok {
			return fmt.Errorf("transform.datetime: {{now}} not found in context")
		}
		t, ok := nowVal.(time.Time)
		if !ok {
			return fmt.Errorf("transform.datetime: {{now}} is not a time.Time")
		}
		base = t
	}

	// Resolve amount
	amount, err := resolveIntValue(step.Value, pCtx)
	if err != nil {
		return fmt.Errorf("transform.datetime: resolve value: %w", err)
	}

	// Parse unit → duration
	var d time.Duration
	switch step.Unit {
	case "seconds":
		d = time.Duration(amount) * time.Second
	case "minutes":
		d = time.Duration(amount) * time.Minute
	case "hours":
		d = time.Duration(amount) * time.Hour
	case "days":
		d = time.Duration(amount) * 24 * time.Hour
	default:
		return fmt.Errorf("transform.datetime: unknown unit %q (valid: seconds, minutes, hours, days)", step.Unit)
	}

	// Apply operation
	var result time.Time
	switch step.Operation {
	case "add":
		result = base.Add(d)
	case "subtract":
		result = base.Add(-d)
	default:
		return fmt.Errorf("transform.datetime: unknown operation %q (valid: add, subtract)", step.Operation)
	}

	if step.Assign != "" {
		pCtx.Set(step.Assign, result)
	}
	return nil
}

// resolveIntValue resolves a step's Value field (any) to an int64.
// Handles literal numbers (JSON unmarshals integers as float64) and {{ref}} strings.
func resolveIntValue(v any, pCtx *PipelineContext) (int64, error) {
	if f, ok := toFloat64(v); ok {
		return int64(f), nil
	}
	if s, ok := v.(string); ok && strings.Contains(s, "{{") {
		resolved, err := ResolveString(s, pCtx)
		if err != nil {
			return 0, err
		}
		if f, ok := toFloat64(resolved); ok {
			return int64(f), nil
		}
		return 0, fmt.Errorf("resolved value %v is not numeric", resolved)
	}
	return 0, fmt.Errorf("value %v is not numeric", v)
}

// resolveInputToArray resolves a step input path to a []any.
// The input can be a direct {{path}} reference or a dot-path like "orders.total"
// where it resolves "orders" and collects "total" from each item.
func resolveInputToArray(input string, pCtx *PipelineContext) ([]any, error) {
	// Wrap in {{}} if not already a template reference
	ref := input
	if len(ref) > 0 && ref[0] != '{' {
		ref = "{{" + ref + "}}"
	}

	val, err := ResolveString(ref, pCtx)
	if err != nil {
		return nil, fmt.Errorf("resolve input %q: %w", input, err)
	}

	switch v := val.(type) {
	case []any:
		return v, nil
	case []map[string]any:
		result := make([]any, len(v))
		for i, m := range v {
			result[i] = m
		}
		return result, nil
	default:
		// Single value — wrap in array
		return []any{val}, nil
	}
}

var mathPlaceholder = regexp.MustCompile(`\{\{[^}]*\}\}`)

// ValidateMathExpression checks a transform.math expression's syntax with
// every {{ref}} substituted by 1. Division by zero is not a syntax error.
func ValidateMathExpression(expr string) error {
	_, err := evalMath(mathPlaceholder.ReplaceAllString(expr, "1"))
	if err != nil && strings.Contains(err.Error(), "by zero") {
		return nil
	}
	return err
}
