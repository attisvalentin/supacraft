package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/attisvalentin/supacraft/internal/app"
	"github.com/attisvalentin/supacraft/internal/mcpserver"
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
		p, err := loadProject()
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if _, err := p.Apply(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "supacraft: %v\n", err)
		}

		opts := mcpserver.Options{
			Dir:     p.Dir,
			APIURL:  "http://" + net.JoinHostPort(dialHost(mcpHost), mcpPort),
			Version: appVersion,
		}
		if !mcpNoServe {
			cfg, err := serverConfig(p, mcpHost, mcpPort)
			if err != nil {
				return err
			}
			srv, _, err := app.Start(ctx, cfg, true)
			if err != nil {
				fmt.Fprintf(os.Stderr, "supacraft: not serving the API (%v); using %s\n", err, opts.APIURL)
			} else {
				opts.Reload = srv.Reload
				fmt.Fprintf(os.Stderr, "supacraft: API for %q on %s\n", p.Name(), opts.APIURL)
			}
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
