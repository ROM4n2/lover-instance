package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	sidecarCmd, sidecarArgs, sidecarDir, err := findSidecar(root, days, talker, dataRoot)
	if err != nil {
		fmt.Fprintf(stderr, "lover pull: %v\n", err)
		return exitEnv
	}

	fmt.Fprintf(stderr, "lover pull: extracting from WeChat via sidecar...\n")
	cmd := exec.CommandContext(ctx, sidecarCmd, sidecarArgs...)
	if sidecarDir != "" {
		cmd.Dir = sidecarDir
	}
	// The sidecar's summary is JSON on stdout and its progress lines are on
	// stderr. The summary is what says whether anything was extracted, so it is
	// captured and echoed rather than passed straight through.
	var summary bytes.Buffer
	cmd.Stdout = io.MultiWriter(stderr, &summary)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "lover pull: sidecar extraction failed: %v\n", err)
		return exitRun
	}

	// "No new messages" is a normal outcome of a re-run, not a failure. The
	// ingest layer refuses an empty export on purpose (an empty import reported
	// as success is how a broken export turns into a silently empty report), so
	// the emptiness is caught here, where it means "you are up to date".
	if extracted := extractedCount(summary.Bytes()); extracted == 0 {
		fmt.Fprintf(stdout, "lover pull: 已是最新（sidecar 没有返回新消息）\n")
		return exitOK
	}

	exportPath := filepath.Join(app.ExportsDir(), "auto_sync", "wechat_messages.json")
	if _, err := os.Stat(exportPath); err != nil {
		fmt.Fprintf(stderr, "lover pull: expected export file not found at %s\n", exportPath)
		return exitRun
	}

	// This tool reports on one couple. When the sidecar could not be told which
	// conversation to extract, the export can hold every chat the account has,
	// and analysing all of them at once is refused by the pipeline for good
	// reason. The conversation with the most messages is the one a person means
	// by "ours", so it is chosen here and named out loud rather than left as an
	// error the user has to resolve with a flag they cannot know.
	if talker == "" {
		picked, count, err := largestConversation(exportPath)
		if err != nil {
			fmt.Fprintf(stderr, "lover pull: %v\n", err)
			return exitRun
		}
		if picked == "" {
			fmt.Fprintf(stdout, "lover pull: 已是最新（sidecar 没有返回新消息）\n")
			return exitOK
		}
		fmt.Fprintf(stderr, "lover pull: 分析会话 %s（本次导出中消息最多，%d 条）\n", picked, count)
		talker = picked
	}

	syncArgs := []string{"--export", exportPath}
	if talker != "" {
		syncArgs = append(syncArgs, "--talker", talker)
	}

	return runFirstRun(ctx, syncArgs, stdout, stderr, true)
}

// largestConversation returns the conversation_id with the most messages in the
// export, and its message count.
func largestConversation(path string) (string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	var rows []struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(file).Decode(&rows); err != nil {
		return "", 0, fmt.Errorf("read the export %s: %w", filepath.Base(path), err)
	}
	counts := make(map[string]int, 8)
	for _, row := range rows {
		if row.ConversationID == "" {
			continue
		}
		counts[row.ConversationID]++
	}
	best, bestCount := "", 0
	for talker, count := range counts {
		if count > bestCount || (count == bestCount && talker < best) {
			best, bestCount = talker, count
		}
	}
	return best, bestCount, nil
}

// extractedCount reads n_messages out of the sidecar's summary. A summary this
// does not understand counts as "something was extracted": guessing zero would
// turn an unreadable line into a false "up to date".
func extractedCount(summary []byte) int {
	start := bytes.Index(summary, []byte("\"n_messages\""))
	if start < 0 {
		return -1
	}
	rest := summary[start+len("\"n_messages\""):]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return -1
	}
	rest = bytes.TrimSpace(rest[colon+1:])
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	n := 0
	for _, digit := range rest[:end] {
		n = n*10 + int(digit-'0')
	}
	return n
}

func findSidecar(root string, days int, talker, dataRoot string) (string, []string, string, error) {
	// Priority 0: Same folder as the running lover executable or current directory
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "chatlog-keeper.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			args := []string{"wechat", "--lover", fmt.Sprintf("--days=%d", days)}
			if talker != "" {
				args = append(args, "--conversation", talker)
			}
			if dataRoot != "" {
				args = append(args, "--data-root", dataRoot)
			}
			return candidate, args, "", nil
		}
	}
	if info, err := os.Stat("./chatlog-keeper.exe"); err == nil && !info.IsDir() {
		args := []string{"wechat", "--lover", fmt.Sprintf("--days=%d", days)}
		if talker != "" {
			args = append(args, "--conversation", talker)
		}
		if dataRoot != "" {
			args = append(args, "--data-root", dataRoot)
		}
		return "./chatlog-keeper.exe", args, "", nil
	}

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
		return python, args, siblingRepo, nil
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
		return path, args, "", nil
	}

	return "", nil, "", fmt.Errorf("no chatlog-keeper sidecar found; download chatlog-keeper.exe and put it in the same folder as lover.exe")
}
