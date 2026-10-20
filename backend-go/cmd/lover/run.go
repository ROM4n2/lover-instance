package main

// `lover` and `lover sync`: the run itself.
//
// Both go through the same shared orchestration — internal/app's Run, the same
// one cmd/analyze calls — and they differ in two things only: sync prints the
// weekly-update line instead of a plain "report written", and sync is the command
// a person runs every week while `lover` is the one they run once. There is no
// second import path, no second scoring path and no second renderer here; a
// wrapper that grew one would drift from the CLI that has been verified since M1.
//
// The order of the steps is the order a failure becomes legible in:
//
//  1. are the Python dependencies importable? (exitEnv)
//  2. is the port free? (exitPort)
//  3. did the service become healthy? (exitService)
//  4. did the analysis run? (exitRun, or app's own usage code)
//
// Each step's message names what to do about it, and none of them ever quotes a
// message, a talker or a conversation key.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"lover-instance/backend-go/internal/app"
)

// runOptions is the parsed `lover`/`lover sync` command line.
type runOptions struct {
	// opts is handed to app.Run untouched. Every default in it came from
	// app.DefaultOptions, which reads internal/app/paths.go — the one place a
	// default lives.
	opts app.Options

	// external records that --nlp-url was given, which means a service is
	// already running and this command must not start a second one on the same
	// port.
	external bool

	yes    bool
	python string
	port   string

	// portFlag is the port the service this command starts should bind. It is
	// empty unless --port was given, and it is applied to the shared URL rather
	// than to a URL of this package's own: the host half stays whatever
	// internal/app says it is.
	portFlag  string
	portGiven bool
}

// parseRunFlags fills runOptions from app.DefaultOptions, so no default in this
// package can disagree with the one cmd/analyze uses. The test that pins that is
// TestDefaultsComeFromTheSharedLayer, and it fails the moment a literal appears
// here.
func parseRunFlags(args []string, stderr io.Writer, sync bool) (runOptions, error) {
	name := "lover"
	if sync {
		name = "lover sync"
	}
	cfg := runOptions{opts: app.DefaultOptions(), python: pythonCommand()}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		what := "Runs the whole pipeline once: checks the Python dependencies, starts the NLP service, analyzes the newest export and opens the report."
		out := "the report"
		if sync {
			what = "Runs the same pipeline as `lover`, reporting only what is new: what was imported, what was scored, and how much of the conversation is covered."
			out = "one line: 新增 N 条 / 打分 M 条 / 覆盖率 x/y"
		}
		fmt.Fprintf(stderr, `Usage: %s [options]

%s

The export defaults to the newest *.json in the exports directory (%s); pass
--export to name one. No WeChat decryption happens here: %s.

Flags:
`, name, what, app.ExportsDir(), app.ExportJSONHint)
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nThe output is %s.\n", out)
	}

	fs.StringVar(&cfg.opts.ExportPath, "export", "", "path to the chatlog-keeper export JSON; defaults to the newest one in the exports directory")
	fs.StringVar(&cfg.opts.Talker, "talker", "", "conversation key to analyze; required when the export holds more than one")
	fs.StringVar(&cfg.opts.DBPath, "db", cfg.opts.DBPath, "path to the SQLite database")
	fs.StringVar(&cfg.opts.OutPath, "out", cfg.opts.OutPath, "path of the HTML report to write")
	fs.StringVar(&cfg.opts.NLPURL, "nlp-url", cfg.opts.NLPURL, "base URL of the NLP service; passing it means a service is already running and this command will not start one")
	fs.StringVar(&cfg.opts.SentimentVersion, "sentiment-version", "", "sentiment model version to score with; empty uses the version the service reports as its default")
	fs.BoolVar(&cfg.opts.SkipSentiment, "skip-sentiment", false, "rebuild behavioral metrics only; the report will show sentiment coverage as incomplete")
	fs.StringVar(&cfg.opts.OllamaURL, "ollama-url", cfg.opts.OllamaURL, "base URL of the local OpenAI-compatible model; only a loopback address is accepted")
	fs.StringVar(&cfg.opts.OllamaModel, "ollama-model", "", "local model name used to write the narration; empty means no narration")
	fs.BoolVar(&cfg.opts.NoNarrative, "no-narrative", false, "skip the narration section even when --ollama-model is set")
	fs.BoolVar(&cfg.yes, "yes", false, "allow this command to install missing Python dependencies; without it a missing dependency is reported, never installed")
	fs.StringVar(&cfg.python, "python", cfg.python, "interpreter used to start the NLP service")
	fs.StringVar(&cfg.portFlag, "port", "", "port the NLP service this command starts should bind; defaults to the one --nlp-url names")

	if err := fs.Parse(args); err != nil {
		return runOptions{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return runOptions{}, app.Usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "nlp-url":
			cfg.external = true
		case "port":
			cfg.portGiven = true
		}
	})
	if cfg.portGiven && cfg.external {
		return runOptions{}, app.Usagef("--port is for the service this command starts; --nlp-url already names one that is running")
	}
	cfg.port = nlpPort(cfg.opts.NLPURL)
	if cfg.port == "" {
		return runOptions{}, app.Usagef("--nlp-url %q does not name a port, so this command cannot tell which port to keep free", cfg.opts.NLPURL)
	}
	if cfg.portGiven {
		// The port is applied to the shared URL, so the host half — loopback, and
		// nothing else — stays the one internal/app chose.
		redirected, err := withPort(cfg.opts.NLPURL, cfg.portFlag)
		if err != nil {
			return runOptions{}, app.Usagef("--port %s is not a port number: %v", cfg.portFlag, err)
		}
		cfg.opts.NLPURL = redirected
		cfg.port = cfg.portFlag
	}
	return cfg, nil
}

