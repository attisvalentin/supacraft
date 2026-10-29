package pipeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EvaluateCondition evaluates a simple condition expression against the pipeline context.
//
// Supported patterns:
//
//	"{{path}}"              → truthy check
//	"{{path}} == value"     → equality
//	"{{path}} != value"     → inequality
//	"{{path}} > value"      → greater than (numeric)
//	"{{path}} >= value"     → greater than or equal
//	"{{path}} < value"      → less than
//	"{{path}} <= value"     → less than or equal
//	"{{path}} == null"      → nil check
//	"{{path}} != null"      → not nil check
//
// Right-hand side can be: null, number, true, false, "quoted string", or {{path}}.
func EvaluateCondition(expression string, ctx *PipelineContext) (bool, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return false, fmt.Errorf("empty condition")
	}

	// Try to find an operator by splitting the expression
	lhs, op, rhs, hasOp := parseConditionExpr(expression)

	if !hasOp {
		// Truthy check: resolve the whole expression
		val, err := ResolveString(expression, ctx)
		if err != nil {
			return false, err
		}
		return isTruthy(val), nil
	}

	// Resolve left-hand side
	leftVal, err := ResolveString(lhs, ctx)
	if err != nil {
		return false, fmt.Errorf("left side: %w", err)
	}

	// Resolve right-hand side
	rightVal, err := resolveRHS(rhs, ctx)
	if err != nil {
		return false, fmt.Errorf("right side: %w", err)
	}

	return compareValues(leftVal, op, rightVal)
}

// parseConditionExpr splits an expression into lhs, operator, rhs.
// Returns hasOp=false if no operator found.
func parseConditionExpr(expr string) (lhs, op, rhs string, hasOp bool) {
	// Order matters: >= and <= before > and <, != before !
	operators := []string{">=", "<=", "!=", "==", ">", "<"}

	for _, o := range operators {
		// Find operator outside of {{ }} blocks
		idx := findOperatorOutsideBraces(expr, o)
		if idx >= 0 {
			return strings.TrimSpace(expr[:idx]),
				o,
				strings.TrimSpace(expr[idx+len(o):]),
				true
		}
	}
	return expr, "", "", false
}

// findOperatorOutsideBraces finds the position of an operator that is not inside {{ }}.
func findOperatorOutsideBraces(s, op string) int {
	depth := 0
	for i := 0; i < len(s)-len(op)+1; i++ {
		if i < len(s)-1 && s[i] == '{' && s[i+1] == '{' {
			depth++
			i++
			continue
		}
		if i < len(s)-1 && s[i] == '}' && s[i+1] == '}' {
			depth--
			i++
			continue
		}
		if depth == 0 && s[i:i+len(op)] == op {
			return i
		}
	}
	return -1
}

// resolveRHS resolves a right-hand side value which can be a literal or a {{path}}.
func resolveRHS(s string, ctx *PipelineContext) (any, error) {
	s = strings.TrimSpace(s)

	if s == "null" {
		return nil, nil
	}
	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}

	// Quoted string
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}

	// Number (int or float)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}

	// {{path}} reference
	if strings.Contains(s, "{{") {
		return ResolveString(s, ctx)
	}

	// Bare string (treat as string literal)
	return s, nil
}

func compareValues(left any, op string, right any) (bool, error) {
	// Nil comparisons
	if op == "==" && right == nil {
		return left == nil, nil
	}
	if op == "!=" && right == nil {
		return left != nil, nil
	}
	if op == "==" && left == nil {
		return right == nil, nil
	}
	if op == "!=" && left == nil {
		return right != nil, nil
	}

	// time.Time comparisons — must check before numeric to avoid float64 conversion failure
	lt, leftIsTime := left.(time.Time)
	rt, rightIsTime := right.(time.Time)
	if leftIsTime || rightIsTime {
		if !leftIsTime || !rightIsTime {
			return false, fmt.Errorf("cannot compare time.Time with non-time value using %s", op)
		}
		return compareTime(lt, op, rt)
	}

	switch op {
	case "==":
		return fmt.Sprintf("%v", left) == fmt.Sprintf("%v", right), nil
	case "!=":
		return fmt.Sprintf("%v", left) != fmt.Sprintf("%v", right), nil
	case ">", ">=", "<", "<=":
		return compareNumeric(left, op, right)
	default:
		return false, fmt.Errorf("unknown operator: %s", op)
	}
}

func compareTime(left time.Time, op string, right time.Time) (bool, error) {
	switch op {
	case "==":
		return left.Equal(right), nil
	case "!=":
		return !left.Equal(right), nil
	case ">":
		return left.After(right), nil
	case ">=":
		return left.After(right) || left.Equal(right), nil
	case "<":
		return left.Before(right), nil
	case "<=":
		return left.Before(right) || left.Equal(right), nil
	default:
		return false, fmt.Errorf("unknown operator: %s", op)
	}
}

func compareNumeric(left any, op string, right any) (bool, error) {
	lf, lok := toFloat64(left)
	rf, rok := toFloat64(right)
	if !lok || !rok {
		return false, fmt.Errorf("cannot compare non-numeric values with %s", op)
	}

	switch op {
	case ">":
		return lf > rf, nil
	case ">=":
		return lf >= rf, nil
	case "<":
		return lf < rf, nil
	case "<=":
		return lf <= rf, nil
	default:
		return false, fmt.Errorf("unknown operator: %s", op)
	}
}

func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case int:
		return float64(val), true
	case int8:
		return float64(val), true
	case int16:
		return float64(val), true
	case int32:
		return float64(val), true
	case int64:
		return float64(val), true
	case float32:
		return float64(val), true
	case float64:
		return val, true
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func isTruthy(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val != ""
	case []any:
		return len(val) > 0
	case []map[string]any:
		return len(val) > 0
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}
