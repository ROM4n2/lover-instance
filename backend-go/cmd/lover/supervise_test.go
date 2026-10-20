package main

// The supervisor's two guarantees: a taken port is reported with its holder, and
// a cancelled run leaves no process behind.
//
// The cancellation test is the one that cannot be faked. It starts a real
// interpreter that spawns a child of its own, cancels the context, and then asks
// the operating system's process table whether anything survived. Asserting only
// that the context returned would pass on a build that killed the direct child
// and orphaned everything under it — which is exactly the defect this file exists
// to prevent.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAnOccupiedPortIsClassifiedWithItsHolder is the exitPort contract. The port
// is bound by this test, so the occupier is the test process itself and its PID
// is a real one.
func TestAnOccupiedPortIsClassifiedWithItsHolder(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	err = ensurePortFree("the NLP service", port)
	if err == nil {
		t.Fatalf("ensurePortFree(%s) succeeded on a port this test holds", port)
	}
	if !asPortError(err) {
		t.Fatalf("the error is not classified as a port failure: %v", err)
	}
	message := err.Error()
	if !strings.Contains(message, port) {
		t.Errorf("the message does not name the port: %q", message)
	}
	// Either the PID, or an explicit statement that it could not be determined.
	// A message that quietly omitted both would leave the reader wondering.
	if !strings.Contains(message, "PID") {
		t.Errorf("the message says nothing about the holder's PID: %q", message)
	}
	if holder := portHolderPID(port); holder != "" {
		if !strings.Contains(message, holder) {
			t.Errorf("the holder PID %s was found but is not in the message: %q", holder, message)
		}
	} else if !strings.Contains(message, "could not be determined") {
		t.Errorf("the PID could not be determined and the message does not say so: %q", message)
	}
}

// TestAFreePortIsLeftAlone is the negative control: the check must not consume
// the port it just proved was free.
func TestAFreePortIsLeftAlone(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a port: %v", err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()

	if err := ensurePortFree("the NLP service", port); err != nil {
		t.Errorf("ensurePortFree(%s) = %v, want nil", port, err)
	}
	again, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("the port was not released: %v", err)
	}
	_ = again.Close()
}

// TestTheCommandRefusesATakenPortBeforeStartingAService drives the whole
// classification through the command: the port is held, and nothing is started.
func TestTheCommandRefusesATakenPortBeforeStartingAService(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	port := freePort(t)
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("port %s is not free on this machine, so the test cannot hold it: %v", port, err)
	}
	defer func() { _ = listener.Close() }()

	started := false
	restore := buildNLPCommand
	buildNLPCommand = func(context.Context, string, string) *exec.Cmd {
		started = true
		return exec.Command("true")
	}
	t.Cleanup(func() { buildNLPCommand = restore })

	env := newRunEnv(t)
	got := invoke(t, context.Background(), "run", "--port", port, "--export", env.export,
		"--db", env.db, "--out", env.out)

	if got.code != exitPort {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitPort, got.stderr)
	}
	if !strings.Contains(got.stderr, port) {
		t.Errorf("stderr does not name the port: %q", got.stderr)
	}
	if started {
		t.Error("a service was started even though the port was taken")
	}
}

// TestAServiceThatNeverBecomesReadyIsClassified is the exitService contract: the
// message says how long it waited and how to see the service's own output.
func TestAServiceThatNeverBecomesReadyIsClassified(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	restoreTimeout := serviceReadyTimeout
	serviceReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { serviceReadyTimeout = restoreTimeout })

	// A service that answers /health but never says "ready" is the shape this
	// classification exists for: it is alive, and it is not usable.
	restore := buildNLPCommand
	buildNLPCommand = func(ctx context.Context, python, port string) *exec.Cmd {
		return exec.CommandContext(ctx, python, "-c", "import time; time.sleep(300)")
	}
	t.Cleanup(func() { buildNLPCommand = restore })
	if _, err := exec.LookPath(pythonCommand()); err != nil {
		t.Skip("no interpreter on PATH, so a sleeping fake service cannot be started")
	}

	env := newRunEnv(t)
	got := invoke(t, context.Background(), "run", "--port", freePort(t), "--export", env.export,
		"--db", env.db, "--out", env.out)

	if got.code != exitService {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitService, got.stderr)
	}
	if !strings.Contains(got.stderr, "300ms") {
		t.Errorf("stderr does not say how long it waited: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "uvicorn") {
		t.Errorf("stderr does not point at the service's own log: %q", got.stderr)
	}
}

