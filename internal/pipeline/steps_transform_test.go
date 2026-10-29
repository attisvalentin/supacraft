package pipeline

import (
	"math"
	"strings"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestEvalMath_BasicArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1 + 2", 3},
		{"10 - 4", 6},
		{"3 * 4", 12},
		{"10 / 4", 2.5},
		{"2 + 3 * 4", 14},
		{"-5 + 2", -3},
		{"-(2 + 3)", -5},
		{"3.14 * 2", 6.28},
	}
	for _, c := range cases {
		got, err := evalMath(c.expr)
		if err != nil {
			t.Errorf("evalMath(%q) error: %v", c.expr, err)
			continue
		}
		if !floatEq(got, c.want) {
			t.Errorf("evalMath(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMath_Parentheses(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"(2 + 3) * 4", 20},
		{"((1 + 2) * (3 + 4))", 21},
		{"2 * (3 + (4 * 5))", 46},
		{"(10 - 2) / (1 + 3)", 2},
	}
	for _, c := range cases {
		got, err := evalMath(c.expr)
		if err != nil {
			t.Errorf("evalMath(%q) error: %v", c.expr, err)
			continue
		}
		if !floatEq(got, c.want) {
			t.Errorf("evalMath(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMath_Modulo(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"10 % 3", 1},
		{"100 % 7", 2},
		{"5.5 % 2", 1.5},
		{"(20 + 5) % 4", 1},
	}
	for _, c := range cases {
		got, err := evalMath(c.expr)
		if err != nil {
			t.Errorf("evalMath(%q) error: %v", c.expr, err)
			continue
		}
		if !floatEq(got, c.want) {
			t.Errorf("evalMath(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMath_Power(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2 ** 3", 8},
		{"2 ** 10", 1024},
		{"2 ** 3 ** 2", 512}, // right-associative: 2**(3**2) = 2**9
		{"-2 ** 2", -4},      // unary - binds weaker than **: -(2**2)
		{"2 ** -2", 0.25},    // 2**(-2)
		{"3 * 2 ** 3", 24},   // ** binds tighter than *: 3 * (2**3)
	}
	for _, c := range cases {
		got, err := evalMath(c.expr)
		if err != nil {
			t.Errorf("evalMath(%q) error: %v", c.expr, err)
			continue
		}
		if !floatEq(got, c.want) {
			t.Errorf("evalMath(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMath_Functions(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"abs(-5)", 5},
		{"abs(3.14)", 3.14},
		{"ceil(1.2)", 2},
		{"ceil(-1.2)", -1},
		{"floor(1.8)", 1},
		{"floor(-1.2)", -2},
		{"round(1.5)", 2},
		{"round(2.5)", 3},   // half-away-from-zero (math.Round)
		{"round(-1.5)", -2}, // half-away-from-zero
		{"round(1.4)", 1},
		{"pow(2, 10)", 1024},
		{"pow(3, 2)", 9},
		{"min(3, 1, 2)", 1},
		{"min(5)", 5},
		{"max(3, 1, 2)", 3},
		{"max(-1, -5, -2)", -1},
		{"ceil(7 / 2)", 4},
		{"floor(7 / 2)", 3},
		{"min(max(1, 2), 5)", 2},
		{"abs(-3) + ceil(1.2)", 5},
	}
	for _, c := range cases {
		got, err := evalMath(c.expr)
		if err != nil {
			t.Errorf("evalMath(%q) error: %v", c.expr, err)
			continue
		}
		if !floatEq(got, c.want) {
			t.Errorf("evalMath(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalMath_Errors(t *testing.T) {
	cases := []struct {
		expr    string
		wantSub string
	}{
		{"1 / 0", "division by zero"},
		{"10 % 0", "modulo by zero"},
		{"foo(1)", "unknown function"},
		{"ceil(1, 2)", "expects 1 argument"},
		{"pow(2)", "expects 2 argument"},
		{"min()", "expects at least 1"},
		{"(1 + 2", "expected ')'"},
		{"1 + +", "expected"},
		{"abs", "expected '('"},
		{"abs(", "expected"},
	}
	for _, c := range cases {
		_, err := evalMath(c.expr)
		if err == nil {
			t.Errorf("evalMath(%q) expected error, got nil", c.expr)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("evalMath(%q) error %q does not contain %q", c.expr, err.Error(), c.wantSub)
		}
	}
}

func TestEvalMath_BackwardsCompat(t *testing.T) {
	// CLAUDE.md example: existing expressions must keep working.
	got, err := evalMath("100 * 2 * 1.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !floatEq(got, 240) {
		t.Errorf("got %v, want 240", got)
	}
}

func TestExecuteTransformMath_WithVariables(t *testing.T) {
	pCtx := NewPipelineContext()
	pCtx.Set("price", float64(123))
	pCtx.Set("qty", float64(2))

	step := &tenant.FunctionStep{
		Type:       "transform.math",
		Expression: "ceil({{price}} / {{qty}}) * {{qty}}",
		Assign:     "result",
	}
	if err := ExecuteTransformMath(nil, step, pCtx, &StepDeps{}); err != nil {
		t.Fatalf("execute error: %v", err)
	}
	v, _ := pCtx.Get("result")
	got, ok := v.(float64)
	if !ok {
		t.Fatalf("result is %T, want float64", v)
	}
	if !floatEq(got, 124) { // ceil(61.5) * 2 = 62 * 2 = 124
		t.Errorf("got %v, want 124", got)
	}
}

func TestExecuteTransformMath_ModuloFromVariable(t *testing.T) {
	pCtx := NewPipelineContext()
	pCtx.Set("n", float64(7))

	step := &tenant.FunctionStep{
		Type:       "transform.math",
		Expression: "{{n}} % 2",
		Assign:     "remainder",
	}
	if err := ExecuteTransformMath(nil, step, pCtx, &StepDeps{}); err != nil {
		t.Fatalf("execute error: %v", err)
	}
	v, _ := pCtx.Get("remainder")
	got, _ := v.(float64)
	if !floatEq(got, 1) {
		t.Errorf("got %v, want 1", got)
	}
}

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
