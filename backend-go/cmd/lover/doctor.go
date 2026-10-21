package main

// The dependency probe, and the doctor that reports what it found.
//
// "Is Python installed and are the six packages importable" is the first
// question a first run has to answer, and the answer is a list of names rather
// than a boolean: "the environment is broken" is not actionable, "onnxruntime is
// missing, install it with this command" is.
//
// The probe imports each module in one Python process and reports them
// individually, because a single `python -c "import a, b, c"` stops at the first
// failure and a machine missing three packages would then be told about one.
//
// Nothing is installed here. Installing is an explicit, announced step that
// `lover` performs only when --yes was given, and doctor never performs at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"lover-instance/backend-go/internal/app"
)

// dependencyModules is the import list a run of the pipeline needs: the service
// itself (fastapi, uvicorn), the tokenizer (jieba, tokenizers), the runtime
// (onnxruntime) and the array library both of them sit on (numpy). The list is
// the same one requirements.txt pins, and it is spelled here rather than read
// from that file because a probe that could not run without a working pip is a
// probe that cannot report the problem.
var dependencyModules = []string{"fastapi", "uvicorn", "jieba", "onnxruntime", "tokenizers", "numpy"}

// registryModules is the much shorter list `lover model` needs. Importing the
// registry pulls in numpy and nothing heavier — onnxruntime and tokenizers are
// imported inside Encoder.from_model_dir — so a menu must not refuse to print
// because of a model this machine was never asked to load.
var registryModules = []string{"numpy"}

// lexiconProbeTimeout bounds the zero-model probe. The first call into the
// dictionary path builds jieba's cache, which is a second or two on a cold disk;
// 20s is generous for that and still short enough that a wedged interpreter is
// reported rather than waited on. It is a variable, but never zero: a zero
// deadline would cancel the child before it started and turn a working path into
// a permanent problem.
var lexiconProbeTimeout = 20 * time.Second

// installHint is the one command that installs everything a run needs. It names
// the pinned file rather than a list of packages, so the versions the service
// was verified against are the versions that get installed.
func installHint(python string) string {
	return python + " -m pip install -r nlp-service/requirements.txt"
}

// probeDependencies is the seam a test replaces to hand the command a missing
// package without uninstalling anything.
var probeDependencies = probeImports

// probeScript asks Python to import each module and report them one per line.
// The module list arrives as JSON, which is also a Python list literal, so
// nothing has to be escaped twice.
const probeScript = `
import importlib, json
for name in json.loads(%q):
    try:
        importlib.import_module(name)
        print(name + " ok")
    except Exception as exc:
        print(name + " missing " + type(exc).__name__)
`

// probeImports runs the probe in dir and returns the modules that could not be
// imported. An empty slice means the environment is complete.
func probeImports(ctx context.Context, python, dir string, modules []string) ([]string, error) {
	if _, err := exec.LookPath(python); err != nil {
		return nil, fmt.Errorf("%s was not found on PATH (%v); install Python 3.11 or newer, or point LOVER_PYTHON at the interpreter to use", python, err)
	}
	names, err := json.Marshal(modules)
	if err != nil {
		return nil, fmt.Errorf("encode the module list: %w", err)
	}
	cmd := exec.CommandContext(ctx, python, "-c", fmt.Sprintf(probeScript, string(names)))
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("could not check the Python dependencies with %s (%v): %s", python, err, strings.TrimSpace(stderr.String()))
	}

	var missing []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		seen[fields[0]] = true
		if fields[1] != "ok" {
			missing = append(missing, fields[0])
		}
	}
	// A module the probe never mentioned is a module that was never asked about:
	// the script is the thing that changed, and reporting it as missing is the
	// honest answer.
	for _, name := range modules {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// installDependencies runs the pinned install, streaming its output so a slow
// download is visible rather than looking like a hang. It is a variable so a test
// can count the installs without running pip.
var installDependencies = runPinnedInstall

func runPinnedInstall(ctx context.Context, python, root string, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, python, "-m", "pip", "install", "-r", filepath.Join("nlp-service", "requirements.txt"))
	cmd.Dir = root
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the install failed (%v); run it yourself to see the full output: %s", err, installHint(python))
	}
	return nil
}

// doctorOptions is what `lover doctor` parses: which interpreter to ask about
// and nothing else. Doctor reports, so it has no flag that changes anything.
type doctorOptions struct {
	python string
	port   string
}

