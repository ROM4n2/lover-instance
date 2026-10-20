package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/app"
)

// The default this command binds is internal/app's, not its own.
//
// TestParse_DefaultsComeFromTheSharedLayer compares values and
// TestServerKeepsNoCopyOfADefault reads this package's own source. The two
// together are what make the rule checkable: a hardcoded "127.0.0.1:8787"
// computes the same value today and so passes the first, and only the second
// one — which looks for the literal — turns red. That is the difference between
// "the two agree" and "there is only one".

// TestParse_DefaultAddrBindsLoopbackOnly is the command-line half of "no remote
// access".
//
// The API answers questions about a plaintext chat log and has no login, so the
// interface it binds is the access control. ":8787" and "0.0.0.0:8787" both mean
// every interface — on a laptop on a shared network that is the room reading the
// database — so the default is asserted as loopback by address, not by string
// equality alone: a future edit to a different loopback spelling (127.0.0.2,
// ::1) still passes, and any wildcard host fails.
func TestParse_DefaultAddrBindsLoopbackOnly(t *testing.T) {
	cfg, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatalf("parse(nil) error: %v", err)
	}
	if cfg.addr != app.DefaultDashboardAddr() {
		t.Errorf("addr = %q, want %q", cfg.addr, app.DefaultDashboardAddr())
	}
	if cfg.addr != "127.0.0.1:8787" {
		t.Errorf("addr = %q, want 127.0.0.1:8787", cfg.addr)
	}
	host, port, err := net.SplitHostPort(cfg.addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error: %v", cfg.addr, err)
	}
	if port != "8787" {
		t.Errorf("port = %q, want 8787", port)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Errorf("host = %q, want a loopback address: this server has no login and must not answer the network", host)
	}
}

// TestParse_RejectsAnEmptyAddr keeps "listen nowhere" from becoming "listen
// everywhere": an empty --addr is a typo, not a request for the default.
func TestParse_RejectsAnEmptyAddr(t *testing.T) {
	_, err := parse([]string{"--addr", ""}, io.Discard)
	if err == nil {
		t.Fatal("parse(--addr \"\") = nil error, want a usage error")
	}
	var usage usageError
	if !errors.As(err, &usage) {
		t.Errorf("error = %v (%T), want a usageError so the exit code is 2", err, err)
	}
}

// TestParse_DefaultsMatchTheCLIDatabase pins the one piece of the two commands
// that must not drift: they are two faces of the same file, so a server left on
// its own default would report on a database analyze never wrote.
func TestParse_DefaultsMatchTheCLIDatabase(t *testing.T) {
	cfg, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatalf("parse(nil) error: %v", err)
	}
	if cfg.nlpURL != "http://127.0.0.1:8000" {
		t.Errorf("nlp-url = %q, want http://127.0.0.1:8000", cfg.nlpURL)
	}
	if !strings.HasSuffix(cfg.dbPath, filepath.Join("lover-instance", "data.db")) {
		t.Errorf("db = %q, want a path ending in lover-instance/data.db", cfg.dbPath)
	}
	if filepath.Base(cfg.dbPath) != "data.db" {
		t.Errorf("db = %q, want the file named data.db", cfg.dbPath)
	}
}

// TestParse_DefaultsComeFromTheSharedLayer is hard constraint 1: the three
// defaults this command registers are internal/app's, field for field. Under a
// LOCALAPPDATA that names a real-looking directory, so a path default cannot
// pass by collapsing to its fallback.
func TestParse_DefaultsComeFromTheSharedLayer(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "test", "AppData", "Local"))
	cfg, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatalf("parse(nil) error: %v", err)
	}
	if cfg.dbPath != app.DefaultDBPath() {
		t.Errorf("--db default = %q, want app.DefaultDBPath() = %q", cfg.dbPath, app.DefaultDBPath())
	}
	if cfg.nlpURL != app.DefaultNLPURL() {
		t.Errorf("--nlp-url default = %q, want app.DefaultNLPURL() = %q", cfg.nlpURL, app.DefaultNLPURL())
	}
	if cfg.addr != app.DefaultDashboardAddr() {
		t.Errorf("--addr default = %q, want app.DefaultDashboardAddr() = %q", cfg.addr, app.DefaultDashboardAddr())
	}
}

// TestServerKeepsNoCopyOfADefault reads this package's own source. A duplicated
// default that happens to compute the same path today is still a duplication,
// and the only way to catch one is to look for the literals it would contain.
func TestServerKeepsNoCopyOfADefault(t *testing.T) {
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
