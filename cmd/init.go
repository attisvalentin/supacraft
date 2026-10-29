package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/spf13/cobra"
)

var initAuth bool

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Create a new project in the current directory",
	Long: `Creates supacraft.json, .env, .gitignore and .mcp.json (so Claude Code and
other MCP clients pick up the supacraft MCP server automatically).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(projectDir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, project.FileName)); err == nil {
			return fmt.Errorf("%s already exists in %s", project.FileName, dir)
		}
		name := filepath.Base(dir)
		if len(args) == 1 {
			name = args[0]
		}

		if err := project.New(dir, name, initAuth).Save(); err != nil {
			return err
		}
		if err := writeIfMissing(filepath.Join(dir, ".env"), "# Secrets referenced from supacraft.json as \"${NAME}\".\n"); err != nil {
			return err
		}
		if err := appendLines(filepath.Join(dir, ".gitignore"), project.StateDir+"/", ".env"); err != nil {
			return err
		}
		if err := addMCPConfig(filepath.Join(dir, ".mcp.json")); err != nil {
			return err
		}

		fmt.Printf("✓ Created %s project in %s\n\n", name, dir)
		fmt.Println("Next:")
		fmt.Println("  supacraft dev        start the API on http://localhost:8080")
		fmt.Println("  Ask your AI agent to build the backend — .mcp.json registers `supacraft mcp`.")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initAuth, "auth", true, "enable built-in user auth (users table, /auth/*)")
	rootCmd.AddCommand(initCmd)
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
