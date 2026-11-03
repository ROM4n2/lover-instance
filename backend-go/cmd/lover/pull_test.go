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
