package cmd

import (
	"fmt"
	"path/filepath"

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
		name := filepath.Base(dir)
		if len(args) == 1 {
			name = args[0]
		}
		if err := project.Init(dir, name, initAuth); err != nil {
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
