package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/attisvalentin/supacraft/internal/app"
	"github.com/attisvalentin/supacraft/internal/project"
	"github.com/spf13/cobra"
)

var (
	servePort   string
	serveNoCron bool
	devHost     string
	serveHost   string
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
	Short: "Apply supacraft.json and run the API (production)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServer(false)
	},
}

func init() {
	// dev is for one machine, so it stays off the network unless asked;
	// serve is the production entry point and listens on all interfaces.
	devCmd.Flags().StringVar(&devHost, "host", envOr("HOST", "127.0.0.1"), "interface to listen on (0.0.0.0 = all)")
	serveCmd.Flags().StringVar(&serveHost, "host", envOr("HOST", "0.0.0.0"), "interface to listen on")
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