func parseDoctorFlags(args []string, stderr io.Writer) (doctorOptions, error) {
	cfg := doctorOptions{python: pythonCommand()}
	fs := flag.NewFlagSet("lover doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: lover doctor [options]

Reports what this machine is missing before a first run: the Python
dependencies, the port the NLP service needs, and which model versions are
installed. It changes nothing: no package is installed, no model is downloaded,
no service is started.

Flags:
`)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.python, "python", cfg.python, "interpreter used to probe the Python dependencies")
	if err := fs.Parse(args); err != nil {
		return doctorOptions{}, flagParseError{err: err}
	}
	if fs.NArg() > 0 {
		return doctorOptions{}, app.Usagef("unexpected argument %q; every option is a flag", fs.Arg(0))
	}
	cfg.port = nlpPort(app.DefaultNLPURL())
	if cfg.port == "" {
		return doctorOptions{}, app.Usagef("the NLP service URL %q does not name a port, so the port cannot be checked", app.DefaultNLPURL())
	}
	return cfg, nil
}

// runDoctor is the diagnostic: it reports and exits, and it is the one
// subcommand a person can run without any fear of side effects.
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parseDoctorFlags(args, stderr)
	if err != nil {
		return classifyParseError(err, stderr)
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	serviceDir := filepath.Join(root, "nlp-service")
	problems := 0

	missing, err := probeDependencies(ctx, cfg.python, serviceDir, dependencyModules)
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	if len(missing) == 0 {
		fmt.Fprintf(stdout, "python dependencies: all %d importable\n", len(dependencyModules))
	} else {
		problems++
		fmt.Fprintf(stdout, "python dependencies: missing %s\n", strings.Join(missing, ", "))
		fmt.Fprintf(stdout, "  install with: %s\n", installHint(cfg.python))
	}

	if err := ensurePortFree("the NLP service", cfg.port); err != nil {
		problems++
		fmt.Fprintf(stdout, "port %s: taken — %v\n", cfg.port, err)
	} else {
		fmt.Fprintf(stdout, "port %s: free\n", cfg.port)
	}

	if err := probeRegistryModules(ctx, cfg.python, serviceDir); err != nil {
		problems++
		fmt.Fprintf(stdout, "models: %v\n", err)
	} else if reg, err := fetchRegistry(ctx, cfg.python, serviceDir); err != nil {
		problems++
		fmt.Fprintf(stdout, "models: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "models: cache at %s\n", reg.CacheRoot)
		anyFiles := false
		for _, status := range modelStatuses(reg, reg.CacheRoot) {
			fmt.Fprintf(stdout, "  %-28s %s%s\n", status.entry.Version, status.state(), status.detail())
			if status.anyFilePresent() {
				anyFiles = true
			}
		}
		if !anyFiles {
			// An empty cache is the shipping default, not a failure. Saying so is
			// the point: a person who deleted the directory to reclaim disk should
			// not read a bare model listing as "something is missing".
			fmt.Fprintf(stdout, "no models installed: that is the supported default, not a problem\n")
		}
	}

	// The zero-model check: prove the dictionary path scores with no weights on
	// disk. This deliberately runs even when the model block above reported an
	// empty cache — that is the situation it exists for. A failure is a real
	// problem, and one no download can rescue, so the message says exactly that.
	if probe, err := probeLexicon(ctx, cfg.python, serviceDir); err != nil {
		problems++
		fmt.Fprintf(stdout, "lexicon path is broken: %v; this is the zero-model path, so no model can rescue it\n", err)
	} else {
		fmt.Fprintf(stdout, "lexicon path: works without any model weights (version=%s, sample polarity=%+.3f)\n", probe.Version, probe.Polarity)
	}

	if problems == 0 {
		fmt.Fprintf(stdout, "doctor: nothing missing; run `lover` to analyze the newest export\n")
		return exitOK
	}
	fmt.Fprintf(stdout, "doctor: %d problem(s) found; nothing was installed, downloaded or started\n", problems)
	if len(missing) > 0 {
		return exitEnv
	}
	return exitPort
}

// probeRegistryModules checks the smaller import list the model commands need, so
// a doctor on a machine without numpy says so instead of failing inside the
// registry read with a Python traceback. It is a variable because a test has to
// answer the question without an interpreter.
var probeRegistryModules = probeRegistryImports

func probeRegistryImports(ctx context.Context, python, serviceDir string) error {
	missing, err := probeImports(ctx, python, serviceDir, registryModules)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot read the model registry: %s is missing (%s)", strings.Join(missing, ", "), installHint(python))
	}
	return nil
}

// lexiconProbe is what `python -m app.model_cli lexicon-probe` prints: one line
// of JSON. On success `ok` is true and `polarity` is the sample sentence's
// score; on failure `ok` is false and `error` carries a short reason.
type lexiconProbe struct {
	Version      string  `json:"version"`
	OK           bool    `json:"ok"`
	Polarity     float64 `json:"polarity"`
	NeedsWeights bool    `json:"needs_weights"`
	Error        string  `json:"error"`
}

// probeLexicon is the seam a test replaces with a canned success, failure and
// timeout, so doctor's reporting is checked without an interpreter or a jieba
// load.
var probeLexicon = defaultProbeLexicon

// defaultProbeLexicon runs the service's own probe in a bounded subprocess. It
// is read-only: the subcommand scores a sentence the Python side wrote itself
// and downloads nothing, so this can run with an empty cache — which is the case
// it exists to prove still works.
func defaultProbeLexicon(ctx context.Context, python, serviceDir string) (lexiconProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, lexiconProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-m", "app.model_cli", "lexicon-probe")
	cmd.Dir = serviceDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return lexiconProbe{}, fmt.Errorf("the lexicon probe did not answer within %s", lexiconProbeTimeout)
		}
		return lexiconProbe{}, fmt.Errorf("could not run the lexicon probe with %s (%v): %s", python, err, strings.TrimSpace(stderr.String()))
	}
	var probe lexiconProbe
	if err := json.Unmarshal(out, &probe); err != nil {
		return lexiconProbe{}, fmt.Errorf("the lexicon probe output is not JSON (%v)", err)
	}
	if !probe.OK {
		reason := probe.Error
		if reason == "" {
			reason = "no reason given"
		}
		return lexiconProbe{}, errors.New(reason)
	}
	return probe, nil
}
