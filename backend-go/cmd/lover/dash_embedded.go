//go:build embedded

package main

// A release build is one file shipped to a machine with no Go toolchain, no
// source tree and no npm. Nothing can be compiled there, so the dashboard is
// served by re-running this same binary as `lover serve`: the handler, the
// templates and the frontend bundle are all already inside the executable.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const dashboardEmbedded = true

func init() { startDashboardServer = serveSelf }

// serveSelf supervises `lover serve` from the directory holding this binary.
// Running from that directory is what makes a double-clicked exe behave the same
// as one started from a terminal: the database, the exports folder and a sibling
// chatlog-keeper.exe are all resolved relative to it.
func serveSelf(ctx context.Context, cfg dashOptions, root string, stderr io.Writer) (*child, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(exe)
	cmd := exec.CommandContext(ctx, exe, "serve", "--db", cfg.dbPath, "--nlp-url", cfg.nlpURL, "--addr", cfg.addr)
	return newChild(ctx, cmd, dir, stderr)
}
