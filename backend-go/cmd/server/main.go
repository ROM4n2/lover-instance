// Command server serves the local dashboard's API.
//
// It is the long-lived counterpart of cmd/analyze: the same store, the same job
// table and the same analysis code, reached over HTTP instead of a flag list.
// It binds 127.0.0.1 on purpose — this is a single-user tool on one machine,
// the data behind it is a plaintext chat log, and there is no login to put in
// front of a wider bind.
//
// Exit codes are part of the interface, and they match cmd/analyze: 0 for a
// clean shutdown, 1 for a failure during the work, 2 for the command line
// itself being wrong.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"lover-instance/backend-go/internal/api"
	"lover-instance/backend-go/internal/store"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2

	defaultNLPURL = "http://127.0.0.1:8000"

	// defaultAddr is loopback and nothing else. "127.0.0.1:8787" rather than
	// ":8787" because an empty host means every interface: on a laptop joined to
	// a café's wifi that is a chat database answering requests from the room.
	defaultAddr = "127.0.0.1:8787"

	// The server's timeouts. A zero value in http.Server means "no limit",
	// which for ReadHeaderTimeout is the slowloris shape: one client that
	// opens a socket and sends half a line holds the connection forever.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// version is what /api/health echoes back so a dashboard can say which build is
// answering. It is a variable rather than a constant so a release build can set
// it with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// usageError marks a bad invocation, which is a different outcome from a run
// that failed: the first is fixed by changing the command, the second by
// changing the world.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{err: fmt.Errorf(format, args...)}
}

// flagParseError is a rejected flag. The flag package has already printed the
// message and the usage text, so run() only has to choose the exit code.
type flagParseError struct{ err error }

func (e flagParseError) Error() string { return e.err.Error() }
func (e flagParseError) Unwrap() error { return e.err }

// options is the parsed command line.
type options struct {
	dbPath string
	nlpURL string
	addr   string
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parse(args, stderr)
	if err != nil {
		// Asking for help is not a failure, and the flags package has already
		// written the help text.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		// A rejected flag was reported by the flags package along with its
		// usage text, so reprinting it here would double the message.
		var badFlag flagParseError
		if errors.As(err, &badFlag) {
			return exitUsage
		}
		fmt.Fprintf(stderr, "server: %v\n", err)
		return exitUsage
	}

	if err := execute(ctx, cfg, stderr); err != nil {
		fmt.Fprintf(stderr, "server: %v\n", err)
		var usage usageError
		if errors.As(err, &usage) {
			return exitUsage
		}
		return exitFailure
	}
	return exitOK
}

func parse(args []string, stderr io.Writer) (options, error) {
	var cfg options
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: server [options]

Serves the local dashboard API on %s. There is no login and no remote access:
the process binds the loopback interface only.

Flags:
`, defaultAddr)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.dbPath, "db", defaultDBPath(), "path to the SQLite database")
	fs.StringVar(&cfg.nlpURL, "nlp-url", defaultNLPURL, "base URL of the NLP service, probed by /api/health")
	fs.StringVar(&cfg.addr, "addr", defaultAddr, "host:port to listen on; keep the host on 127.0.0.1, this is a local-only tool")

	if err := fs.Parse(args); err != nil {
		return options{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return options{}, usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	if cfg.addr == "" {
		return options{}, usagef("--addr must name a host and port, for example %s", defaultAddr)
	}
	return cfg, nil
}

// defaultDBPath is the local-only location the design fixes, and the same one
// cmd/analyze defaults to: the two commands are two faces of one database, and
// a divergence here would mean the server reporting on a database the CLI never
// wrote. LOCALAPPDATA is where it goes on Windows precisely because it is not
// synced by default.
func defaultDBPath() string {
	if root := os.Getenv("LOCALAPPDATA"); root != "" {
		return filepath.Join(root, "lover-instance", "data.db")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "lover-instance.db"
	}
	return filepath.Join(dir, "lover-instance", "data.db")
}

// execute opens the store, binds the socket and serves until ctx is cancelled.
func execute(ctx context.Context, cfg options, stderr io.Writer) error {
	// Before the file is created, not after: a database already uploaded to a
	// sync client is not recalled by deleting it locally.
	if err := store.RejectSyncedPath(cfg.dbPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.dbPath), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}

	s, err := openStore(ctx, cfg.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Bound before Serve, so "the port is taken" is a startup failure with an
	// exit code rather than an error that arrives only after the process has
	// claimed to have started.
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.New(api.Options{Store: s, NLPURL: cfg.nlpURL, Version: version, Log: stderr}),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	// The line names the address actually bound and nothing else. The database
	// path is not printed: it is not needed to use the dashboard (the browser
	// needs a URL, not a path) and a terminal or a pasted log is not where a
	// filesystem layout belongs. No talker is printed because the server has
	// not seen one.
	fmt.Fprintf(stderr, "server: listening on %s\n", listener.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		// Serve returns ErrServerClosed only after Shutdown, which cannot
		// have happened yet in this branch; anything else is real.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve on %s: %w", cfg.addr, err)
	case <-ctx.Done():
	}

	// Shutdown on a context that is not the cancelled one: the whole point of
	// the call is to let in-flight requests finish after the signal arrived.
	// The deadline keeps a stuck connection from turning Ctrl-C into a hang.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down the server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve on %s: %w", cfg.addr, err)
	}
	return nil
}

// openStore opens the database and applies the migrations, so a fresh install
// answers /api/health instead of failing on a missing table.
func openStore(ctx context.Context, path string) (*store.Store, error) {
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
