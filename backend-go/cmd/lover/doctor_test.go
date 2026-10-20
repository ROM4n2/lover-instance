package main

// The doctor: it reports and it changes nothing.
//
// The interesting assertions are negative — no package is installed, no model is
// downloaded, no service is started — and the one positive: a missing package is
// named together with the command that installs it.

import (
	"context"
	"io"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/app"
)

// TestDoctorReportsMissingDependenciesWithoutInstalling is the exitEnv contract:
// the package name and the install command both appear, and pip is never run.
func TestDoctorReportsMissingDependenciesWithoutInstalling(t *testing.T) {
	newRepo(t)
	stubProbe(t, "onnxruntime", "tokenizers")
	stubRegistryModules(t)
	stubRegistry(t, cannedRegistry(t.TempDir()))
	stubDownload(t)
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
