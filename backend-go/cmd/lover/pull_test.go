package main

import (
	"bytes"
	"context"
	"testing"
)

func TestPullCommand_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPull(context.Background(), []string{"-h"}, &stdout, &stderr)
	if code != exitUsage {
		t.Errorf("expected exitUsage on -h, got %d", code)
	}
}

// TestExtractedCount covers the one decision `lover pull` makes from the
// sidecar's summary: whether "nothing new" is a normal re-run or an unreadable
// answer. Getting the second case wrong would turn a broken export into a
// cheerful "已是最新".
func TestExtractedCount(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    int
	}{
		{"nothing new", `{"source":"wechat","available":true,"n_messages":0}`, 0},
		{"some messages", `{"n_messages": 1806}`, 1806},
		{"pretty printed", "{\n  \"source\": \"wechat\",\n  \"n_messages\": 12,\n}", 12},
		{"not a summary", "sidecar exploded", -1},
		{"missing field", `{"available":false}`, -1},
		{"unparsable value", `{"n_messages":"many"}`, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractedCount([]byte(tc.summary)); got != tc.want {
				t.Errorf("extractedCount(%q) = %d, want %d", tc.summary, got, tc.want)
			}
		})
	}
}
