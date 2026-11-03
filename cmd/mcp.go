package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/attisvalentin/supacraft/internal/app"
	"github.com/attisvalentin/supacraft/internal/mcpserver"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/spf13/cobra"
)

var (
	mcpPort    string
	mcpHost    string
	mcpNoServe bool
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the MCP server for AI agents (stdio)",
	Long: `Serves the project to an AI agent over MCP on stdin/stdout. The agent can read
and change supacraft.json (every change is validated, saved, migrated and
reloaded), call the API and query the database.

The API itself is started in-process on --port, unless that port is already
taken (e.g. by supacraft dev), in which case the tools talk to that server.

Register it with your agent, e.g. in .mcp.json (supacraft init does this):
  {"mcpServers": {"supacraft": {"command": "supacraft", "args": ["mcp"]}}}`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// stdout belongs to the MCP protocol: everything else goes to stderr.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		dir, err := filepath.Abs(projectDir)
		if err != nil {
			return err
		}
		opts := mcpserver.Options{
			Dir:     dir,
			APIURL:  "http://" + net.JoinHostPort(dialHost(mcpHost), mcpPort),
			Version: appVersion,
		}
		// start applies the project and serves its API in-process.
		start := func(ctx context.Context) (func(context.Context), error) {
			p, err := loadProject()
			if err != nil {
				return nil, err
			}
			if _, err := p.Apply(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "supacraft: %v\n", err)
			}
			if mcpNoServe {
				return nil, nil
			}
			cfg, err := serverConfig(p, mcpHost, mcpPort)
			if err != nil {
				return nil, err
			}
			srv, _, err := app.Start(ctx, cfg, true)
			if errors.Is(err, syscall.EADDRINUSE) {
				// e.g. supacraft dev already serves it: the tools use that.
				fmt.Fprintf(os.Stderr, "supacraft: not serving the API (%v); using %s\n", err, opts.APIURL)
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "supacraft: API for %q on %s\n", p.Name(), opts.APIURL)
			return srv.Reload, nil
		}
		if _, err := os.Stat(filepath.Join(dir, project.FileName)); errors.Is(err, os.ErrNotExist) {
			// No project yet: the agent can create one with init_project.
			fmt.Fprintf(os.Stderr, "supacraft: no %s in %s; waiting for init_project\n", project.FileName, dir)
			// The API lives as long as this process, not the tool call.
			opts.Started = func(context.Context) (func(context.Context), error) { return start(ctx) }
		} else if opts.Reload, err = start(ctx); err != nil {
			// The tools still work on the project; only calls to the API fail.
			fmt.Fprintf(os.Stderr, "supacraft: not serving the API (%v)\n", err)
		}
		return mcpserver.Run(ctx, opts)
	},
}

// dialHost is the address to reach a server listening on host.
func dialHost(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1"
	}
	return host
}

func init() {
	mcpCmd.Flags().StringVar(&mcpHost, "host", envOr("HOST", "127.0.0.1"), "interface the API listens on (0.0.0.0 = all)")
	mcpCmd.Flags().StringVarP(&mcpPort, "port", "p", envOr("PORT", "8080"), "HTTP port of the API")
	mcpCmd.Flags().BoolVar(&mcpNoServe, "no-serve", false, "don't start the API in-process")
	rootCmd.AddCommand(mcpCmd)
}
