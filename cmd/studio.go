package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/studio"
	"github.com/spf13/cobra"
)

var (
	studioPort   string
	studioNoOpen bool
	studioProxy  string
)

var studioCmd = &cobra.Command{
	Use:   "studio",
	Short: "Browse the database in a read-only web UI",
	Long: `Starts a read-only data browser for the project database. Sign in with
STUDIO_SECRET (a random one is generated and printed when it isn't set).

  supacraft studio
  supacraft studio --proxy https://api.example.com   # remote server with STUDIO_SECRET set`,
	RunE: func(cmd *cobra.Command, args []string) error {
		secret := os.Getenv("STUDIO_SECRET")
		generated := false
		if secret == "" {
			b := make([]byte, 12)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			secret, generated = hex.EncodeToString(b), true
		}

		var src studio.DataSource
		var err error
		if studioProxy != "" {
			if generated {
				return errors.New("--proxy needs STUDIO_SECRET set to the remote server's secret")
			}
			src, err = studio.NewProxySource(studioProxy, secret)
		} else {
			p, perr := loadProject()
			if perr != nil {
				return perr
			}
			src, err = studio.NewSQLiteSource(&config.Config{DataDir: p.DataDir()})
		}
		if err != nil {
			return err
		}
		defer src.Close()

		ln, err := net.Listen("tcp", ":"+studioPort)
		if err != nil {
			return fmt.Errorf("cannot bind :%s: %w", studioPort, err)
		}
		srv := &http.Server{Handler: studio.NewServer(src, secret).Handler()}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "studio server error: %v\n", err)
			}
		}()

		url := "http://localhost:" + studioPort
		fmt.Printf("SupaCraft Studio on %s\n", url)
		if generated {
			fmt.Printf("  Password: %s  (set STUDIO_SECRET to choose one)\n", secret)
		}
		if !studioNoOpen {
			_ = studio.OpenBrowser(url)
		}

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	},
}

func init() {
	studioCmd.Flags().StringVar(&studioPort, "port", "4983", "studio port")
	studioCmd.Flags().BoolVar(&studioNoOpen, "no-open", false, "don't open the browser")
	studioCmd.Flags().StringVar(&studioProxy, "proxy", "", "browse a remote SupaCraft API instead of the local database")
	rootCmd.AddCommand(studioCmd)
}
