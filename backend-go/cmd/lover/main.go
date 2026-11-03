// Command lover is the one entry point a person is meant to type.
//
// It owns everything a first run needs and nothing else: it checks that the
// Python dependencies are importable, starts the NLP service as its own child
// process, waits for that service to report ready, runs the shared
// orchestration in internal/app and hands the finished report to the system's
// opener. `sync` is the same run with a weekly-update output line, `dash` serves
// the dashboard, `model` is the explicit model menu and `doctor` reports what is
// missing without changing anything.
//
// Two rules shape this package:
//
//   - It holds no default of its own. Every path and URL comes from
//     internal/app, which is the single source cmd/analyze reads too. A second
//     copy of the service's own address here would be the "one rule, two
//     implementations" defect the shared package exists to prevent, and a test
//     in this package fails if one appears.
//   - It imports nothing of the pipeline. No ingest, no scoring, no rendering:
//     those live in internal/app and are reached through app.Run, so a wrapper
//     cannot grow a second implementation of the analysis.
//
// Exit codes are part of the interface, because this is meant to be scripted and
// double-clicked alike. A failure never exits 0, and a failure that a person can
// fix never exits with the code a broken run uses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"lover-instance/backend-go/internal/app"
)

// The exit codes this command ends with.
//
// Three are the shared contract internal/app defines, aliased rather than
// redefined so a script that already knows cmd/analyze's codes does not have to
// learn a second table: exitRun is app.ExitFailure (the analysis itself failed)
// and exitUsage is app.ExitUsage (the command line was wrong).
//
// The other three are this command's own, because they classify failures the
// shared layer never sees: a machine that cannot run the service at all. A run
// that could not start is not a run that failed, and telling those apart — with
// a message naming the port, the PID or the missing package — is what keeps one
// command from becoming a black box.
const (
	exitOK      = app.ExitOK      // 0 — a completed run
	exitRun     = app.ExitFailure // 1 — the analysis itself failed (app.ExitFailure)
	exitUsage   = app.ExitUsage   // 2 — the command line was wrong (app.ExitUsage)
	exitEnv     = 3               // this machine is missing something the run needs
	exitPort    = 4               // the port the NLP service needs is already taken
	exitService = 5               // the service started but never became healthy
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches to one subcommand. An empty argument list is the first run:
// "lover" with nothing after it means exactly that, and it is the command the
// README tells a person to type.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runFirstRun(ctx, nil, stdout, stderr, false)
	}
	switch args[0] {
	case "run":
		return runFirstRun(ctx, args[1:], stdout, stderr, false)
	case "sync":
		return runFirstRun(ctx, args[1:], stdout, stderr, true)
	case "dash":
		return runDash(ctx, args[1:], stdout, stderr)
	case "model":
		return runModel(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "demo":
		return runDemo(ctx, args[1:], stdout, stderr)
	case "pull":
		return runPull(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stderr)
		return exitOK
	default:
		fmt.Fprintf(stderr, "lover: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return exitUsage
	}
}

// printUsage writes the whole command surface, including the exit codes, because
// a tool whose failures are classified has to say what the classes are.
func printUsage(stderr io.Writer) {
	fmt.Fprint(stderr, `Usage: lover [command]

Commands:
  lover                 first run: check the Python dependencies, start the NLP
                        service, analyze the newest export and open the report
  lover sync            weekly update: the same run, reporting only what is new
  lover dash            make sure the dashboard is built, then serve it
  lover model list      show which model versions are installed; never downloads
  lover model add <v>   download and verify one model version
  lover model verify    re-hash the installed weights and list what does not match
  lover model rm <v>    remove one model version's cache; needs --yes
  lover doctor          report what is missing, changing nothing
  lover demo            generate a self-contained report using embedded sample data
  lover pull            automatically extract latest records via sidecar and sync

Every command takes -h for its own flags. The analysis itself is internal/app's,
shared with the analyze command: this binary owns starting services and
classifying failures, not a second copy of the pipeline.

Exit codes:
  0  the run completed
  1  the analysis failed (the run itself, not the machine)
  2  the command line was wrong
  3  a dependency is missing (Python packages, npm, the Go toolchain)
  4  the port the service needs is taken; the message names the holder's PID
  5  the service started but never reported ready within 60s

No WeChat decryption happens anywhere in this repository: the export is produced
by an external tool, and this command only reads the file it left.
`)
}

// flagParseError is a rejected flag. The flag package has already printed the
// message and the usage text, so the caller only has to choose the exit code. It
// stays in this package because it describes this package's flag set.
type flagParseError struct{ err error }

func (e flagParseError) Error() string { return e.err.Error() }
func (e flagParseError) Unwrap() error { return e.err }

// classify turns a parse failure into the exit code the table above promises.
// Asking for help is not a failure, and a rejected flag was already reported by
// the flags package along with the usage text.
func classifyParseError(err error, stderr io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var badFlag flagParseError
	if errors.As(err, &badFlag) {
		return exitUsage
	}
	fmt.Fprintf(stderr, "lover: %v\n", err)
	return exitUsage
}

// repoRoot finds the repository this binary was built from.
//
// The search walks up from the working directory and then from the executable,
// because the two ways this command runs are `go run ./cmd/lover` from
// backend-go (where the executable is a temporary file and only the working
// directory knows) and a built binary copied anywhere (where only the executable
// knows). LOVER_REPO_ROOT short-circuits both, which is how a test points this
// at a throwaway tree.
func repoRoot() (string, error) {
	if env := os.Getenv("LOVER_REPO_ROOT"); env != "" {
		return env, nil
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, start := range starts {
		dir := start
		for range 8 {
			if isRepoRoot(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("could not find the repository root (a directory holding backend-go/go.mod and nlp-service/app); set LOVER_REPO_ROOT to it")
}

// isRepoRoot recognises the repository by the two directories this command
// needs, rather than by a .git folder: a source export has no .git and this
// command has to work from one.
func isRepoRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "backend-go", "go.mod")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "nlp-service", "app", "main.py")); err != nil {
		return false
	}
	return true
}

// pythonCommand is the interpreter every subprocess this command spawns uses.
// LOVER_PYTHON overrides it, which is how a test hands the supervisor a fake
// service process and how a person with several Pythons on PATH picks one.
func pythonCommand() string {
	if env := os.Getenv("LOVER_PYTHON"); env != "" {
		return env
	}
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}
