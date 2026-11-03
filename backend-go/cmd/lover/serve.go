package main

// `lover serve`: serve the dashboard API from this process.
//
// A release build is one file and has no Go toolchain on the user's machine, so
// `lover dash` cannot compile cmd/server there. This command is the same handler
// without a second binary: the same store, the same job manager, the same
// api.New — reached directly instead of through a child process.
//
// It exists as its own command rather than as an inline branch of `dash` so the
// dashboard can still be a supervised child process in both builds: the parent
// keeps the process-group kill and the single stdout, and only the thing it
// supervises changes.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/api"
	"lover-instance/backend-go/internal/app"
	"lover-instance/backend-go/internal/store"
)

const (
	serveReadHeaderTimeout = 10 * time.Second
	serveIdleTimeout       = 60 * time.Second
	serveShutdownTimeout   = 5 * time.Second
)

// serveVersion is what /api/health echoes back. Set with -ldflags at release
// time so a user can tell which build is answering.
var serveVersion = "dev"

type serveOptions struct {
	dbPath string
	nlpURL string
	addr   string
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	dbPath := app.DefaultDBPath()
	nlpURL := app.DefaultNLPURL()
	addr := app.DefaultDashboardAddr()

	fs := flag.NewFlagSet("lover serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&dbPath, "db", dbPath, "path to the SQLite database")
	fs.StringVar(&nlpURL, "nlp-url", nlpURL, "base URL of the NLP service")
	fs.StringVar(&addr, "addr", addr, "host:port to listen on")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	if err := serveUntilCancelled(ctx, serveOptions{dbPath: dbPath, nlpURL: nlpURL, addr: addr}, stderr); err != nil {
		fmt.Fprintf(stderr, "lover serve: %v\n", err)
		return exitRun
	}
	return exitOK
}

func serveUntilCancelled(ctx context.Context, cfg serveOptions, stderr io.Writer) error {
	if err := store.RejectSyncedPath(cfg.dbPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.dbPath), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}

	s, err := openDashboardStore(ctx, cfg.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	if os.Getenv("LOVER_GIN_DEBUG") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	manager := api.NewJobManager(s, cfg.nlpURL)
	defer manager.Shutdown()

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.New(api.Options{Store: s, NLPURL: cfg.nlpURL, Version: serveVersion, Log: stderr, Jobs: manager}),
		ReadHeaderTimeout: serveReadHeaderTimeout,
		IdleTimeout:       serveIdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	fmt.Fprintf(stderr, "lover serve: listening on http://%s\n", cfg.addr)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serveShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// openDashboardStore opens the database through the same path every other
// command uses, so a schema difference cannot appear between faces of one file.
func openDashboardStore(ctx context.Context, path string) (*store.Store, error) {
	dsn, err := store.DSN(path)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
