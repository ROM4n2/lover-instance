// Command analyze turns a chatlog-keeper export into a shareable report.
//
// It is one conversation's pipeline end to end: import the export into the
// store, rebuild the behavioral metrics, optionally score the text through the
// NLP service, and render the self-contained HTML file. Each stage commits
// before the next begins, so a run that dies partway is resumed by issuing the
// same command again rather than by starting over.
//
// Exit codes are part of the interface, because this is meant to be scripted:
// 0 for a completed run, 1 for a failure during the work, 2 for the command
// line itself being wrong. A failure never exits 0 — an import that stores
// nothing is the exact outcome this pipeline must not describe as success.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/ingest"
	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/report"
	"lover-instance/backend-go/internal/store"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2

	// sourceName is the adapter this build reads. It is part of every message's
	// identity, so it is named once here rather than repeated per call.
	sourceName = "chatlog-keeper"

	// exportJSONHint is what the user has to produce before this command can
	// do anything.
	exportJSONHint = "run the chatlog-keeper exporter first; this repository contains no code that reads the WeChat database"

	defaultNLPURL = "http://127.0.0.1:8000"

	// jobKindSentiment is the analysis_jobs.kind this command produces. It is
	// one conversation's sentiment pass, which is the unit ADR-0002 calls a
	// job: minute-scale work that must survive a restart.
	jobKindSentiment = "sentiment"

	// finishTimeout bounds the write that closes a job. It runs on a context
	// that ignores cancellation, so it is the only thing standing between a
	// wedged database and a Ctrl-C that never returns.
	finishTimeout = 5 * time.Second
)

// heartbeatInterval is how often a live run refreshes its job row, and it is a
// variable rather than a constant so a test can shorten it. It has to be well
// under jobs.StaleAfter (a 10s beat against a 90s window is nine missed beats of
// margin), because the row it writes is what tells a second CLI that this
// process is still alive.
var heartbeatInterval = 10 * time.Second

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
	export        string
	talker        string
	dbPath        string
	outPath       string
	nlpURL        string
	skipSentiment bool
	takeOver      bool
	listTalkers   bool
	// sentimentVersion is which of the service's models scores this run. It
	// is empty for "whatever the service reports as its default", which is
	// what every run did before the choice existed.
	sentimentVersion string
}

