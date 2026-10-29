// Package app runs the SupaCraft API server (REST, auth, files, pipeline
// functions and the cron scheduler) in-process.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/attisvalentin/supacraft/internal/config"
	"github.com/attisvalentin/supacraft/internal/cronjob"
	"github.com/attisvalentin/supacraft/internal/errorlog"
	"github.com/attisvalentin/supacraft/internal/handler"
	"github.com/attisvalentin/supacraft/internal/tenant"
)

// Server is a running API server.
type Server struct {
	Store    *tenant.CachedStore
	Pools    *tenant.PoolManager
	ErrorLog *errorlog.ErrorLog
	Crons    *cronjob.CronManager // nil when cron is disabled
	Addr     string
}

// Reload makes config changes on disk visible immediately.
func (s *Server) Reload(ctx context.Context) {
	s.Store.Invalidate()
	if s.Crons != nil {
		s.Crons.Reload(ctx)
	}
}

// Start binds the listener and serves until ctx is cancelled. The returned
// channel yields the serve error (nil on clean shutdown) once stopped.
func Start(ctx context.Context, cfg *config.Config, withCron bool) (*Server, <-chan error, error) {
	store, err := tenant.NewFileStore(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("tenant store: %w", err)
	}
	cached := tenant.NewCachedStore(store)
	pools := tenant.NewPoolManager(cfg)

	errLog, err := errorlog.Open(cfg.ErrorLogDB)
	if err != nil {
		return nil, nil, fmt.Errorf("open error log: %w", err)
	}
	errorlog.Global = errLog

	pools.StartEvictor(ctx)
	errLog.StartCleanup(ctx)

	ln, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		errLog.Close()
		return nil, nil, fmt.Errorf("listen on :%s: %w", cfg.Port, err)
	}
	srv := &http.Server{Handler: handler.NewRouter(cached, pools, errLog, cfg)}

	var crons *cronjob.CronManager
	if withCron && !cfg.DisableCron {
		crons = cronjob.NewCronManager(cached, pools)
		crons.Start(ctx)
	}

	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	stopped := make(chan error, 1)
	go func() {
		var err error
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			srv.Shutdown(shutdownCtx)
			cancel()
			err = <-done
		case err = <-done:
		}
		if crons != nil {
			<-crons.Stop().Done()
		}
		errLog.Close()
		stopped <- err
	}()

	return &Server{Store: cached, Pools: pools, ErrorLog: errLog, Crons: crons, Addr: ln.Addr().String()}, stopped, nil
}
