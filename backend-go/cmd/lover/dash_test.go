package main

// The dashboard path: build when it must, skip when it cannot, and never let a
// missing npm become a reason the report is unavailable.

import (
	"context"
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

// freeAddr picks a port this test can bind, so the dashboard's own port check is
// deterministic on a machine where something else may hold 8787.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return "127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// stubServer replaces the build-and-start step with a process that exits at once,
// so a test about the build decision finishes in milliseconds.
func stubServer(t *testing.T) *int {
	t.Helper()
	starts := 0
	restore := startDashboardServer
	startDashboardServer = func(ctx context.Context, cfg dashOptions, root string, stderr io.Writer) (*child, error) {
		starts++
		cmd := exec.CommandContext(ctx, "cmd", "/c", "exit", "0")
		if runtime.GOOS != "windows" {
			cmd = exec.CommandContext(ctx, "true")
		}
		return newChild(ctx, cmd, root, stderr)
	}
	t.Cleanup(func() { startDashboardServer = restore })
	return &starts
}

// TestDashSkipsWhenNpmIsMissing is the D1 escape hatch: the dashboard is optional
// and the report is not, so the message says which is which and the exit code is
// the environment one.
func TestDashSkipsWhenNpmIsMissing(t *testing.T) {
	root := newRepo(t) // no dist in it
	restore := npmAvailable
	npmAvailable = func() bool { return false }
	t.Cleanup(func() { npmAvailable = restore })
	builds := countBuilds(t)
	starts := stubServer(t)

	got := invoke(t, context.Background(), "dash", "--addr", freeAddr(t))

	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitEnv, got.stderr)
	}
	if !strings.Contains(got.stderr, "Dashboard 跳过") {
		t.Errorf("stderr does not say the dashboard was skipped: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "报告路径不受影响") {
		t.Errorf("stderr does not say the report path is unaffected: %q", got.stderr)
	}
	if *builds != 0 {
		t.Errorf("npm run build ran %d time(s) with npm reported missing", *builds)
	}
	if *starts != 0 {
		t.Errorf("the server was started %d time(s), want 0: a skipped dashboard must not start one", *starts)
	}
	if _, err := os.Stat(distIndex(root)); !os.IsNotExist(err) {
		t.Error("a bundle appeared although npm was reported missing")
	}
}

// TestDashBuildsWhenTheBundleIsStale is the other half: a stale bundle is
// rebuilt, and only then is the server started.
func TestDashBuildsWhenTheBundleIsStale(t *testing.T) {
	root := newRepo(t)
	stale := bundleIsStale(t, root)
	if !stale {
		t.Fatal("a repository with no dist should report the bundle as stale")
	}
	restore := npmAvailable
	npmAvailable = func() bool { return true }
	t.Cleanup(func() { npmAvailable = restore })
	builds := countBuilds(t)
	starts := stubServer(t)

	got := invoke(t, context.Background(), "dash", "--addr", freeAddr(t))

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if *builds != 1 {
		t.Errorf("npm run build ran %d time(s), want 1", *builds)
	}
	if *starts != 1 {
		t.Errorf("the server was started %d time(s), want 1", *starts)
	}
	if !strings.Contains(got.stdout, "dashboard on http://") {
		t.Errorf("stdout does not name the dashboard: %q", got.stdout)
	}
}

// TestDashDoesNotRebuildAFreshBundle keeps a second `lover dash` from paying for
// a build it does not need.
func TestDashDoesNotRebuildAFreshBundle(t *testing.T) {
	root := newRepo(t)
	writeDist(t, root)
	if stale := bundleIsStale(t, root); stale {
		t.Fatal("a bundle newer than every source should not be stale")
	}
	restore := npmAvailable
	npmAvailable = func() bool { return true }
	t.Cleanup(func() { npmAvailable = restore })
	builds := countBuilds(t)
	stubServer(t)

	got := invoke(t, context.Background(), "dash", "--addr", freeAddr(t))

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if *builds != 0 {
		t.Errorf("npm run build ran %d time(s) on a fresh bundle", *builds)
	}
}

