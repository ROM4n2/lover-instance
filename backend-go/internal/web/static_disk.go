//go:build !embedded

package web

import (
	"net/http"
	"os"
	"path/filepath"
)

// distFS locates a built dashboard on disk and opens it as a filesystem.
//
// The search order is the one the design fixes (D3). The environment variable,
// when set, is authoritative: if it names a directory without an index.html the
// lookup reports "missing" instead of silently falling through to some other
// copy, so a test and an operator both get the build they asked for.
//
// With no environment variable the fallbacks cover the two ways this binary
// runs: from the repository (go run ./cmd/server, where the build lands under
// backend-go/internal/web/dist and, if someone built there, frontend/dist) and
// from a directory holding a hand-copied dist beside the executable.
func distFS() (http.FileSystem, bool) {
	if env := os.Getenv("LOVER_FRONTEND_DIST"); env != "" {
		if hasIndex(env) {
			return http.Dir(env), true
		}
		return nil, false
	}
	for _, dir := range fallbackDirs() {
		if hasIndex(dir) {
			return http.Dir(dir), true
		}
	}
	return nil, false
}

// fallbackDirs is the ordered list of directories searched when the environment
// variable is unset. Duplicates are harmless: the first one holding an
// index.html wins.
func fallbackDirs() []string {
	dirs := []string{
		// Repo-relative paths, for `go run ./cmd/server` and `go test`.
		filepath.Join("frontend", "dist"),
		filepath.Join("backend-go", "internal", "web", "dist"),
		// Package-relative, for a test whose working directory is this package.
		"dist",
		filepath.Join("..", "frontend", "dist"),
		filepath.Join("..", "..", "..", "frontend", "dist"),
	}
	if exe, err := os.Executable(); err == nil {
		// Beside the binary, for a distributed copy that ships dist/ next to it.
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "dist"))
	}
	return dirs
}

// hasIndex reports whether dir exists and holds an index.html. A directory with
// assets but no index.html is not a usable build: the SPA has no entry point,
// so it is treated as absent rather than served half-working.
func hasIndex(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	info, err = os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !info.IsDir()
}