// summary is the single line the command prints on success. It is machine
// readable on purpose so a wrapper script can tell what happened without
// parsing prose.
//
// It deliberately carries no message text and no conversation key: stdout ends
// up in terminals, logs and pasted bug reports, and the report's own rule —
// identifiers are replaced by a fingerprint — applies here too. --list-talkers
// is the explicit, opt-in way to see the keys.
type summary struct {
	BehaviorModel  string `json:"behavior_model"`
	SentimentModel string `json:"sentiment_model"`
	TalkerHash     string `json:"talker_fingerprint"`

	Parsed   int `json:"parsed"`
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
	Messages int `json:"messages"`

	// Scored is what this run scored, which is zero on a re-run. Coverage is
	// what the conversation holds, which is what the report will show. They are
	// different numbers, and reporting the first as though it were the second
	// makes a finished conversation look half done — a re-run over a fully
	// scored 32,624-message export reported coverage_partial because it had
	// scored nothing itself.
	Scored   int  `json:"scored_this_run"`
	Coverage int  `json:"coverage_scored"`
	Total    int  `json:"analyzable"`
	Partial  bool `json:"coverage_partial"`

	ReportPath  string `json:"report"`
	ReportBytes int    `json:"report_bytes"`

	// Per-stage timings. A single total says a run took four seconds without
	// saying whether the cost is the import, the HTTP scoring or the render,
	// which is the difference between a usable and a useless measurement on
	// the first real-sized export.
	IngestMs    int64 `json:"ingest_ms"`
	BehaviorMs  int64 `json:"behavior_ms"`
	SentimentMs int64 `json:"sentiment_ms"`
	RenderMs    int64 `json:"render_ms"`
	ElapsedMs   int64 `json:"elapsed_ms"`
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
		fmt.Fprintf(stderr, "analyze: %v\n", err)
		return exitUsage
	}
	started := time.Now()

	result, err := execute(ctx, cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "analyze: %v\n", err)
		var usage usageError
		if errors.As(err, &usage) {
			return exitUsage
		}
		return exitFailure
	}
	if cfg.listTalkers {
		for _, talker := range result.talkers {
			fmt.Fprintln(stdout, talker)
		}
		return exitOK
	}

	result.summary.ElapsedMs = time.Since(started).Milliseconds()
	line, err := json.Marshal(result.summary)
	if err != nil {
		fmt.Fprintf(stderr, "analyze: encode summary: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, string(line))
	return exitOK
}

func parse(args []string, stderr io.Writer) (options, error) {
	var cfg options
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, `Usage: analyze --export <path> [options]

Imports a chatlog-keeper export, analyzes one conversation and writes a
self-contained HTML report. No WeChat decryption happens here: %s.

Flags:
`, exportJSONHint)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.export, "export", "", "path to the chatlog-keeper export JSON (required unless --list-talkers)")
	fs.StringVar(&cfg.talker, "talker", "", "conversation key to analyze; required when the export holds more than one")
	fs.StringVar(&cfg.dbPath, "db", defaultDBPath(), "path to the SQLite database")
	fs.StringVar(&cfg.outPath, "out", "report.html", "path of the HTML report to write")
	fs.StringVar(&cfg.nlpURL, "nlp-url", defaultNLPURL, "base URL of the NLP service")
	fs.StringVar(&cfg.sentimentVersion, "sentiment-version", "", "sentiment model version to score with; empty uses the default version the service reports on /health, and the choices are the versions listed there in available_versions")
	fs.BoolVar(&cfg.skipSentiment, "skip-sentiment", false, "rebuild behavioral metrics only; the report will show sentiment coverage as incomplete")
	fs.BoolVar(&cfg.takeOver, "take-over", false, "interrupt whatever job holds this conversation and start anyway; only correct when you know that run is dead — a live one would mean two scorers and doubled word counts")
	fs.BoolVar(&cfg.listTalkers, "list-talkers", false, "print the conversation keys already stored, one per line, and exit")

	if err := fs.Parse(args); err != nil {
		return options{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return options{}, usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	if cfg.listTalkers {
		return cfg, nil
	}
	if cfg.export == "" {
		return options{}, usagef("--export is required: there is nothing to analyze without an export to import")
	}
	if cfg.outPath == "" {
		return options{}, usagef("--out must name a file")
	}
	return cfg, nil
}

// defaultDBPath is the local-only location the design fixes. LOCALAPPDATA is
// where it goes on Windows precisely because it is not synced by default.
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

// outcome carries what run() needs after the work is done.
type outcome struct {
	talkers []string
	summary summary
}

func execute(ctx context.Context, cfg options, stderr io.Writer) (outcome, error) {
	// The check runs before the file is created, not after: a database that
	// has already been uploaded to a sync client is not recalled by deleting
	// it locally.
	for _, path := range []string{cfg.dbPath, cfg.outPath} {
		if err := store.RejectSyncedPath(path); err != nil {
			return outcome{}, err
		}
	}
	if cfg.listTalkers {
		// Opening the store creates the file, and a listing command must not
		// leave an empty database behind as a side effect of being asked what
		// is in one.
		if _, err := os.Stat(cfg.dbPath); err != nil {
			return outcome{}, fmt.Errorf("no database at %s yet: import an export first (%s)", cfg.dbPath, exportJSONHint)
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg.dbPath), 0o700); err != nil {
		return outcome{}, fmt.Errorf("create database directory: %w", err)
	}

	s, err := openStore(ctx, cfg.dbPath)
	if err != nil {
		return outcome{}, err
	}
	defer func() { _ = s.Close() }()

	if cfg.listTalkers {
		talkers, err := s.ListTalkers(ctx)
		if err != nil {
			return outcome{}, err
		}
		return outcome{talkers: talkers}, nil
	}

	at := time.Now()
	imported, err := importExport(ctx, s, cfg)
	if err != nil {
		return outcome{}, err
	}
	ingestMs := time.Since(at).Milliseconds()

	talker, err := resolveTalker(imported, cfg.talker)
	if err != nil {
		return outcome{}, err
	}

	// Unconditional, and before the report is assembled: an export imports
	// messages without touching the aggregates, so skipping this leaves the
	// report describing the previous run's conversation.
	at = time.Now()
	if err := s.RebuildBehaviorMetrics(ctx, talker); err != nil {
		return outcome{}, err
	}
	behaviorMs := time.Since(at).Milliseconds()

	at = time.Now()
	sentimentModel := ""
	scoredThisRun := 0
	if !cfg.skipSentiment {
		sentimentModel, scoredThisRun, err = scoreSentiment(ctx, s, cfg, talker, stderr)
		if err != nil {
			return outcome{}, err
		}
	}
	sentimentMs := time.Since(at).Milliseconds()

	at = time.Now()
	written, err := writeReport(ctx, s, cfg, talker, sentimentModel)
	if err != nil {
		return outcome{}, err
	}
	renderMs := time.Since(at).Milliseconds()

	messages, err := s.CountMessages(ctx, talker)
	if err != nil {
		return outcome{}, err
	}
	// Read from the same place the report reads, so the line the user sees and
	// the coverage the report states cannot disagree.
	coverage, err := s.SentimentCoverage(ctx, talker, sentimentModel)
	if err != nil {
		return outcome{}, err
	}
	return outcome{summary: summary{
		BehaviorModel:  store.BehaviorModelVersion,
		SentimentModel: sentimentModel,
		TalkerHash:     report.TalkerHash(talker),
		Parsed:         imported.Parsed,
		Inserted:       imported.Inserted,
		Updated:        imported.Updated,
		Skipped:        imported.Skipped,
		Messages:       messages,
		Scored:         scoredThisRun,
		Coverage:       coverage.Scored,
		Total:          coverage.Total,
		Partial:        coverage.Scored < coverage.Total,
		ReportPath:     cfg.outPath,
		ReportBytes:    written,
		IngestMs:       ingestMs,
		BehaviorMs:     behaviorMs,
		SentimentMs:    sentimentMs,
		RenderMs:       renderMs,
	}}, nil
}

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

func importExport(ctx context.Context, s *store.Store, cfg options) (ingest.Report, error) {
	file, err := os.Open(cfg.export)
	if err != nil {
		return ingest.Report{}, fmt.Errorf("open export: %w", err)
	}
	defer func() { _ = file.Close() }()

	imported, err := ingest.Run(ctx, s, ingest.Options{Source: sourceName, Talker: cfg.talker, Reader: file})
	if err != nil {
		return ingest.Report{}, err
	}
	return imported, nil
}

// resolveTalker picks the conversation to analyze.
//
// An export holding one conversation needs no flag, which is the common case;
// more than one is a decision only the user can make, so it is refused with
// the choices named rather than guessed at.
func resolveTalker(imported ingest.Report, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	switch len(imported.Talkers) {
	case 1:
		return imported.Talkers[0], nil
	case 0:
		return "", fmt.Errorf("the export contained no conversation to analyze: %s", exportJSONHint)
	default:
		return "", usagef("the export holds %d conversations, so --talker is required; every conversation in it has been stored already, so re-running with --talker costs only the analysis, not the import", len(imported.Talkers))
	}
}

// keepFresh runs beat on its own clock until the returned stop is called, and
// stop waits for the goroutine to exit.
//
// The beat cannot be tied to committed blocks. A block is one HTTP call that may
// legitimately take the whole block timeout, so a run can go minutes without
// touching its job row, and jobs.Recover reads silence as a dead process; the
// row would then be interrupted while this process is still scoring. Waiting for
// stop before returning is the other half: a beat that lands after the job was
// closed fails against a finished row, and one that overlaps the closing write
// is two writers on one row.
//
// A cancelled context is a second way out, so a Ctrl-C does not leave stop
// waiting for a tick that may be a minute away. every must be positive;
// time.NewTicker panics otherwise.
func keepFresh(ctx context.Context, every time.Duration, beat func(context.Context) error, onErr func(error)) (stop func()) {
	quit := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-ticker.C:
				if err := beat(ctx); err != nil {
					onErr(err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-exited
	}
}

// scoreSentiment asks the service which versions it can score with, picks one
// (the version the run asked for, or the service's default), scores the
// conversation with it, and rolls the polarity into the daily table.
//
// The run is recorded in analysis_jobs, because this is the minute-scale stage:
// a process killed halfway must be resumable, must not be started twice, and
// must leave a status a reader can trust. The job covers the scoring pass and
// its rollup together, since a "done" job whose daily table is stale would let
// the report draw a complete-looking curve from half-written data.
func scoreSentiment(ctx context.Context, s *store.Store, cfg options, talker string, stderr io.Writer) (string, int, error) {
	client, err := nlp.NewClient(cfg.nlpURL)
	if err != nil {
		return "", 0, err
	}
	health, err := client.Health(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("the NLP service at %s is not answering (%w); start it, or run with --skip-sentiment to build a behavioral-only report", cfg.nlpURL, err)
	}

	// The version this run scores with, and the version every score is recorded
	// under. An explicit --sentiment-version wins; otherwise the service's own
	// default, which is what every run used before the choice existed. Either
	// way the result is refused rather than guessed at when it is empty: a
	// report must name the model that produced it.
	version := cfg.sentimentVersion
	if version == "" {
		version = health.ModelVersion
	}
	if version == "" {
		return "", 0, fmt.Errorf("the NLP service at %s reports status %q with no lexicon version; a report must name the model that produced it, so this run is refused until the service is ready", cfg.nlpURL, health.Status)
	}
	// Checked before the slot is taken and before the first block: a version the
	// service does not offer would otherwise come back as a 400 somewhere in the
	// middle of a long pass, and leave a job row explaining a pass that could
	// never have worked.
	if err := checkRequestedVersion(cfg.sentimentVersion, health.AvailableVersions); err != nil {
		return "", 0, err
	}

	// Before the slot is asked for: a killed run left its job in 'running', and
	// without this the next start would find the slot held by a job nobody is
	// working on. The count goes to stderr rather than the summary line, which
	// is a fixed machine-readable contract, and it names no conversation and no
	// message.
	interrupted, err := jobs.Recover(ctx, s, talker, jobKindSentiment)
	if err != nil {
		return "", 0, err
	}
	if interrupted > 0 {
		fmt.Fprintf(stderr, "analyze: marked %d interrupted job(s) from a previous run\n", interrupted)
	}

	job, created, err := jobs.Start(ctx, s, talker, jobKindSentiment, version)
	if err != nil {
		return "", 0, err
	}
	if !created {
		// Start reports whether this call made the job; a false means another
		// run owns the (talker, kind) slot. Scoring anyway is the second pass
		// over one conversation that ADR-0002 exists to prevent, so this run
		// stops before it sends a single block. The message names the job rather
		// than the conversation: stderr ends up in pasted bug reports.
		//
		// Recover ran immediately before this, and it interrupts every run whose
		// heartbeat has stopped. So a slot still held here is held by a process
		// that is alive, and saying so is what stops the reader from deleting a
		// row — or waiting — for a job nobody is working on.
		if !cfg.takeOver {
			return "", 0, fmt.Errorf("a %s job already holds this conversation (job %d, status %q) and its heartbeat is still current, so another process is still working on it; this run is not starting a second scoring pass — if that process is gone, wait for its heartbeat to go stale (about %s) and re-run, or pass --take-over to say it is dead", jobKindSentiment, job.ID, job.Status, jobs.StaleAfter)
		}
		// The operator has asserted the other owner is gone. Say so out loud:
		// this is the one path that can put two scorers on one conversation, and
		// the only thing between that and doubled word counts is the person
		// reading this line.
		fmt.Fprintf(stderr, "analyze: taking over job %d (status %q) because --take-over was given; if that run is in fact alive, both will write and word counts will double\n", job.ID, job.Status)
		taken, err := jobs.TakeOver(ctx, s, talker, jobKindSentiment, version)
		if err != nil {
			return "", 0, err
		}
		job = taken
	}

	// The beat and the per-block callback write the same row from two goroutines,
	// so the outcome is kept behind a mutex: run under -race with a plain field
	// and this is a data race, and the run's reported failure would be whichever
	// write the scheduler let land last.
	var heartbeatMu sync.Mutex
	var heartbeatErr error
	noteHeartbeat := func(err error) {
		if err == nil {
			return
		}
		heartbeatMu.Lock()
		defer heartbeatMu.Unlock()
		// The first failure wins: later heartbeats would be recorded against a
		// job whose progress is already known to be stale.
		if heartbeatErr == nil {
			heartbeatErr = err
		}
	}
	heartbeatFailure := func() error {
		heartbeatMu.Lock()
		defer heartbeatMu.Unlock()
		return heartbeatErr
	}
	beat := func(ctx context.Context) error {
		return recordJobProgress(ctx, s, job.ID, talker, version)
	}
	stopHeartbeat := keepFresh(ctx, heartbeatInterval, beat, noteHeartbeat)
	defer stopHeartbeat()

	progress, runErr := analyze.RunSentimentProgress(ctx, s, client, talker, version, nlp.MaxBatchSize,
		func(_ int, _ int) {
			noteHeartbeat(recordJobProgress(ctx, s, job.ID, talker, version))
		})
	// Before anything that closes the run, and it waits for the goroutine: the
	// closing write and a beat landing at the same moment are two writers on one
	// row, and a beat after Finish is an error against a finished job.
	stopHeartbeat()
	if runErr == nil {
		// A heartbeat that failed is a database that stopped recording the run.
		// The scores are committed, so re-running costs only the missing blocks
		// — cheaper than a job row that understates what was done.
		runErr = heartbeatFailure()
	}
	if runErr == nil {
		runErr = s.RebuildSentimentMetrics(ctx, talker, version)
	}
	if runErr == nil {
		// The last write is the authoritative one. A run over a conversation
		// that was already fully scored sends no blocks at all, so without this
		// a finished job would be recorded as 0/0 and a dashboard would show a
		// run that never started. Coverage is also what a resumed run needs:
		// the per-block numbers count this run's messages, not the
		// conversation's.
		runErr = recordJobProgress(ctx, s, job.ID, talker, version)
	}

	// The job's outcome is derived from the same error the command reports, so
	// the row and the exit code cannot tell different stories. The error text
	// never carries message content: the NLP client reports status codes and
	// codes only, and the pipeline's own wrapping counts blocks.
	status := jobs.StatusDone
	message := ""
	if runErr != nil {
		status = jobs.StatusFailed
		message = runErr.Error()
		if ctx.Err() != nil {
			status = jobs.StatusCanceled
			message = ctx.Err().Error()
		}
	}
	// The closing write must not inherit the run's cancellation. Ctrl-C is the
	// case it exists for: database/sql checks the context before it will hand
	// out the connection, so a cancelled context means the UPDATE is never
	// sent, the row stays 'running' forever, and the message says the job could
	// not be closed — the one outcome a canceled job must not produce. The
	// deadline keeps that from turning a wedged database into a hang.
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancelFinish()
	if finishErr := jobs.Finish(finishCtx, s, job.ID, status, message); finishErr != nil {
		if runErr != nil {
			return version, progress.Scored, fmt.Errorf("%w (and the job could not be closed: %v)", runErr, finishErr)
		}
		return version, progress.Scored, finishErr
	}
	if runErr != nil {
		return version, progress.Scored, runErr
	}
	return version, progress.Scored, nil
}

// checkRequestedVersion refuses a sentiment version the service does not
// advertise, and names what it does offer so the fix is a copy-paste rather
// than a documentation hunt.
//
// Two cases deliberately pass:
//   - nothing was requested, so the run uses the service's own default, which
//     is available by definition;
//   - the service advertises no menu at all (one older than version selection),
//     which carries no information to check against. The requested version is
//     still sent, and the service's own refusal is what reports a wrong one.
func checkRequestedVersion(requested string, available []string) error {
	if requested == "" || len(available) == 0 {
		return nil
	}
	for _, offered := range available {
		if offered == requested {
			return nil
		}
	}
	return fmt.Errorf("the NLP service does not offer sentiment version %q; it scores with %s — re-run with one of those, or omit --sentiment-version to use the service's default", requested, strings.Join(available, ", "))
}

// recordJobProgress writes how much of the conversation is scored, not how much
// this run scored.
//
// ADR-0002 asks the job row for absolute values because a resumed run only
// handles the missing blocks: recording "12 scored" on a conversation with
// 22,093 would show a finished conversation as barely started. Coverage is the
// only number that answers "how far along is this?" for both a fresh run and a
// run that found nothing left to do.
func recordJobProgress(ctx context.Context, s *store.Store, jobID int64, talker, modelVersion string) error {
	coverage, err := s.SentimentCoverage(ctx, talker, modelVersion)
	if err != nil {
		return err
	}
	return jobs.Heartbeat(ctx, s, jobID, coverage.Scored, coverage.Total)
}

// writeReport renders the report next to its final name and renames it into
// place, so an interrupted write never leaves a truncated file that looks like
// a finished report.
func writeReport(ctx context.Context, s *store.Store, cfg options, talker, sentimentModel string) (int, error) {
	view, err := report.Collect(ctx, s, talker, store.BehaviorModelVersion, sentimentModel)
	if err != nil {
		return 0, err
	}
	var buffer bytes.Buffer
	if err := report.Render(&buffer, view); err != nil {
		return 0, err
	}
	if refs := report.ExternalRefs(buffer.Bytes()); len(refs) != 0 {
		return 0, fmt.Errorf("the rendered report would reach the network in %d place(s), which ADR-0003 forbids; refusing to write it: %v", len(refs), refs)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.outPath), 0o700); err != nil {
		return 0, fmt.Errorf("create report directory: %w", err)
	}
	temporary := cfg.outPath + ".tmp"
	if err := os.WriteFile(temporary, buffer.Bytes(), 0o600); err != nil {
		return 0, fmt.Errorf("write report: %w", err)
	}
	if err := os.Rename(temporary, cfg.outPath); err != nil {
		return 0, fmt.Errorf("move report into place: %w", err)
	}
	return buffer.Len(), nil
}