// TestDashSkipsWhenTheBuildFails is the other way npm can be unavailable: it is
// there and it fails.
func TestDashSkipsWhenTheBuildFails(t *testing.T) {
	newRepo(t)
	restore := npmAvailable
	npmAvailable = func() bool { return true }
	t.Cleanup(func() { npmAvailable = restore })
	restoreBuild := runNPMBuild
	runNPMBuild = func(context.Context, string, io.Writer) error {
		return errBuildFailed
	}
	t.Cleanup(func() { runNPMBuild = restoreBuild })
	starts := stubServer(t)

	got := invoke(t, context.Background(), "dash", "--addr", freeAddr(t))

	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitEnv, got.stderr)
	}
	if !strings.Contains(got.stderr, "Dashboard 跳过") {
		t.Errorf("stderr does not say the dashboard was skipped: %q", got.stderr)
	}
	if *starts != 0 {
		t.Errorf("the server was started %d time(s), want 0", *starts)
	}
}

// TestDashRefusesATakenPort reuses the port classification: the dashboard's own
// port is not special.
func TestDashRefusesATakenPort(t *testing.T) {
	root := newRepo(t)
	writeDist(t, root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	addr := "127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	got := invoke(t, context.Background(), "dash", "--addr", addr)

	if got.code != exitPort {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitPort, got.stderr)
	}
	if !strings.Contains(got.stderr, addr[strings.LastIndex(addr, ":")+1:]) {
		t.Errorf("stderr does not name the port: %q", got.stderr)
	}
}

// TestDashRemovesTheServerBinaryItBuilt is the cleanup the temporary directory
// is owed: `lover dash` compiles cmd/server into %TEMP%, and that file is this
// command's to delete, not the person's.
//
// The file is a real one and the assertion is about the filesystem rather than
// about a counter, because a cleanup that is registered but never reached
// leaves the file behind and only os.Stat can tell the difference. What is
// faked is which process gets launched — a real build costs a compile of
// cmd/server and cannot be made to exit on a timer — and the identity of the
// process has no bearing on what happens to the file.
func TestDashRemovesTheServerBinaryItBuilt(t *testing.T) {
	root := newRepo(t)
	writeDist(t, root) // a fresh bundle, so the build question is out of the way
	restore := npmAvailable
	npmAvailable = func() bool { return true }
	t.Cleanup(func() { npmAvailable = restore })

	binary := fakeBuild(t, exitsAtOnceCommand)

	got := invoke(t, context.Background(), "dash", "--addr", freeAddr(t))

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	// The dashboard line is printed only after startDashboardServer returned a
	// started child. Without it, the file below could have been deleted by the
	// path that covers a failed start, and this test would assert nothing.
	if !strings.Contains(got.stdout, "dashboard on http://") {
		t.Fatalf("no server was started, so the removal proves nothing: %q / %q", got.stdout, got.stderr)
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Errorf("the temporary server binary survived the run (stat = %v): %s", err, binary)
	}
}

