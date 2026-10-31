// Package secrets stores the server-side values of "${NAME}" references for
// the tenants of a central data directory, one file per tenant:
// {dataDir}/env/{tenant}.json, readable only by the server's user. Values
// never appear in tenant configs; the tenant store resolves references when
// it loads a config.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/attisvalentin/supacraft/internal/envref"
)

// tenantRe matches tenant ids (the same rule as the tenant middleware).
var tenantRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// ErrNotUTF8 rejects values JSON can't store byte for byte.
var ErrNotUTF8 = errors.New("secret value is not valid UTF-8 text")

// MaxValueBytes bounds one secret value.
const MaxValueBytes = 64 << 10

// Store reads and writes secrets under {dataDir}/env.
type Store struct {
	dir string
}

func New(dataDir string) *Store {
	return &Store{dir: filepath.Join(dataDir, "env")}
}

func (s *Store) path(tenantID string) (string, error) {
	if !tenantRe.MatchString(tenantID) {
		return "", fmt.Errorf("invalid tenant id %q", tenantID)
	}
	return filepath.Join(s.dir, tenantID+".json"), nil
}

// Values returns a tenant's secrets (empty when none are set).
func (s *Store) Values(tenantID string) (map[string]string, error) {
	p, err := s.path(tenantID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	vals := map[string]string{}
	if err := json.Unmarshal(data, &vals); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(p), err)
	}
	return vals, nil
}

// Names returns the sorted names of a tenant's secrets.
func (s *Store) Names(tenantID string) ([]string, error) {
	vals, err := s.Values(tenantID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// mu serialises read-modify-write within this process; lockFile (an OS
// file lock) does it across processes, e.g. "secrets --data-dir" next to a
// running server.
var mu sync.Mutex

func (s *Store) locked(fn func() error) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(s.dir, ".lock"))
	if err != nil {
		return fmt.Errorf("lock secrets: %w", err)
	}
	defer unlock()
	return fn()
}

// Set stores one secret.
func (s *Store) Set(tenantID, name, value string) error {
	if !envref.NameRe.MatchString(name) {
		return fmt.Errorf("invalid secret name %q (letters, digits and _, not starting with a digit)", name)
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("secret value too large (max %d bytes)", MaxValueBytes)
	}
	if !utf8.ValidString(value) {
		return ErrNotUTF8
	}
	return s.locked(func() error {
		vals, err := s.Values(tenantID)
		if err != nil {
			return err
		}
		vals[name] = value
		return s.write(tenantID, vals)
	})
}

// Unset removes one secret; removing an unknown name is not an error.
func (s *Store) Unset(tenantID, name string) error {
	return s.locked(func() error {
		vals, err := s.Values(tenantID)
		if err != nil {
			return err
		}
		if _, ok := vals[name]; !ok {
			return nil
		}
		delete(vals, name)
		return s.write(tenantID, vals)
	})
}

// Remove deletes all of a tenant's secrets.
func (s *Store) Remove(tenantID string) error {
	p, err := s.path(tenantID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) write(tenantID string, vals map[string]string) error {
	p, err := s.path(tenantID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// JSON round-trips any value exactly; .env quoting can't (a trailing
	// backslash, number-like values).
	content, err := json.MarshalIndent(vals, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, tenantID+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(content, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
