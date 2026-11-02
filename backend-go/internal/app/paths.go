package app

import (
	"os"
	"path/filepath"
)

// The defaults in this file are the single source of every path and URL a run
// uses. They live here, and not in a command, because a second entry point that
// carried its own copy would drift from this one — the "one rule, two
// implementations" defect this package exists to prevent. A command registers
// its flags from these functions; nothing else may name a default.

// DefaultDBPath is the local-only location the design fixes. LOCALAPPDATA is
// where it goes on Windows precisely because it is not synced by default.
func DefaultDBPath() string {
	if root := os.Getenv("LOCALAPPDATA"); root != "" {
		return filepath.Join(root, "lover-instance", "data.db")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "lover-instance.db"
	}
	return filepath.Join(dir, "lover-instance", "data.db")
}

// DefaultNLPURL is where the sentiment service listens. It is loopback because
// the service reads the local database and nothing else may (ADR-0004).
func DefaultNLPURL() string { return "http://127.0.0.1:8000" }

// DefaultOllamaURL is the loopback endpoint the narrator talks to. Only a
// loopback address is ever accepted (ADR-0004).
func DefaultOllamaURL() string { return "http://127.0.0.1:11434" }

// DefaultDashboardAddr is where the dashboard's own server listens. It lives
// here, beside the other defaults, because the command that starts that server
// has to name the address to bind and the user has to be told where to point a
// browser — and a second copy of "127.0.0.1:8787" is a second thing to drift.
// cmd/server binds this one, as does the entry point that launches it, so the
// two agree by construction rather than by review.
//
// It is loopback and nothing else. "127.0.0.1:8787" rather than ":8787" because
// an empty host means every interface: on a laptop joined to a café's wifi that
// is a plaintext chat database answering requests from the room. There is no
// login in front of this server, so the interface it binds is the access
// control.
func DefaultDashboardAddr() string { return "127.0.0.1:8787" }

// DefaultReportPath is the default filename for the HTML report.
func DefaultReportPath() string { return "report.html" }

// DefaultDemoReportPath is the default filename for the demo report.
func DefaultDemoReportPath() string { return "demo_report.html" }

// NarrativeCacheDir is where narrations are remembered. It sits beside the
// database under LOCALAPPDATA, which is not synced by default, because a cached
// paragraph describes the conversation.
func NarrativeCacheDir() string {
	if root := os.Getenv("LOCALAPPDATA"); root != "" {
		return filepath.Join(root, "lover-instance", "narrative")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "lover-instance-narrative")
	}
	return filepath.Join(dir, "lover-instance", "narrative")
}

// ExportsDir is where a chatlog-keeper export is expected to be waiting. It
// follows the same LOCALAPPDATA rule as the database: an export describes the
// conversation, so it is not left somewhere a sync client would carry off.
func ExportsDir() string {
	if root := os.Getenv("LOCALAPPDATA"); root != "" {
		return filepath.Join(root, "lover-instance", "exports")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "lover-instance-exports")
	}
	return filepath.Join(dir, "lover-instance", "exports")
}
