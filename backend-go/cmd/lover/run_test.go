package main

// The run itself, end to end but offline: a fake NLP service, a temporary
// database and a temporary export. Nothing here starts uvicorn, installs a
// package or touches the real model cache.
//
// The assertions that matter are the ones the M5 plan calls out by name: the
// weekly line reports absolute coverage rather than this run's count, a missing
// dependency is never installed without --yes, and an interrupted run says so.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exportJSON is one conversation with four text messages and one image, the same
// shape cmd/analyze's tests use: the behavioral metrics have something to
// measure and the image is out of the sentiment pass, so coverage is 4/4 rather
// than 5/5.
const exportJSON = `[
  {"ts":1792166700.0,"sender":"Me","sender_wxid":"wxid_self","conversation_id":"wxid_peer",
   "content":"今天特别开心","msg_type":1,"is_self":true,"server_id":"1","source_offset":"o:1"},
  {"ts":1792166760.0,"sender":"Peer","sender_wxid":"wxid_peer","conversation_id":"wxid_peer",
   "content":"我也开心","msg_type":1,"is_self":false,"server_id":"2","source_offset":"o:2"},
  {"ts":1792166820.0,"sender":"Peer","sender_wxid":"wxid_peer","conversation_id":"wxid_peer",
   "content":"","msg_type":3,"is_self":false,"server_id":"3","source_offset":"o:3"},
  {"ts":1792166880.0,"sender":"Me","sender_wxid":"wxid_self","conversation_id":"wxid_peer",
   "content":"好难过","msg_type":1,"is_self":true,"server_id":"4","source_offset":"o:4"},
  {"ts":1792166940.0,"sender":"Peer","sender_wxid":"wxid_peer","conversation_id":"wxid_peer",
   "content":"别难过","msg_type":1,"is_self":false,"server_id":"5","source_offset":"o:5"}
]`

