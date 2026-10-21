package main

// The doctor: it reports and it changes nothing.
//
// The interesting assertions are negative — no package is installed, no model is
// downloaded, no service is started — and the one positive: a missing package is
// named together with the command that installs it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/app"
)

// stubLexiconProbe makes the zero-model check answer success, so a doctor test
// about something else does not need a Python interpreter or a jieba load.
func stubLexiconProbe(t *testing.T) {
	t.Helper()
	stubLexiconProbeResult(t, lexiconProbe{
		Version:      "lexicon-hownet-v1",
		OK:           true,
		Polarity:     0.618,
		NeedsWeights: false,
	}, nil)
}

// stubLexiconProbeResult hands doctor a canned probe outcome: a success, a
// failure or a timeout, the three states the reporting has to distinguish.
func stubLexiconProbeResult(t *testing.T, probe lexiconProbe, err error) {
	t.Helper()
	restore := probeLexicon
	probeLexicon = func(context.Context, string, string) (lexiconProbe, error) { return probe, err }
	t.Cleanup(func() { probeLexicon = restore })
}

// TestDoctorReportsMissingDependenciesWithoutInstalling is the exitEnv contract:
// the package name and the install command both appear, and pip is never run.
func TestDoctorReportsMissingDependenciesWithoutInstalling(t *testing.T) {
	newRepo(t)
	stubProbe(t, "onnxruntime", "tokenizers")
	stubRegistryModules(t)
	stubRegistry(t, cannedRegistry(t.TempDir()))
	stubDownload(t)
	stubLexiconProbe(t)
	installs := countInstalls(t)

	got := invoke(t, context.Background(), "doctor")

	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stdout:\n%s", got.code, exitEnv, got.stdout)
	}
	if !strings.Contains(got.stdout, "onnxruntime") || !strings.Contains(got.stdout, "tokenizers") {
		t.Errorf("stdout does not name every missing package: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "pip install -r nlp-service/requirements.txt") {
		t.Errorf("stdout does not give the install command: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "nothing was installed") {
		t.Errorf("stdout does not say that nothing was changed: %q", got.stdout)
	}
	if *installs != 0 {
		t.Errorf("pip was run %d time(s) by a command that must only report", *installs)
	}
}

// TestDoctorIsQuietWhenNothingIsMissing covers the port line too, which is the
// one thing doctor reports about the machine that a test cannot control: the
// expected line is derived by asking the same question first.
func TestDoctorIsQuietWhenNothingIsMissing(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubLexiconProbe(t)
	countInstalls(t)

	portBusy := ensurePortFree("the NLP service", nlpPort(app.DefaultNLPURL())) != nil
	got := invoke(t, context.Background(), "doctor")

	want, wantLine := exitOK, "free"
	if portBusy {
		want, wantLine = exitPort, "taken"
	}
	if got.code != want {
		t.Fatalf("exit code = %d, want %d; stdout:\n%s", got.code, want, got.stdout)
	}
	if !strings.Contains(got.stdout, "python dependencies: all") {
		t.Errorf("stdout does not report the dependencies: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "port "+nlpPort(app.DefaultNLPURL())+": "+wantLine) {
		t.Errorf("stdout does not report the port as %s: %q", wantLine, got.stdout)
	}
	if !strings.Contains(got.stdout, "installed-v1") {
		t.Errorf("stdout does not list the models: %q", got.stdout)
	}
}

// TestDoctorRefusesToGuessAtAnInterpreter keeps a machine without Python from
// producing a Python traceback in place of a message.
func TestDoctorRefusesToGuessAtAnInterpreter(t *testing.T) {
	newRepo(t)
	restore := probeDependencies
	probeDependencies = probeImports
	t.Cleanup(func() { probeDependencies = restore })

	got := invoke(t, context.Background(), "doctor", "--python", "definitely-not-python")

	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitEnv, got.stderr)
	}
	if !strings.Contains(got.stderr, "LOVER_PYTHON") {
		t.Errorf("stderr does not say how to point at another interpreter: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "definitely-not-python") {
		t.Errorf("stderr does not name the interpreter it could not find: %q", got.stderr)
	}
}

// countInstalls replaces the install with a counter.
func countInstalls(t *testing.T) *int {
	t.Helper()
	calls := 0
	restore := installDependencies
	installDependencies = func(context.Context, string, string, io.Writer) error {
		calls++
		return nil
	}
	t.Cleanup(func() { installDependencies = restore })
	return &calls
}

// TestDoctorProvesTheLexiconPathWorksWithoutWeights is the promise made
// checkable: a machine can be told, every day, that the dictionary path scores
// with no weights on disk. Success must not add a problem.
func TestDoctorProvesTheLexiconPathWorksWithoutWeights(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubLexiconProbe(t)

	portBusy := ensurePortFree("the NLP service", nlpPort(app.DefaultNLPURL())) != nil
	got := invoke(t, context.Background(), "doctor")

	want := exitOK
	if portBusy {
		want = exitPort
	}
	if got.code != want {
		t.Fatalf("exit code = %d, want %d; stdout:\n%s", got.code, want, got.stdout)
	}
	if strings.Contains(got.stdout, "lexicon path is broken") {
		t.Errorf("a working probe was reported as broken: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "works without any model weights") {
		t.Errorf("doctor does not report the zero-model path: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "version=lexicon-hownet-v1") {
		t.Errorf("doctor does not name the version the probe scored with: %q", got.stdout)
	}
}

// TestDoctorCountsABrokenLexiconPathAsAProblem: a failure on the zero-model path
// is a problem, and the message has to say no download can fix it.
func TestDoctorCountsABrokenLexiconPathAsAProblem(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubLexiconProbeResult(t, lexiconProbe{}, errors.New("dictionary data missing"))

	portBusy := ensurePortFree("the NLP service", nlpPort(app.DefaultNLPURL())) != nil
	got := invoke(t, context.Background(), "doctor")

	if !strings.Contains(got.stdout, "lexicon path is broken") {
		t.Fatalf("doctor does not report the broken lexicon path: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "zero-model path") {
		t.Errorf("the message does not say a model cannot rescue it: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "dictionary data missing") {
		t.Errorf("the message does not carry the reason: %q", got.stdout)
	}
	// The failure must be counted: with a healthy port and models, exactly one
	// problem. A busy port would add a second, so account for it.
	wantProblems := 1
	if portBusy {
		wantProblems = 2
	}
	if !strings.Contains(got.stdout, fmt.Sprintf("%d problem(s) found", wantProblems)) {
		t.Errorf("the lexicon failure was not counted as a problem: %q", got.stdout)
	}
}

// TestDoctorReportsALexiconProbeTimeout covers the third state: the subprocess
// did not answer in time. It is a problem like any failure, but the reason names
// the timeout so a wedged interpreter is distinguishable from a broken lexicon.
func TestDoctorReportsALexiconProbeTimeout(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubLexiconProbeResult(t, lexiconProbe{}, errors.New("the lexicon probe did not answer within 20s"))

	got := invoke(t, context.Background(), "doctor")

	if !strings.Contains(got.stdout, "lexicon path is broken") || !strings.Contains(got.stdout, "did not answer within") {
		t.Fatalf("a probe timeout is not reported with its reason: %q", got.stdout)
	}
	if got.code == exitOK {
		t.Errorf("exit code = %d, want non-zero for a probe timeout; stdout:\n%s", got.code, got.stdout)
	}
}

// TestDoctorDoesNotTreatAnEmptyModelCacheAsAProblem: the supported default is no
// models at all. An empty cache must not raise a problem, and doctor should say
// out loud that it is fine — the same message a person who freed disk needs.
func TestDoctorDoesNotTreatAnEmptyModelCacheAsAProblem(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir()) // no seedCache: every model is absent
	stubRegistry(t, reg)
	stubLexiconProbe(t)

	portBusy := ensurePortFree("the NLP service", nlpPort(app.DefaultNLPURL())) != nil
	got := invoke(t, context.Background(), "doctor")

	want := exitOK
	if portBusy {
		want = exitPort
	}
	if got.code != want {
		t.Fatalf("exit code = %d, want %d; an empty model cache must not be a problem; stdout:\n%s", got.code, want, got.stdout)
	}
	if !strings.Contains(got.stdout, "no models installed: that is the supported default, not a problem") {
		t.Errorf("doctor does not explain that an empty cache is the default: %q", got.stdout)
	}
}

// TestLexiconProbeTimeoutIsNotZero guards the one number that would silently
// break the check: a zero deadline cancels the child before it starts.
func TestLexiconProbeTimeoutIsNotZero(t *testing.T) {
	if lexiconProbeTimeout <= 0 {
		t.Fatalf("lexiconProbeTimeout = %s; a zero deadline cancels the probe before it runs", lexiconProbeTimeout)
	}
}
