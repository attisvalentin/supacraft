package cmd

import (
	"strings"
	"testing"
)

func TestGenerateUnit_QuotesPaths(t *testing.T) {
	unit, err := generateUnit("/usr/local/bin/supacraft", "app", "/home/app", "/srv/my apps/100%$HOME", "--data-dir")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`WorkingDirectory=/srv/my apps/100%%$HOME`,
		`ExecStart="/usr/local/bin/supacraft" serve --data-dir "/srv/my apps/100%%$$HOME"`,
		`Environment="HOME=/home/app"`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	for _, bad := range []string{"/srv/a\nExecStartPre=/bin/evil", `/srv/a\`} {
		if _, err := generateUnit("/bin/sc", "app", "/home/app", bad, "--dir"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