// fakeService stands in for the Python service: it reports the lexicon version on
// /health and echoes one score per item on /score.
func fakeService(t *testing.T, cancel func()) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ready", "model_loaded": false, "model_version": "lexicon-hownet-v1",
			"available_versions": []string{"lexicon-hownet-v1"},
		})
	})
	mux.HandleFunc("/score", func(w http.ResponseWriter, r *http.Request) {
		if cancel != nil {
			// The user presses Ctrl-C while the first block is being scored.
			cancel()
		}
		var request struct {
			Items []struct {
				MsgID int64 `json:"msg_id"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		results := make([]map[string]any, 0, len(request.Items))
		for _, item := range request.Items {
			results = append(results, map[string]any{
				"msg_id": item.MsgID, "polarity": 0.6, "emotion": "乐", "intensity": 0.5,
				"tokens": []string{"开心"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model_version": "lexicon-hownet-v1", "results": results})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// runEnv is one throwaway run: an export, a database and a report path.
type runEnv struct {
	export string
	db     string
	out    string
}

func newRunEnv(t *testing.T) runEnv {
	t.Helper()
	dir := t.TempDir()
	env := runEnv{
		export: filepath.Join(dir, "export.json"),
		db:     filepath.Join(dir, "data.db"),
		out:    filepath.Join(dir, "report.html"),
	}
	if err := os.WriteFile(env.export, []byte(exportJSON), 0o600); err != nil {
		t.Fatalf("write the export: %v", err)
	}
	return env
}

// args is the command line one run of `lover sync` gets. --nlp-url marks the
// service as somebody else's, which is what keeps these tests from starting one.
func (e runEnv) args(server *httptest.Server, extra ...string) []string {
	return append([]string{
		"sync", "--export", e.export, "--db", e.db, "--out", e.out,
		"--nlp-url", server.URL,
	}, extra...)
}

func TestSyncReportsAbsoluteCoverageNotThisRunsCount(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	first := invoke(t, context.Background(), env.args(server)...)
	if first.code != exitOK {
		t.Fatalf("first run exit code = %d, want %d; stderr:\n%s", first.code, exitOK, first.stderr)
	}
	if !strings.Contains(first.stdout, "新增 5 条 / 打分 4 条 / 覆盖率 4/4") {
		t.Errorf("the first run's weekly line is wrong: %q", first.stdout)
	}

	// The second run imports nothing and scores nothing, and must still report
	// the conversation as fully covered. This is the M2 pitfall: a line built
	// from this run's own numbers would read "覆盖率 0/0" on a finished
	// conversation and look like a regression every single week.
	second := invoke(t, context.Background(), env.args(server)...)
	if second.code != exitOK {
		t.Fatalf("second run exit code = %d, want %d; stderr:\n%s", second.code, exitOK, second.stderr)
	}
	if !strings.Contains(second.stdout, "新增 0 条 / 打分 0 条 / 覆盖率 4/4") {
		t.Errorf("the second run's weekly line is wrong: %q", second.stdout)
	}
	if _, err := os.Stat(env.out); err != nil {
		t.Errorf("no report was written: %v", err)
	}
}

// TestFirstRunReportsTheReportPath is the non-sync output: the same run, one
// different line.
func TestFirstRunReportsTheReportPath(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	got := invoke(t, context.Background(), "run", "--export", env.export, "--db", env.db,
		"--out", env.out, "--nlp-url", server.URL)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "report written to "+env.out) {
		t.Errorf("stdout does not name the report: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "覆盖率") {
		t.Errorf("a first run printed the weekly line: %q", got.stdout)
	}
}

// TestNoExportFoundNamesTheExporter keeps the one thing a person has to do
// outside this repository visible at the moment it is needed.
func TestNoExportFoundNamesTheExporter(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	env := newRunEnv(t)
	server := fakeService(t, nil)

	got := invoke(t, context.Background(), "run", "--db", env.db, "--out", env.out, "--nlp-url", server.URL)

	if got.code != exitRun {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitRun, got.stderr)
	}
	if !strings.Contains(got.stderr, "chatlog-keeper") {
		t.Errorf("stderr does not name the exporter: %q", got.stderr)
	}
}

// TestMissingDependenciesAreNotInstalledWithoutYes is the no-silent-install rule.
func TestMissingDependenciesAreNotInstalledWithoutYes(t *testing.T) {
	newRepo(t)
	stubProbe(t, "onnxruntime")
	installs := countInstalls(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	got := invoke(t, context.Background(), env.args(server)...)

	if got.code != exitEnv {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitEnv, got.stderr)
	}
	if !strings.Contains(got.stderr, "onnxruntime") {
		t.Errorf("stderr does not name the missing package: %q", got.stderr)
	}
	if !strings.Contains(got.stderr, "pip install -r nlp-service/requirements.txt") {
		t.Errorf("stderr does not give the install command: %q", got.stderr)
	}
	if *installs != 0 {
		t.Errorf("pip ran %d time(s) without --yes", *installs)
	}
	if _, err := os.Stat(env.out); !os.IsNotExist(err) {
		t.Error("a report was written by a run that never started")
	}
}

// TestYesInstallsTheDependenciesAndThenRuns is the announced install: the command
// says what it is about to do, does it, and carries on.
func TestYesInstallsTheDependenciesAndThenRuns(t *testing.T) {
	newRepo(t)
	stubReport(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	calls := 0
	restoreProbe := probeDependencies
	probeDependencies = func(context.Context, string, string, []string) ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"onnxruntime"}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { probeDependencies = restoreProbe })
	installs := countInstalls(t)

	got := invoke(t, context.Background(), env.args(server, "--yes")...)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if *installs != 1 {
		t.Errorf("pip ran %d time(s), want exactly one", *installs)
	}
	if !strings.Contains(got.stderr, "installing the Python dependencies") {
		t.Errorf("stderr does not announce the install: %q", got.stderr)
	}
	if !strings.Contains(got.stdout, "覆盖率 4/4") {
		t.Errorf("the run did not finish after the install: %q", got.stdout)
	}
}

// TestAnInterruptedRunSaysSo is the Ctrl-C contract: the service is collected, the
// job row is closed by app.Run on a context that ignores cancellation, and the
// exit code is exitRun with the interruption named.
func TestAnInterruptedRunSaysSo(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	env := newRunEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := fakeService(t, cancel)

	got := invoke(t, ctx, env.args(server)...)

	if got.code != exitRun {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitRun, got.stderr)
	}
	if !strings.Contains(got.stderr, "已中断") {
		t.Errorf("stderr does not say the run was interrupted: %q", got.stderr)
	}
	if strings.Contains(got.stderr, "wxid") {
		t.Errorf("stderr carries the conversation key: %q", got.stderr)
	}
	if _, err := os.Stat(env.out); !os.IsNotExist(err) {
		t.Error("a report was written by a run that was cancelled")
	}
}

// TestARunThatScoresNothingIsNotARegression is the other half of the coverage
// rule, asserted on the report rather than the line: a fully scored conversation
// must not be described as half done.
func TestARunThatScoresNothingIsNotARegression(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	for i := 0; i < 2; i++ {
		if got := invoke(t, context.Background(), env.args(server)...); got.code != exitOK {
			t.Fatalf("run %d failed: %s", i+1, got.stderr)
		}
	}
	body, err := os.ReadFile(env.out)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	if !strings.Contains(string(body), "lexicon-hownet-v1") {
		t.Error("the report does not name the model that scored it")
	}
}

// TestLatestExportPicksTheNewestFile is what `lover` with no --export does.
func TestLatestExportPicksTheNewestFile(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "2024-01-01.json")
	newer := filepath.Join(dir, "2024-06-01.json")
	if err := os.WriteFile(older, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An mtime in the future on the newer file: same-second writes would
	// otherwise make the choice depend on directory order.
	stamp := time.Now().Add(time.Hour)
	if err := os.Chtimes(newer, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// A non-JSON file must never be picked, whatever its age.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, stamp.Add(time.Hour), stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := latestExport(dir)
	if err != nil {
		t.Fatalf("latestExport() error = %v", err)
	}
	if got != newer {
		t.Errorf("latestExport() = %q, want %q", got, newer)
	}
}

func TestLatestExportRefusesAnEmptyDirectory(t *testing.T) {
	_, err := latestExport(t.TempDir())
	if err == nil {
		t.Fatal("latestExport() on an empty directory succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "chatlog-keeper") {
		t.Errorf("the refusal does not name the exporter: %v", err)
	}
}

// TestPrivacyNoMessageTextReachesStdout is the rule that applies to every line
// this command prints.
func TestPrivacyNoMessageTextReachesStdout(t *testing.T) {
	newRepo(t)
	stubProbe(t)
	stubReport(t)
	env := newRunEnv(t)
	server := fakeService(t, nil)

	got := invoke(t, context.Background(), env.args(server)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d", got.code, exitOK)
	}
	for _, forbidden := range []string{"开心", "难过", "wxid", "Peer"} {
		if strings.Contains(got.stdout, forbidden) {
			t.Errorf("stdout carries %q: %q", forbidden, got.stdout)
		}
		if strings.Contains(got.stderr, forbidden) {
			t.Errorf("stderr carries %q: %q", forbidden, got.stderr)
		}
	}
}
