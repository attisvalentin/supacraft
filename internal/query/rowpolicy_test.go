package query

import (
	"reflect"
	"testing"

	"github.com/attisvalentin/supacraft/internal/tenant"
)

func TestBuildRowPolicy(t *testing.T) {
	claims := &tenant.AuthClaims{UserID: "u1", Email: "a@b.c", Role: "admin"}
	cases := []struct {
		expr     string
		wantSQL  string
		wantArgs []any
	}{
		{"user_id = $auth.id", "user_id = ?", []any{"u1"}},
		// Arguments follow the placeholders, including repeats.
		{"$auth.role = 'admin' OR owner = $auth.id OR editor = $auth.id", "? = 'admin' OR owner = ? OR editor = ?", []any{"admin", "u1", "u1"}},
		{"email = $auth.email AND user_id = $auth.id", "email = ? AND user_id = ?", []any{"a@b.c", "u1"}},
		{"published = 1", "published = 1", nil},
	}
	for _, c := range cases {
		gotSQL, gotArgs, err := BuildRowPolicy(c.expr, claims, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		if gotSQL != c.wantSQL || !reflect.DeepEqual(gotArgs, c.wantArgs) {
			t.Errorf("%q: got %q %v, want %q %v", c.expr, gotSQL, gotArgs, c.wantSQL, c.wantArgs)
		}
	}

	if _, _, err := BuildRowPolicy("$auth.role = 'admin'", nil, 0); err == nil {
		t.Error("expected an error without an authenticated user")
	}
}
