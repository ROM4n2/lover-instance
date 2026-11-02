package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"lover-instance/backend-go/internal/app"
	"lover-instance/backend-go/internal/demo"
)

// runDemo loads the embedded synthetic conversation, analyzes it, and generates a demo report.
func runDemo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lover demo", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var outPath string
	var openBrowser bool
	var dbPath string
	var nlpURL string

	flags.StringVar(&outPath, "out", app.DefaultDemoReportPath(), "Path to write the demo report HTML file")
	flags.BoolVar(&openBrowser, "open", false, "Open the generated report in system browser")
	flags.StringVar(&dbPath, "db", "", "Path to demo SQLite database (defaults to temporary database)")
	flags.StringVar(&nlpURL, "nlp-url", app.DefaultNLPURL(), "URL of running NLP service")

	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	if dbPath == "" {
		tempDir, err := os.MkdirTemp("", "lover_demo_*")
		if err != nil {
			fmt.Fprintf(stderr, "lover demo: failed to create temporary dir: %v\n", err)
			return exitRun
		}
		defer func() { _ = os.RemoveAll(tempDir) }()
		dbPath = filepath.Join(tempDir, "demo.db")
	}

	opts := app.DefaultOptions()
	opts.DBPath = dbPath
	opts.OutPath = outPath
	opts.ExportReader = bytes.NewReader(demo.SampleChatJSON)
	opts.Talker = demo.SampleTalker
	opts.NoNarrative = true

	// Check if NLP service is reachable; if not, skip sentiment for zero-dependency offline demo.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(nlpURL + "/api/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		opts.SkipSentiment = true
		fmt.Fprintf(stderr, "lover demo: NLP service at %s offline, running in fast offline mode\n", nlpURL)
	} else {
		_ = resp.Body.Close()
		opts.NLPURL = nlpURL
	}

	fmt.Fprintf(stderr, "lover demo: analyzing sample conversation (%d bytes)...\n", len(demo.SampleChatJSON))

	summary, exitCode, err := app.Run(ctx, opts, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "lover demo: analysis failed: %v\n", err)
		return exitCode
	}

	fmt.Fprintf(stdout, "lover demo: report written to %s (%d messages, %d bytes)\n",
		summary.ReportPath, summary.Messages, summary.ReportBytes)

	if openBrowser {
		openReport(summary.ReportPath, stderr)
	}

	return exitOK
}
