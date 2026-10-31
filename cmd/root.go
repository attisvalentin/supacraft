// Package cmd implements the supacraft CLI.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/spf13/cobra"
)

var (
	projectDir string
	appVersion string
)

var rootCmd = &cobra.Command{
	Use:   "supacraft",
	Short: "AI-native backend in a single binary",
	Long: `SupaCraft turns supacraft.json into a REST backend: SQLite database, auth,
file storage, pipeline functions (/fn/...) and cron jobs.

Let your AI agent build it over MCP (supacraft mcp), or edit supacraft.json
yourself and run supacraft dev.`,
	SilenceUsage: true,
}

// Execute runs the CLI.
func Execute(version string) error {
	appVersion = version
	rootCmd.Version = version
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	return err
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.PersistentFlags().StringVarP(&projectDir, "dir", "C", ".", "project directory (containing supacraft.json)")
}

// centralConfig is the server config for a central data directory that
// holds many projects' tenants (supacraft serve --data-dir).
func centralConfig(dataDir, host, port string) (*config.Config, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg.Host = host
	cfg.Port = port
	cfg.DataDir = abs
	if os.Getenv("ERROR_LOG_DB") == "" {
		cfg.ErrorLogDB = filepath.Join(abs, "errors.db")
	}
	if os.Getenv("STORAGE_PATH") == "" {
		cfg.StoragePath = filepath.Join(abs, "storage")
	}
	return cfg, nil
}

func loadProject() (*project.Project, error) {
	return project.Load(projectDir)
}

// serverConfig derives the API server config from the project, keeping all
// runtime state under the project's .supacraft directory.
func serverConfig(p *project.Project, host, port string) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg.Host = host
	cfg.Port = port
	cfg.DataDir = p.DataDir()
	cfg.ErrorLogDB = filepath.Join(cfg.DataDir, "errors.db")
	if os.Getenv("STORAGE_PATH") == "" {
		cfg.StoragePath = filepath.Join(cfg.DataDir, "storage")
	}
	cfg.DefaultTenant = p.TenantID()
	return cfg, nil
}