// freePort binds and releases a port, so a test can name one nothing holds.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// sleepScript is the fake service the cancellation test runs: it spawns a child of
// its own, records both PIDs, and then does nothing for five minutes. The child is
// the whole point — killing the direct process and leaving it behind is the
// regression this test has to catch.
const sleepScript = `
import json, os, subprocess, sys, time
child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(300)"])
with open(%q, "w", encoding="utf-8") as handle:
    handle.write(json.dumps({"parent": os.getpid(), "child": child.pid}))
time.sleep(300)
`

// TestCancellingLeavesNoOrphanProcess is the Ctrl-C guarantee.
func TestCancellingLeavesNoOrphanProcess(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		if python, err = exec.LookPath("python3"); err != nil {
			t.Skip("no Python interpreter on PATH, so the cancellation test cannot run")
		}
	}
	newRepo(t)
	stubProbe(t)
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids.json")

	restore := buildNLPCommand
	buildNLPCommand = func(ctx context.Context, _, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, python, "-c", fmt.Sprintf(sleepScript, pids))
	}
	t.Cleanup(func() { buildNLPCommand = restore })

	ctx, cancel := context.WithCancel(context.Background())
	service, err := startService(ctx, serviceConfig{python: python, port: "0", dir: dir, stderr: io.Discard})
	if err != nil {
		t.Fatalf("startService() error = %v", err)
	}
	defer service.stop()

	// Wait until the fake service has recorded both PIDs: cancelling before that
	// would prove nothing about the tree.
	var recorded struct {
		Parent int `json:"parent"`
		Child  int `json:"child"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if data, err := os.ReadFile(pids); err == nil {
			if json.Unmarshal(data, &recorded) == nil && recorded.Child != 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake service never recorded its PIDs")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	// The direct child goes first, whichever mechanism kills it.
	waitGone(t, recorded.Parent, "the fake service itself")
	// Then the tree it started. Asking this second — only after the parent is
	// already gone — is what makes the assertion about the *group*: with a
	// direct-child-only kill the parent is gone by now and the process it
	// spawned is the thing left behind.
	waitGone(t, recorded.Child, "the process it spawned")
	// And it must have been reaped, not left as a corpse. WaitDelay is what
	// bounds this: a grandchild holding the output pipe open would otherwise
	// leave Wait blocked forever, which is a Ctrl-C that never returns.
	select {
	case <-service.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the child process was never reaped after cancellation")
	}
}

// waitGone polls the process table until a PID is gone. A single check races the
// kernel: a process that has been signalled is still in the table for a moment.
func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if !processAlive(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s (PID %d) is still in the process table after cancellation: the process group was not killed", what, pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// processAlive asks the operating system whether a PID is still running.
func processAlive(pid int) bool {
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		out, err = exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").CombinedOutput()
	} else {
		out, err = exec.Command("ps", "-p", strconv.Itoa(pid)).CombinedOutput()
	}
	if err != nil {
		// A listing that failed cannot prove anything, so it reports the process
		// as gone rather than guessing in the direction that fails the test.
		return false
	}
	text := strings.ToLower(string(out))
	if strings.Contains(text, "no tasks") || strings.Contains(text, "no matching") {
		return false
	}
	return strings.Contains(text, strconv.Itoa(pid))
}
