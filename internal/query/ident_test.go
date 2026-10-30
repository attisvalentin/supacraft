package query

import "testing"

func TestQuoteIdent(t *testing.T) {
	for in, want := range map[string]string{
		"posts":                 `"posts"`,
		`x" , secret FROM t --`: `"x"" , secret FROM t --"`,
		`a\"`:                   `"a\"""`,
	} {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}
