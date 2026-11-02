package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoCommand_GeneratesReport(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "test_demo_report.html")

	var stdout, stderr bytes.Buffer
	code := runDemo(context.Background(), []string{"--out", outPath}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("expected exitOK, got %d, stderr: %s", code, stderr.String())
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("expected report file to exist: %v", err)
	}
	if info.Size() < 50_000 {
		t.Fatalf("expected report size > 50KB, got %d bytes", info.Size())
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	html := string(content)
	if !strings.Contains(html, "关系高光与里程碑") {
		t.Error("expected demo report to contain highlights section")
	}
}

func TestDemoCommand_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDemo(context.Background(), []string{"-h"}, &stdout, &stderr)
	if code != exitUsage {
		t.Errorf("expected exitUsage on -h with flag.ContinueOnError, got %d", code)
	}
}
