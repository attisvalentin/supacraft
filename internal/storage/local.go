package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LocalDriver struct {
	basePath string
}

func NewLocalDriver(basePath string) *LocalDriver {
	return &LocalDriver{basePath: basePath}
}

func (d *LocalDriver) resolvePath(path string) (string, error) {
	resolved := filepath.Join(d.basePath, filepath.Clean(path))
	// Ensure the resolved path is under basePath (prevent traversal)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	base, err := filepath.Abs(d.basePath)
	if err != nil {
		return "", fmt.Errorf("invalid base path: %w", err)
	}
	if !strings.HasPrefix(abs, base+string(filepath.Separator)) && abs != base {
		return "", fmt.Errorf("path traversal detected")
	}
	return abs, nil
}

func (d *LocalDriver) Put(_ context.Context, path string, reader io.Reader, _ int64) error {
	resolved, err := d.resolvePath(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	f, err := os.Create(resolved)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		os.Remove(resolved)
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

func (d *LocalDriver) Get(_ context.Context, path string) (io.ReadCloser, error) {
	resolved, err := d.resolvePath(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return f, nil
}

func (d *LocalDriver) Delete(_ context.Context, path string) error {
	resolved, err := d.resolvePath(path)
	if err != nil {
		return err
	}

	if err := os.Remove(resolved); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", path)
		}
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

func (d *LocalDriver) Exists(_ context.Context, path string) (bool, error) {
	resolved, err := d.resolvePath(path)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat file: %w", err)
	}
	return true, nil
}
