package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/attisvalentin/supacraft/internal/app"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/attisvalentin/supacraft/internal/tenant"
	"github.com/spf13/cobra"
)

var (
	servePort     string
	serveNoCron   bool
	devHost       string
	serveHost     string
	serveDataDir  string
	serveDefaultT string
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Run the API and re-apply supacraft.json on every change",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServer(true)
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the API in production: one project, or every tenant in a data directory",
	Long: `Without --data-dir, applies supacraft.json and serves that one project.

With --data-dir, serves every tenant in a central data directory from one
process, picked per request by the X-Tenant-ID header. Add or update a
tenant with "supacraft apply --data-dir" (or a deploy); the server picks it
up without a restart.

  supacraft serve --data-dir /var/lib/supacraft`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if serveDataDir != "" {
			return runCentral()
		}
		return runServer(false)
	},
}

func init() {
	// dev is for one machine, so it stays off the network unless asked;
	// serve is the production entry point and listens on all interfaces.
	devCmd.Flags().StringVar(&devHost, "host", envOr("HOST", "127.0.0.1"), "interface to listen on (0.0.0.0 = all)")
	serveCmd.Flags().StringVar(&serveHost, "host", envOr("HOST", "0.0.0.0"), "interface to listen on")
	serveCmd.Flags().StringVar(&serveDataDir, "data-dir", os.Getenv("SUPACRAFT_DATA_DIR"), "serve every tenant in this central data directory (env SUPACRAFT_DATA_DIR)")
	serveCmd.Flags().StringVar(&serveDefaultT, "default-tenant", os.Getenv("DEFAULT_TENANT"), "with --data-dir: tenant for requests without X-Tenant-ID")
	for _, c := range []*cobra.Command{devCmd, serveCmd} {
		c.Flags().StringVarP(&servePort, "port", "p", envOr("PORT", "8080"), "HTTP port")
		c.Flags().BoolVar(&serveNoCron, "no-cron", false, "don't run cron jobs")
		rootCmd.AddCommand(c)
	}
}

func runServer(watch bool) error {
	p, err := loadProject()
	if err != nil {
		return err
	}
	if _, err := applyAndReport(context.Background(), p); err != nil {
		if !watch {
			return err
		}
		fmt.Fprintf(os.Stderr, "✗ %v\n  (fix supacraft.json — it is re-applied on save)\n", err)
	}

	host := serveHost
	if watch {
		host = devHost
	}
	cfg, err := serverConfig(p, host, servePort)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv, stopped, err := app.Start(ctx, cfg, !serveNoCron)
	if err != nil {
		return err
	}
	fmt.Printf("\nSupaCraft API for %q on http://localhost:%s\n", p.Name(), cfg.Port)
	if watch {
		fmt.Println("Watching supacraft.json and .env — Ctrl+C to stop.")
		go watchProject(ctx, p.Dir, func() {
			fresh, err := project.Load(p.Dir)
			if err == nil {
				_, err = applyAndReport(ctx, fresh)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "✗ %v\n", err)
				return
			}
			srv.Reload(ctx)
		})
	}
	return <-stopped
}

// runCentral serves every tenant in serveDataDir and reloads when a tenant
// config is added, changed, disabled or removed.
func runCentral() error {
	cfg, err := centralConfig(serveDataDir, serveHost, servePort)
	if err != nil {
		return err
	}
	cfg.DefaultTenant = serveDefaultT
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv, stopped, err := app.Start(ctx, cfg, !serveNoCron)
	if err != nil {
		return err
	}
	tenants, err := tenant.ListTenants(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	fmt.Printf("SupaCraft API on http://%s serving %s (%d tenants)\n", srv.Addr, cfg.DataDir, len(tenants))
	for _, t := range tenants {
		state := ""
		if t.Disabled {
			state = " (disabled)"
		}
		fmt.Printf("  • %s%s\n", t.ID, state)
	}
	if cfg.DefaultTenant == "" {
		fmt.Println("Requests choose a tenant with the X-Tenant-ID header.")
	}
	go watchTenants(ctx, cfg.DataDir, func() {
		fmt.Println("↻ tenants changed, reloading")
		srv.Reload(ctx)
	})
	return <-stopped
}

// watchTenants polls the tenants directory and calls onChange after any
// config or disabled marker changes.
func watchTenants(ctx context.Context, dataDir string, onChange func()) {
	dir := filepath.Join(dataDir, "tenants")
	snapshot := func() string {
		entries, _ := os.ReadDir(dir)
		var s strings.Builder
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !strings.HasSuffix(e.Name(), ".tmp") {
				fmt.Fprintf(&s, "%s|%s|%d;", e.Name(), info.ModTime(), info.Size())
			}
		}
		return s.String()
	}
	last := snapshot()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if now := snapshot(); now != last {
				last = now
				onChange()
			}
		}
	}
}

// watchProject polls supacraft.json and .env and calls onChange after edits.
// Polling avoids editor rename-on-save quirks of file notification APIs.
func watchProject(ctx context.Context, dir string, onChange func()) {
	files := []string{filepath.Join(dir, project.FileName), filepath.Join(dir, ".env")}
	last := modTimes(files)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := modTimes(files)
			if now != last {
				last = now
				fmt.Println("\n↻ change detected")
				onChange()
			}
		}
	}
}

func modTimes(files []string) string {
	var s string
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			s += info.ModTime().String() + fmt.Sprint(info.Size())
		}
		s += "|"
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
