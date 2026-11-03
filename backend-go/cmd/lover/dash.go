package main

// `lover dash`: make sure the dashboard is built, then serve it.
//
// Decision D1 keeps the dashboard and keeps `dist` out of the repository, which
// leaves exactly one question this command has to answer before it can serve
// anything: is the built bundle newer than the sources it was built from? If it
// is missing or stale, `npm run build` is the one step between source and a
// servable dashboard, and it is run here rather than documented because a person
// who typed `lover dash` has already said they want a dashboard.
//
// npm being unavailable is not a failure of the run: the report path is
// independent of the dashboard by design, so the message says the dashboard was
// skipped and exits with exitEnv, which is the code that means "this machine is
// missing a tool", not "the analysis is broken".
//
// The server is built with `go build` and then run as this command's own child,
// rather than with `go run`. The two cost the same compile; the difference is
// that a built binary is a process this command can put in a process group and
// stop as a group, and that `go run`'s pipe-copying can hold the server's output
// back. The binary lands in the temporary directory, so nothing is left in the
// repository and nothing goes stale between runs — and this command deletes it
// once the process it built has been reaped, because nobody else will.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"lover-instance/backend-go/internal/app"
)

// dashOptions is the parsed `lover dash` command line.
type dashOptions struct {
	dbPath string
	nlpURL string
	addr   string
}

// distIndex is the file whose presence means a usable build, and whose timestamp
// is what "stale" is measured against. It is the same path internal/web searches,
// because that is the directory the vite config writes into.
func distIndex(root string) string {
	return filepath.Join(root, "backend-go", "internal", "web", "dist", "index.html")
}

// frontendSrc is the tree a build is made from.
func frontendSrc(root string) string {
	return filepath.Join(root, "frontend", "src")
}

