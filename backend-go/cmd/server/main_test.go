package main

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

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
	if cfg.addr != defaultAddr {
		t.Errorf("addr = %q, want %q", cfg.addr, defaultAddr)
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
