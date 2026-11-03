package main

// The command surface and the one rule that keeps this package from becoming a
// second implementation of the CLI: every default is read from internal/app.
//
// TestDefaultsComeFromTheSharedLayer compares values, and
// TestNoSecondCopyOfADefault reads this package's own source. The two together
// are what make the rule checkable: a literal that happens to compute the same
// path today passes the first and fails the second, and a literal that computes a
// different path fails both.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"lover-instance/backend-go/internal/app"
)

type invocation struct {
	code   int
	stdout string
	stderr string
}

// syncBuffer is a bytes.Buffer that tolerates concurrent writes.
//
// It is needed because a child process's output is copied into the same writer
// this command writes its own lines to, from os/exec's own goroutine. os.Stderr
// needs no such guard — the kernel serializes writes to one descriptor — but a
// plain buffer does not, and the race shows up as output that silently vanishes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// invoke runs the command the way main does, under a context the test controls.
func invoke(t *testing.T, ctx context.Context, args ...string) invocation {
	t.Helper()
	out, errOut := &syncBuffer{}, &syncBuffer{}
	code := run(ctx, args, out, errOut)
	return invocation{code: code, stdout: out.String(), stderr: errOut.String()}
}

// newRepo builds a throwaway repository and points the command at it.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"backend-go",
		"backend-go/internal/web/dist",
		"nlp-service/app",
		"frontend/src",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, body := range map[string]string{
		"backend-go/go.mod":       "module test\n",
		"nlp-service/app/main.py": "",
		"frontend/src/main.js":    "// a source file, newer than any build\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Setenv("LOVER_REPO_ROOT", root)
	return root
}

// stubProbe makes the dependency question answer itself, so a test about
// something else does not need a Python interpreter.
func stubProbe(t *testing.T, missing ...string) {
	t.Helper()
	restore := probeDependencies
	probeDependencies = func(context.Context, string, string, []string) ([]string, error) { return missing, nil }
	t.Cleanup(func() { probeDependencies = restore })
}

// stubRegistryModules keeps the model commands from reaching for an interpreter.
func stubRegistryModules(t *testing.T) {
	t.Helper()
	restore := probeRegistryModules
	probeRegistryModules = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { probeRegistryModules = restore })
}

// stubReport keeps a test from opening a browser.
func stubReport(t *testing.T) {
	t.Helper()
	restore := openReport
	openReport = func(string, io.Writer) {}
	t.Cleanup(func() { openReport = restore })
}

// openedURL records what the run handed to the system opener, so a test can
// assert the dashboard URL without launching a browser.
type openedURL struct{ value string }

func stubOpening(t *testing.T) *openedURL {
	t.Helper()
	seen := &openedURL{}
	restore := openReport
	openReport = func(target string, _ io.Writer) { seen.value = target }
	t.Cleanup(func() { openReport = restore })
	return seen
}

// TestNoArgumentsOpensTheDashboard pins the double-click contract: a person who
// runs the binary with no arguments gets the interactive panel, not a file
// written to disk and a program that exits before they can read it. `lover run`
// is the pipeline's own name, and it is asserted separately below.
func TestNoArgumentsOpensTheDashboard(t *testing.T) {
	root := newRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "backend-go", "internal", "web", "dist"), 0o755); err != nil {
		t.Fatalf("create dist: %v", err)
	}
	if err := os.WriteFile(distIndex(root), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write dist index: %v", err)
	}
	stubServer(t)
	opened := stubOpening(t)
	stubFreePorts(t)

	got := invoke(t, context.Background())

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(opened.value, "http://") {
		t.Errorf("no dashboard URL was opened; opened = %q", opened.value)
	}
}

// stubFreePorts makes the port questions answer "yes, free" so the test is about
// dispatch rather than about what else is listening on this machine.
func stubFreePorts(t *testing.T) {
	t.Helper()
	restore := checkPortFree
	checkPortFree = func(string, string) error { return nil }
	t.Cleanup(func() { checkPortFree = restore })
}

// TestRunSubcommandIsThePipeline pins the other half: the report path still has
// a name of its own, so making the empty argument list open the dashboard does
// not strand it.
func TestRunSubcommandIsThePipeline(t *testing.T) {
	newRepo(t)
	stubProbe(t, "onnxruntime")

	got := invoke(t, context.Background(), "run")

	// A missing dependency without --yes is reported, never installed: that is
	// the run's first question, and answering it proves `run` reached the
	// pipeline rather than the usage path.
	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitEnv, got.stderr)
	}
	if !strings.Contains(got.stderr, "onnxruntime") {
		t.Errorf("stderr does not name the missing package: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "pip install -r nlp-service/requirements.txt") {
		t.Errorf("stderr does not give the install command: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "--yes") {
		t.Errorf("stderr does not say how to allow the install: %q", got.stderr)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	got := invoke(t, context.Background(), "frobnicate")

	if got.code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "Usage: lover") {
		t.Errorf("the refusal does not print the usage: %q", got.stderr)
	}
}

func TestUnknownModelSubcommandIsAUsageError(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	restore := fetchRegistry
	fetchRegistry = func(context.Context, string, string) (registry, error) {
		return registry{CacheRoot: t.TempDir(), Models: []modelEntry{{Version: "v1", Name: "v1", Files: map[string]string{"a": "b"}}}}, nil
	}
	t.Cleanup(func() { fetchRegistry = restore })

	got := invoke(t, context.Background(), "model", "frobnicate")

	if got.code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "list, add") {
		t.Errorf("the refusal does not name the choices: %q", got.stderr)
	}
}