func parseDashFlags(args []string, stderr io.Writer) (dashOptions, error) {
	defaults := app.DefaultOptions()
	cfg := dashOptions{
		dbPath: defaults.DBPath,
		nlpURL: defaults.NLPURL,
		addr:   app.DefaultDashboardAddr(),
	}
	fs := flag.NewFlagSet("lover dash", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: lover dash [options]

Makes sure the dashboard is built (running npm run build when the bundle is
missing or older than frontend/src) and serves it with cmd/server. The server
binds the loopback interface only: this is a single-user tool on one machine and
there is no login to put in front of a wider bind.

If npm is unavailable the dashboard is skipped and this says so; the report path
is unaffected, because a report is a self-contained HTML file that needs no
server at all.

Flags:
`)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.dbPath, "db", cfg.dbPath, "path to the SQLite database")
	fs.StringVar(&cfg.nlpURL, "nlp-url", cfg.nlpURL, "base URL of the NLP service, probed by the dashboard's health badge")
	fs.StringVar(&cfg.addr, "addr", cfg.addr, "host:port to listen on; keep the host on 127.0.0.1, this is a local-only tool")
	if err := fs.Parse(args); err != nil {
		return dashOptions{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return dashOptions{}, app.Usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	if cfg.addr == "" {
		return dashOptions{}, app.Usagef("--addr must name a host and port, for example %s", app.DefaultDashboardAddr())
	}
	return cfg, nil
}

// runDash builds if it must, then serves until the context is cancelled.
func runDash(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parseDashFlags(args, stderr)
	if err != nil {
		return classifyParseError(err, stderr)
	}
	root, err := repoRoot()
	if err != nil {
		// A release build is one file: there is no source tree to find, and
		// nothing under it is needed, because the bundle is embedded and the
		// server is this same executable.
		if !dashboardEmbedded {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitEnv
		}
		root, _ = os.Executable()
		root = filepath.Dir(root)
	}

	if !dashboardEmbedded {
		stale, err := distStale(root)
		if err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitEnv
		}
		if stale {
			if !npmAvailable() {
				fmt.Fprintf(stderr, "lover: Dashboard 跳过：npm 不在 PATH 上，前端无法构建；报告路径不受影响（`lover` 生成的报告是自包含 HTML，浏览器直接打开即可）\n")
				return exitEnv
			}
			fmt.Fprintf(stderr, "lover: the dashboard bundle is missing or older than frontend/src; running npm run build\n")
			if err := runNPMBuild(ctx, root, stderr); err != nil {
				fmt.Fprintf(stderr, "lover: Dashboard 跳过：%v；报告路径不受影响\n", err)
				return exitEnv
			}
		}
	}

	port := nlpPort("http://" + cfg.addr)
	if port == "" {
		port = cfg.addr
	}
	if err := ensurePortFree("the dashboard server", port); err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		if asPortError(err) {
			return exitPort
		}
		return exitEnv
	}

	nlpP := nlpPort(cfg.nlpURL)
	if nlpP != "" {
		if err := ensurePortFree("the NLP service", nlpP); err == nil {
			python := pythonCommand()
			serviceDir := filepath.Join(root, "nlp-service")
			// A release build ships no Python service; the dashboard falls
			// back to sentiment already stored in the database, so a missing
			// service directory is not a reason to refuse to serve.
			if _, statErr := os.Stat(serviceDir); statErr == nil {
				if missing, _ := probeDependencies(ctx, python, serviceDir, dependencyModules); len(missing) == 0 {
					svc, err := startService(ctx, serviceConfig{python: python, port: nlpP, dir: serviceDir, stderr: stderr})
					if err == nil {
						defer svc.stop()
						fmt.Fprintf(stderr, "lover: started the NLP service on port %s\n", nlpP)
						_ = svc.waitReady(ctx, cfg.nlpURL, serviceReadyTimeout)
					}
				}
			}
		}
	}

	server, err := startDashboardServer(ctx, cfg, root, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	// Two defers, registered in this order on purpose: they run
	// last-in-first-out, so stop() — which waits for the process to be reaped
	// — runs before the cleanup that deletes the executable it was running.
	// Both ways out of the select below reach them: the server finishing on
	// its own, and this command being cancelled.
	defer server.runCleanup()
	defer server.stop()
	fmt.Fprintf(stdout, "lover: dashboard on http://%s (Ctrl-C to stop)\n", cfg.addr)
	openReport("http://"+cfg.addr, stderr)

	select {
	case <-ctx.Done():
	case <-server.done:
		// The server exiting on its own is a failure worth naming: a port taken
		// by a process that started after this command checked, or a database it
		// could not open.
		if server.exited() && server.waitErr != nil {
			fmt.Fprintf(stderr, "lover: the dashboard server stopped: %v\n", server.waitErr)
			return exitRun
		}
	}
	return exitOK
}

// distStale reports whether the bundle has to be rebuilt.
//
// Missing is stale. Older than any file under frontend/src is stale. Anything
// else — including a src tree that is not there at all, which is what a source
// export looks like — is not, because there is nothing to compare against and a
// build that exists is better than no dashboard.
func distStale(root string) (bool, error) {
	index := distIndex(root)
	info, err := os.Stat(index)
	if err != nil {
		return true, nil
	}
	src := frontendSrc(root)
	if _, err := os.Stat(src); err != nil {
		return false, nil
	}
	stale := false
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, err := entry.Info()
		if err != nil {
			return nil
		}
		if file.ModTime().After(info.ModTime()) {
			stale = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("could not read frontend/src: %w", err)
	}
	return stale, nil
}

// npmAvailable is the seam a test replaces, so "npm is not installed" can be
// exercised on a machine that has it.
var npmAvailable = func() bool {
	_, err := exec.LookPath("npm")
	return err == nil
}

// runNPMBuild builds the dashboard, streaming its output because a first build
// downloads and compiles a great deal and silence would look like a hang.
var runNPMBuild = func(ctx context.Context, root string, stderr io.Writer) error {
	cmd := npmCommand(ctx, "run", "build")
	cmd.Dir = filepath.Join(root, "frontend")
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm run build failed (%v)", err)
	}
	return nil
}

// npmCommand wraps the interpreter on Windows, where npm is a .cmd batch file.
// Running it through cmd.exe is the shape that works on every Windows build: a
// direct exec of the .cmd happens to work on the toolchain this was written
// against too, and relying on that would be relying on an accident.
func npmCommand(ctx context.Context, args ...string) *exec.Cmd {
	npm := append([]string{"npm"}, args...)
	if runtime.GOOS == "windows" {
		npm = append([]string{"cmd", "/c"}, npm...)
	}
	return exec.CommandContext(ctx, npm[0], npm[1:]...)
}

// startDashboardServer starts whatever serves the dashboard. A source build
// compiles cmd/server and supervises the child; a release build has no toolchain
// to compile anything, so it re-runs this same binary as `lover serve`. Both
// return a supervised child, because the process-group kill and the single
// output stream are what a double-clicked tool needs either way.
var startDashboardServer func(ctx context.Context, cfg dashOptions, root string, stderr io.Writer) (*child, error)

// buildAndSuperviseServer builds cmd/server and runs it as this command's child.
//
// The compiled binary is this command's own artefact and this command is what
// deletes it: `go build` was told to write into the temporary directory, which
// keeps it out of the repository, and nothing else is going to notice a file
// named after a PID that has already exited. The removal is registered on the
// child rather than done here, because the file is open for as long as the
// process runs and only the caller knows when the wait has returned.
func buildAndSuperviseServer(ctx context.Context, cfg dashOptions, root string, stderr io.Writer) (*child, error) {
	binary, err := buildServer(ctx, root, stderr)
	if err != nil {
		return nil, err
	}
	c, err := newChild(ctx, serverCommand(ctx, binary, cfg, root), filepath.Join(root, "backend-go"), stderr)
	if err != nil {
		// Nothing was started, so nothing holds the file: remove it now
		// rather than leave an executable that never ran in the temporary
		// directory.
		_ = os.Remove(binary)
		return nil, err
	}
	// Ignored on purpose, like every other cleanup in this command: a stale
	// file in the temporary directory is untidy, not a failure of the run,
	// and reporting it would replace a real exit code with a complaint about
	// housekeeping.
	c.cleanup = func() { _ = os.Remove(binary) }
	return c, nil
}

// serverCommand is the invocation of the compiled server, given the binary to
// run and the directory the bundle lives under.
//
// It is built with CommandContext and not Command: newChild gives the child a
// Cancel that kills the whole process group, and os/exec refuses to start a
// command that has a Cancel but no context — the pair is what makes a
// cancelled run kill the group rather than only the direct child. That is not a
// theoretical refusal: with exec.Command here, `lover dash` reports the Start
// failure and exits 3 without ever serving a page.
//
// It is also the seam a test replaces with a command that exits at once, so the
// removal of the temporary binary can be asserted without paying for a real
// build: what is under test is what happens to the file, not which process
// opened it.
var serverCommand = func(ctx context.Context, binary string, cfg dashOptions, root string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, "--db", cfg.dbPath, "--nlp-url", cfg.nlpURL, "--addr", cfg.addr)
	// The authoritative way to tell the server where the bundle is: it is the
	// variable internal/web reads first, and passing it beats relying on the
	// server's own search order finding the same directory.
	cmd.Env = append(os.Environ(), "LOVER_FRONTEND_DIST="+filepath.Join(root, "backend-go", "internal", "web", "dist"))
	return cmd
}

// buildServer compiles cmd/server into the temporary directory. A missing
// toolchain is an environment failure, and the message says what to install.
var buildServer = func(ctx context.Context, root string, stderr io.Writer) (string, error) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("the Go toolchain was not found on PATH (%v), so the dashboard server cannot be built; install Go, or run `go build ./cmd/server` in backend-go yourself", err)
	}
	binary := filepath.Join(os.TempDir(), fmt.Sprintf("lover-server-%d", os.Getpid()))
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.CommandContext(ctx, goCmd, "build", "-o", binary, "./cmd/server")
	cmd.Dir = filepath.Join(root, "backend-go")
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building cmd/server failed (%v)", err)
	}
	return binary, nil
}
