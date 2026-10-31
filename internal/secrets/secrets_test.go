package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	tricky := map[string]string{
		"A": `p@ss"w'o\rd`,
		"B": "line1\nline2",
		"C": "$HOME ${PATH} `x` !y #z",
		"D": "",
		"E": " spaced ",
		"F": "007",
		"G": "+5",
		"H": `back\slash \n literal`,
	}
	for k, v := range tricky {
		if err := s.Set("shop", k, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Values("shop")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range tricky {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "env", "shop.env"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if d, _ := os.Stat(filepath.Join(dir, "env")); d.Mode().Perm() != 0o700 {
		t.Fatalf("env dir mode %v, want 0700", d.Mode().Perm())
	}

	if err := s.Unset("shop", "A"); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.Names("shop"); len(names) != 7 || names[0] != "B" {
		t.Fatalf("names after unset: %v", names)
	}
	for _, bad := range []string{"../x", "Shop", ""} {
		if err := s.Set(bad, "A", "v"); err == nil {
			t.Errorf("tenant id %q accepted", bad)
		}
	}
	if err := s.Set("shop", "1BAD", "v"); err == nil {
		t.Error("invalid name accepted")
	}
}

func TestConcurrentSetsKeepEveryValue(t *testing.T) {
	s := New(t.TempDir())
	done := make(chan error)
	for i := 0; i < 20; i++ {
		go func(i int) { done <- s.Set("shop", "K"+string(rune('A'+i)), "v") }(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if names, _ := s.Names("shop"); len(names) != 20 {
		t.Fatalf("lost updates: %d of 20 names kept", len(names))
	}
}
