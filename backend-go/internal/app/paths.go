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
