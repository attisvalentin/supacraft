// Package tenantfile reads and writes compiled tenant configs under
// {DataDir}/tenants/{id}.json — the same path internal/tenant.FileStore reads.
package tenantfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/attisvalentin/supacraft/internal/config"
)

// Client owns the tenants directory under DataDir.
type Client struct {
	dir string
}

// New returns a Client rooted at {DataDir}/tenants. The directory is created
// on demand so it works against a fresh project.
func New(cfg *config.Config) (*Client, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("data_dir is not configured (set --dir to a project)")
	}
	dir := filepath.Join(cfg.DataDir, "tenants")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create tenants dir %s: %w", dir, err)
	}
	return &Client{dir: dir}, nil
}

// Ping verifies the tenants directory is writable.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := os.Stat(c.dir); err != nil {
		return fmt.Errorf("tenants dir not accessible: %w", err)
	}
	return nil
}

func (c *Client) tenantPath(uuid string) string {
	return filepath.Join(c.dir, uuid+".json")
}

// WriteTenant atomically writes the payload to {DataDir}/tenants/{uuid}.json
// (write to temp file + rename) so partial writes never appear to the API.
func (c *Client) WriteTenant(ctx context.Context, uuid string, payload map[string]any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, uuid+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, c.tenantPath(uuid)); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// ReadTenant returns the payload at {DataDir}/tenants/{uuid}.json, or nil if missing.
func (c *Client) ReadTenant(ctx context.Context, uuid string) (map[string]any, error) {
	data, err := os.ReadFile(c.tenantPath(uuid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", uuid, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", uuid, err)
	}
	return payload, nil
}

// DeleteTenant removes the tenant JSON file.
func (c *Client) DeleteTenant(ctx context.Context, uuid string) error {
	if err := os.Remove(c.tenantPath(uuid)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", uuid, err)
	}
	return nil
}

// Close is a no-op for the file backend.
func (c *Client) Close() error {
	return nil
}
