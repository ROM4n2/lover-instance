package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"lover-instance/backend-go/internal/app"
)

// runPull locates the external chatlog-keeper sidecar, extracts recent messages,
// and streams them into the lover sync pipeline.
func runPull(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lover pull", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var days int
	var talker string
	var dataRoot string

	flags.IntVar(&days, "days", 30, "lookback window in days to extract from WeChat")
	flags.StringVar(&talker, "talker", "", "target conversation wxid (defaults to single talker in store)")
	flags.StringVar(&dataRoot, "data-root", "", "WeChat data folder (xwechat_files)")

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "lover pull: %v\n", err)
		return exitEnv
	}

	sidecarCmd, sidecarArgs, err := findSidecar(root, days, talker, dataRoot)
	if err != nil {
		fmt.Fprintf(stderr, "lover pull: %v\n", err)
		return exitEnv
	}

	fmt.Fprintf(stderr, "lover pull: extracting from WeChat via sidecar...\n")
	cmd := exec.CommandContext(ctx, sidecarCmd, sidecarArgs...)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "lover pull: sidecar extraction failed: %v\n", err)
		return exitRun
	}

	exportPath := filepath.Join(app.ExportsDir(), "auto_sync", "wechat_messages.json")
	if _, err := os.Stat(exportPath); err != nil {
		fmt.Fprintf(stderr, "lover pull: expected export file not found at %s\n", exportPath)
		return exitRun
	}

	syncArgs := []string{"--export", exportPath}
	if talker != "" {
		syncArgs = append(syncArgs, "--talker", talker)
	}

	return runFirstRun(ctx, syncArgs, stdout, stderr, true)
}

func findSidecar(root string, days int, talker, dataRoot string) (string, []string, error) {
	// Priority 1: Sibling repository chatlog-keeper (Python module)
	siblingRepo := filepath.Join(filepath.Dir(root), "chatlog-keeper")
	if info, err := os.Stat(siblingRepo); err == nil && info.IsDir() {
		python := pythonCommand()
		args := []string{"-m", "chatlog_keeper.cli", "wechat", "--lover", fmt.Sprintf("--days=%d", days)}
		if talker != "" {
			args = append(args, "--conversation", talker)
		}
		if dataRoot != "" {
			args = append(args, "--data-root", dataRoot)
		}
		return python, args, nil
	}

	// Priority 2: PATH chatlog-keeper
	if path, err := exec.LookPath("chatlog-keeper"); err == nil {
		args := []string{"wechat", "--lover", fmt.Sprintf("--days=%d", days)}
		if talker != "" {
			args = append(args, "--conversation", talker)
		}
		if dataRoot != "" {
			args = append(args, "--data-root", dataRoot)
		}
		return path, args, nil
	}

	return "", nil, fmt.Errorf("no chatlog-keeper sidecar found; clone ROM4n2/chatlog-keeper as sibling folder or install chatlog-keeper on PATH")
}
