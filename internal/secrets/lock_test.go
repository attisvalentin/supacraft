package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHelperHoldLock runs in a child process: it takes the lock, signals
// that it holds it, and keeps it until the parent creates the release file.
func TestHelperHoldLock(t *testing.T) {
	dir := os.Getenv("SUPACRAFT_LOCK_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	unlock, err := lockFile(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "held"), nil, 0o600)
	for {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	unlock()
}

// The file lock must exclude another process, not just other goroutines
// (those are already serialised by the in-process mutex).
func TestLockFile_ExcludesOtherProcesses(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	child.Env = append(os.Environ(), "SUPACRAFT_LOCK_DIR="+dir)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "held")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never took the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	acquired := make(chan func())
	go func() {
		unlock, err := lockFile(filepath.Join(dir, ".lock"))
		if err != nil {
			t.Error(err)
			close(acquired)
			return
		}
		acquired <- unlock
	}()
	select {
	case <-acquired:
		t.Fatal("lock taken while another process holds it")
	case <-time.After(300 * time.Millisecond):
	}

	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case unlock := <-acquired:
		if unlock != nil {
			unlock()
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock not taken after the other process released it")
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
}