func TestHelpExitsZeroAndPrintsTheExitCodes(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		got := invoke(t, context.Background(), args...)
		if got.code != exitOK {
			t.Errorf("%v: exit code = %d, want %d", args, got.code, exitOK)
		}
		if !strings.Contains(got.stderr, "Usage: lover") {
			t.Errorf("%v: help text was not printed: %q", args, got.stderr)
		}
	}
	// The failure classes have to be documented where a person can see them
	// before they hit one.
	got := invoke(t, context.Background(), "help")
	for _, want := range []string{"3  a dependency is missing", "4  the port", "5  the service started"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("help does not document %q: %q", want, got.stderr)
		}
	}
}

func TestExitCodesMatchTheSharedContract(t *testing.T) {
	// The three codes internal/app defines are aliased, not renumbered: a script
	// that already knows cmd/analyze must not have to learn a second table.
	if exitOK != app.ExitOK || exitRun != app.ExitFailure || exitUsage != app.ExitUsage {
		t.Fatalf("exit codes drifted from internal/app: %d/%d/%d vs %d/%d/%d",
			exitOK, exitRun, exitUsage, app.ExitOK, app.ExitFailure, app.ExitUsage)
	}
	// And the three this command owns are the numbers the plan fixes.
	if exitEnv != 3 || exitPort != 4 || exitService != 5 {
		t.Fatalf("exitEnv/exitPort/exitService = %d/%d/%d, want 3/4/5", exitEnv, exitPort, exitService)
	}
}

// TestDefaultsComeFromTheSharedLayer is hard constraint 1: lover's defaults are
// internal/app's, field for field.
func TestDefaultsComeFromTheSharedLayer(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "test", "AppData", "Local"))
	want := app.DefaultOptions()

	got, err := parseRunFlags(nil, io.Discard, false)
	if err != nil {
		t.Fatalf("parseRunFlags() error = %v", err)
	}
	if got.opts.DBPath != want.DBPath {
		t.Errorf("--db default = %q, want app.DefaultOptions().DBPath = %q", got.opts.DBPath, want.DBPath)
	}
	if got.opts.OutPath != want.OutPath {
		t.Errorf("--out default = %q, want %q", got.opts.OutPath, want.OutPath)
	}
	if got.opts.NLPURL != want.NLPURL {
		t.Errorf("--nlp-url default = %q, want %q", got.opts.NLPURL, want.NLPURL)
	}
	if got.opts.OllamaURL != want.OllamaURL {
		t.Errorf("--ollama-url default = %q, want %q", got.opts.OllamaURL, want.OllamaURL)
	}
	if got.opts.SentimentVersion != "" || got.opts.OllamaModel != "" {
		t.Errorf("the fields that must be chosen are not empty: %+v", got.opts)
	}
	if got.port != nlpPort(want.NLPURL) {
		t.Errorf("the port kept free = %q, want the one the shared URL names (%q)", got.port, nlpPort(want.NLPURL))
	}

	dash, err := parseDashFlags(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseDashFlags() error = %v", err)
	}
	if dash.dbPath != want.DBPath {
		t.Errorf("dash --db default = %q, want %q", dash.dbPath, want.DBPath)
	}
	if dash.nlpURL != want.NLPURL {
		t.Errorf("dash --nlp-url default = %q, want %q", dash.nlpURL, want.NLPURL)
	}
	if dash.addr != app.DefaultDashboardAddr() {
		t.Errorf("dash --addr default = %q, want %q", dash.addr, app.DefaultDashboardAddr())
	}
}

// TestNoSecondCopyOfADefault reads this package's own source. A duplicated
// default that happens to compute the same path today is still a duplication,
// and the only way to catch one is to look for the literals it would contain.
func TestNoSecondCopyOfADefault(t *testing.T) {
	forbidden := []string{
		"LOCALAPPDATA", "UserConfigDir", "AppData",
		"data.db", "report.html",
		"http://127.0.0.1", ":8000", ":8787", ":11434",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, literal := range forbidden {
			if strings.Contains(string(body), literal) {
				t.Errorf("%s contains %q: a default lives in internal/app/paths.go, not here", name, literal)
			}
		}
	}
}
