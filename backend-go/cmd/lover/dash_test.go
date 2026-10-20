package main

// The dashboard path: build when it must, skip when it cannot, and never let a
// missing npm become a reason the report is unavailable.

import (
	"context"
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