// withPort returns baseURL with its port replaced. It exists so --port moves one
// field of the shared default instead of introducing a second default.
func withPort(baseURL, port string) (string, error) {
	number, err := strconv.Atoi(port)
	if err != nil {
		return "", fmt.Errorf("%q is not a number", port)
	}
	if number < 1 || number > 65535 {
		return "", fmt.Errorf("%d is outside the port range 1-65535", number)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Host = net.JoinHostPort(parsed.Hostname(), port)
	return parsed.String(), nil
}

// runFirstRun is the command a person types with nothing after it.
func runFirstRun(ctx context.Context, args []string, stdout, stderr io.Writer, sync bool) int {
	cfg, err := parseRunFlags(args, stderr, sync)
	if err != nil {
		return classifyParseError(err, stderr)
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	serviceDir := filepath.Join(root, "nlp-service")

	// 1. The environment. A missing package is reported with the command that
	// installs it, and installing needs --yes: a tool that silently pip-installs
	// into a machine it was pointed at is not a tool anybody should double-click.
	missing, err := probeDependencies(ctx, cfg.python, serviceDir, dependencyModules)
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	if len(missing) > 0 {
		if !cfg.yes {
			fmt.Fprintf(stderr, "lover: the Python dependencies are missing: %s\n", strings.Join(missing, ", "))
			fmt.Fprintf(stderr, "lover: install them with: %s\n", installHint(cfg.python))
			fmt.Fprintf(stderr, "lover: re-run with --yes to let this command run that install; nothing has been changed\n")
			return exitEnv
		}
		fmt.Fprintf(stderr, "lover: installing the Python dependencies: %s\n", installHint(cfg.python))
		if err := installDependencies(ctx, cfg.python, root, stderr); err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitEnv
		}
		if missing, err = probeDependencies(ctx, cfg.python, serviceDir, dependencyModules); err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitEnv
		}
		if len(missing) > 0 {
			fmt.Fprintf(stderr, "lover: still missing after the install: %s\n", strings.Join(missing, ", "))
			return exitEnv
		}
	}

	// 2. The service. Either this command starts one, or --nlp-url said somebody
	//    else already did.
	var service *child
	if !cfg.external {
		if err := ensurePortFree("the NLP service", cfg.port); err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			if asPortError(err) {
				return exitPort
			}
			return exitEnv
		}
		service, err = startService(ctx, serviceConfig{python: cfg.python, port: cfg.port, dir: serviceDir, stderr: stderr})
		if err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitEnv
		}
		defer service.stop()
		fmt.Fprintf(stderr, "lover: started the NLP service on port %s\n", cfg.port)
		if err := service.waitReady(ctx, cfg.opts.NLPURL, serviceReadyTimeout); err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			if ctx.Err() != nil {
				return interrupted(stderr)
			}
			return exitService
		}
	}

	// 3. The export. Without one there is nothing to analyze, and the hint is the
	//    shared one so both entry points say the same sentence.
	if cfg.opts.ExportPath == "" {
		path, err := latestExport(app.ExportsDir())
		if err != nil {
			fmt.Fprintf(stderr, "lover: %v\n", err)
			return exitRun
		}
		cfg.opts.ExportPath = path
	}

	// 4. The analysis, in the shared layer.
	summary, code, err := app.Run(ctx, cfg.opts, stdout, stderr)
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(stderr)
		}
		fmt.Fprintf(stderr, "lover: %v\n", err)
		// app.Run classifies its own failures: a usage error (an export holding
		// two conversations, say) is exitUsage and a run that failed is exitRun.
		// Both are the shared codes, so the code it returned is the right one.
		return code
	}

	if sync {
		// Absolute coverage, not this run's count. "打分 0 条" beside "覆盖率
		// 32400/32624" is the honest pair: the second number is what the report
		// will show, and a weekly update that printed only the first would look
		// like a regression every single week.
		fmt.Fprintf(stdout, "新增 %d 条 / 打分 %d 条 / 覆盖率 %d/%d\n",
			summary.Inserted, summary.Scored, summary.Coverage, summary.Total)
	} else {
		fmt.Fprintf(stdout, "lover: report written to %s\n", summary.ReportPath)
	}
	openReport(summary.ReportPath, stderr)
	return exitOK
}