// TestDashRemovesTheServerBinaryWhenCancelled is the other way out of the wait:
// Ctrl-C. The process is killed and reaped by stop, and the cleanup registered
// behind it still has to run — a cancellation is the path a person takes most
// often and the one a deferred cleanup is easiest to lose.
func TestDashRemovesTheServerBinaryWhenCancelled(t *testing.T) {
	root := newRepo(t)
	writeDist(t, root)
	restore := npmAvailable
	npmAvailable = func() bool { return true }
	t.Cleanup(func() { npmAvailable = restore })

	marker := filepath.Join(t.TempDir(), "server-started")
	binary := fakeBuild(t, func(ctx context.Context) *exec.Cmd { return sleepCommand(ctx, marker) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan invocation, 1)
	go func() { finished <- invoke(t, ctx, "dash", "--addr", freeAddr(t)) }()

	// Cancelled only once the child is genuinely running: a cancellation that
	// arrived before the process existed would prove nothing about a cleanup
	// that is defined to run after the wait.
	if err := waitForMarker(marker, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("the fake server never started (%v); stderr:\n%s", err, (<-finished).stderr)
	}
	cancel()

	got := <-finished
	// Which branch of the select the run took is decided by the kernel: the
	// cancellation and the death it caused become ready at the same moment.
	// Both are a run that ended, and the subject of this test is the file.
	if got.code != exitOK && got.code != exitRun {
		t.Fatalf("exit code = %d, want %d or %d; stderr:\n%s", got.code, exitOK, exitRun, got.stderr)
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Errorf("the temporary server binary survived a cancellation (stat = %v): %s", err, binary)
	}
}

// fakeBuild replaces the two seams startDashboardServer uses, so the removal of
// the temporary binary can be tested without a toolchain: buildServer writes a
// real file and names it, and serverCommand launches run instead of that file.
// The file is never executed, which is what keeps this fast and portable.
func fakeBuild(t *testing.T, run func(ctx context.Context) *exec.Cmd) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "lover-server-test")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	if err := os.WriteFile(binary, []byte("never launched: the command is faked"), 0o600); err != nil {
		t.Fatalf("write the fake temporary binary: %v", err)
	}
	restoreBuild := buildServer
	buildServer = func(context.Context, string, io.Writer) (string, error) { return binary, nil }
	t.Cleanup(func() { buildServer = restoreBuild })

	restoreCommand := serverCommand
	serverCommand = func(ctx context.Context, _ string, _ dashOptions, _ string) *exec.Cmd { return run(ctx) }
	t.Cleanup(func() { serverCommand = restoreCommand })
	return binary
}

// exitsAtOnceCommand is the server as a process that finishes immediately.
func exitsAtOnceCommand(ctx context.Context) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", "exit", "0")
	}
	return exec.CommandContext(ctx, "true")
}

// sleepCommand is the server as a process that outlives the command's own
// start: it writes a marker, so a test can know it is running before it
// cancels, and then does nothing for long enough that only the cancellation
// ends it.
//
// The Windows form passes every word separately rather than as one quoted
// command line: os/exec escapes the arguments it was given, and a single
// argument full of quotes is a command line cmd.exe then parses differently.
func sleepCommand(ctx context.Context, marker string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", "echo", "started", ">", marker, "&&", "ping", "-n", "60", "127.0.0.1", ">", "nul")
	}
	return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(`echo started > "%s"; sleep 60`, marker))
}

// waitForMarker waits until the child has written its marker, which is the
// earliest moment a cancellation can say anything about the cleanup that
// follows the wait. It polls the filesystem rather than the child's fields,
// because the child is started by another goroutine.
func waitForMarker(marker string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the fake server never wrote %s", marker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func countBuilds(t *testing.T) *int {
	t.Helper()
	builds := 0
	restore := runNPMBuild
	runNPMBuild = func(context.Context, string, io.Writer) error {
		builds++
		return nil
	}
	t.Cleanup(func() { runNPMBuild = restore })
	return &builds
}

// writeDist puts a bundle newer than every source in the repository.
func writeDist(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "backend-go", "internal", "web", "dist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	index := filepath.Join(dir, "index.html")
	if err := os.WriteFile(index, []byte("<html></html>"), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	stamp := time.Now().Add(time.Hour)
	if err := os.Chtimes(index, stamp, stamp); err != nil {
		t.Fatalf("stamp index.html: %v", err)
	}
}

// bundleIsStale is a thin wrapper so a test does not repeat the error check.
func bundleIsStale(t *testing.T, root string) bool {
	t.Helper()
	stale, err := distStale(root)
	if err != nil {
		t.Fatalf("distStale() error = %v", err)
	}
	return stale
}

var errBuildFailed = &buildError{}

type buildError struct{}

func (e *buildError) Error() string { return "npm run build failed" }
