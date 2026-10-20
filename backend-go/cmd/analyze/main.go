// Command analyze turns a chatlog-keeper export into a shareable report.
//
// It is one conversation's pipeline end to end: import the export into the
// store, rebuild the behavioral metrics, optionally score the text through the
// NLP service, and render the self-contained HTML file. Each stage commits
// before the next begins, so a run that dies partway is resumed by issuing the
// same command again rather than by starting over.
//
// The pipeline itself lives in internal/app, which the next entry point shares;
// what stays here is this command's own surface: its flags, its usage text and
// the "analyze:" prefix on everything it prints.
//
// Exit codes are part of the interface, because this is meant to be scripted:
// 0 for a completed run, 1 for a failure during the work, 2 for the command
// line itself being wrong. A failure never exits 0 — an import that stores
// nothing is the exact outcome this pipeline must not describe as success.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"lover-instance/backend-go/internal/app"
)

// The exit codes are the shared contract internal/app defines. They are aliased
// here so this package keeps naming them the way its tests always have.
const (
	exitOK      = app.ExitOK
	exitFailure = app.ExitFailure
	exitUsage   = app.ExitUsage
)

// summary is the single line the command prints on success. Its shape is fixed
// in internal/app so every entry point prints the same fields.
type summary = app.Summary

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run parses this command's flags, hands the configuration to the shared
// orchestration, and reports what it said. The error is printed here rather
// than in the app layer because the prefix names this command: the next entry
// point prints its own name over the same error.
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
		fmt.Fprintf(stderr, "analyze: %v\n", err)
		return exitUsage
	}
	_, code, err := app.Run(ctx, cfg, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "analyze: %v\n", err)
	}
	return code
}

// flagParseError is a rejected flag. The flag package has already printed the
// message and the usage text, so run() only has to choose the exit code. It
// stays in this package because it describes this package's flag set; the
// shared layer never sees a flag.
type flagParseError struct{ err error }

func (e flagParseError) Error() string { return e.err.Error() }
func (e flagParseError) Unwrap() error { return e.err }

// parse turns this command's flags into the shared Options. Every default it
// registers comes from app.DefaultOptions, which reads them from
// internal/app/paths.go — the one place a default lives — so this command
// cannot drift from the entry point that comes next.
func parse(args []string, stderr io.Writer) (app.Options, error) {
	cfg := app.DefaultOptions()
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: analyze --export <path> [options]

Imports a chatlog-keeper export, analyzes one conversation and writes a
self-contained HTML report. No WeChat decryption happens here: %s.

Flags:
`, app.ExportJSONHint)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.ExportPath, "export", "", "path to the chatlog-keeper export JSON (required unless --list-talkers)")
	fs.StringVar(&cfg.Talker, "talker", "", "conversation key to analyze; required when the export holds more than one")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "path to the SQLite database")
	fs.StringVar(&cfg.OutPath, "out", cfg.OutPath, "path of the HTML report to write")
	fs.StringVar(&cfg.NLPURL, "nlp-url", cfg.NLPURL, "base URL of the NLP service")
	fs.StringVar(&cfg.SentimentVersion, "sentiment-version", "", "sentiment model version to score with; empty uses the default version the service reports on /health, and the choices are the versions listed there in available_versions")
	fs.BoolVar(&cfg.SkipSentiment, "skip-sentiment", false, "rebuild behavioral metrics only; the report will show sentiment coverage as incomplete")
	fs.BoolVar(&cfg.TakeOver, "take-over", false, "interrupt whatever job holds this conversation and start anyway; only correct when you know that run is dead — a live one would mean two scorers and doubled word counts")
	fs.BoolVar(&cfg.ListTalkers, "list-talkers", false, "print the conversation keys already stored, one per line, and exit")
	fs.StringVar(&cfg.OllamaURL, "ollama-url", cfg.OllamaURL, "base URL of the local OpenAI-compatible model; only a loopback address is accepted")
	fs.StringVar(&cfg.OllamaModel, "ollama-model", "", "local model name used to write the narration; empty means no narration")
	fs.BoolVar(&cfg.NoNarrative, "no-narrative", false, "skip the narration section even when --ollama-model is set")
	fs.BoolVar(&cfg.RescoreWords, "rescore-words", false, "delete this model version's scores and word counts for this conversation before scoring, so monthly words are rebuilt; without it nothing is deleted")

	if err := fs.Parse(args); err != nil {
		return app.Options{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return app.Options{}, app.Usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	if cfg.ListTalkers {
		return cfg, nil
	}
	if cfg.ExportPath == "" {
		return app.Options{}, app.Usagef("--export is required: there is nothing to analyze without an export to import")
	}
	if cfg.OutPath == "" {
		return app.Options{}, app.Usagef("--out must name a file")
	}
	// Rescanning words is a scoring action: with scoring skipped there would be
	// nothing to rebuild, and deleting the old rows first would only lose them.
	if cfg.RescoreWords && cfg.SkipSentiment {
		return app.Options{}, app.Usagef("--rescore-words needs the sentiment pass, so it cannot be combined with --skip-sentiment")
	}
	return cfg, nil
}

// defaultDBPath forwards to app.DefaultDBPath. The default itself lives in
// internal/app/paths.go so the next entry point reads the same one; this
// package's tests pin it through the command's own seam.
func defaultDBPath() string { return app.DefaultDBPath() }

// keepFresh forwards to app.KeepFresh, so the heartbeat's tests keep exercising
// the implementation the run itself uses rather than a copy of it.
func keepFresh(ctx context.Context, every time.Duration, beat func(context.Context) error, onErr func(error)) func() {
	return app.KeepFresh(ctx, every, beat, onErr)
}