// interrupted is what a Ctrl-C looks like from this command's side. The service
// has already been collected by the deferred stop (or is about to be), and the
// job row was closed by app.Run on a context that ignores cancellation, so there
// is nothing to clean up and nothing to report beyond the fact.
func interrupted(stderr io.Writer) int {
	fmt.Fprintf(stderr, "lover: 已中断：服务已收掉，job 行已按取消关闭；重跑同一条命令即可接着跑\n")
	return exitRun
}

// openReport hands the finished report to the system's default opener.
//
// A machine with no opener — a headless box, a minimal container — is not a
// failure: the path is printed and the run still succeeded, because the report is
// the artifact and the browser is a convenience.
var openReport = func(path string, stderr io.Writer) {
	var cmd = openCommand(path)
	if cmd == nil {
		fmt.Fprintf(stderr, "lover: the report is at %s\n", path)
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "lover: could not open the report (%v); it is at %s\n", err, path)
		return
	}
	fmt.Fprintf(stderr, "lover: opened %s\n", path)
}

// openCommand builds the platform's opener, or nil where there is none.
func openCommand(path string) *exec.Cmd {
	switch runtime.GOOS {
	case "windows":
		// `start` is a cmd builtin, and the empty title is what stops a quoted
		// path from being read as the window title.
		return exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		return exec.Command("open", path)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return nil
		}
		return exec.Command("xdg-open", path)
	}
}

// latestExport picks the newest *.json in dir. The exports directory is where an
// external exporter leaves its output, so "the newest one" is the one a person
// just produced, and refusing when the directory is empty says what to produce.
func latestExport(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no export found in %s: %s", dir, app.ExportJSONHint)
	}
	newest := ""
	var newestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestTime) {
			newestTime = info.ModTime()
			newest = filepath.Join(dir, entry.Name())
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no export found in %s: %s", dir, app.ExportJSONHint)
	}
	return newest, nil
}
