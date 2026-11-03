package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Init creates a new project in dir: supacraft.json, .env, .gitignore and
// .mcp.json (so MCP clients pick up `supacraft mcp`). It refuses when
// supacraft.json already exists.
func Init(dir, name string, auth bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		return fmt.Errorf("%s already exists in %s", FileName, dir)
	}
	if err := New(dir, name, auth).Save(); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(dir, ".env"), "# Secrets referenced from supacraft.json as \"${NAME}\".\n"); err != nil {
		return err
	}
	if err := appendLines(filepath.Join(dir, ".gitignore"), StateDir+"/", ".env"); err != nil {
		return err
	}
	return addMCPConfig(filepath.Join(dir, ".mcp.json"))
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func appendLines(path string, lines ...string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, l := range lines {
		if !have[l] {
			add = append(add, l)
		}
	}
	if len(add) == 0 {
		return nil
	}
	out := string(existing)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += strings.Join(add, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

// addMCPConfig registers the supacraft server in .mcp.json, keeping any
// servers already there.
func addMCPConfig(path string) error {
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["supacraft"] = map[string]any{"command": "supacraft", "args": []string{"mcp"}}
	cfg["mcpServers"] = servers
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
