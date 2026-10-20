package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"lover-instance/backend-go/internal/report"
	"lover-instance/backend-go/internal/store"
)

// exportJSON is a minimal but complete chatlog-keeper export: one conversation,
// four text messages and one image, timestamps two minutes apart so the
// behavioral metrics have a reply latency to measure.
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

// secondConversation appends a message from another conversation, so the
// single-talker inference has something to refuse.
const twoConversationExport = `[
  {"ts":1792166700.0,"sender":"Me","sender_wxid":"wxid_self","conversation_id":"wxid_a",
   "content":"你好","msg_type":1,"is_self":true,"server_id":"1","source_offset":"o:1"},
  {"ts":1792166760.0,"sender":"Other","sender_wxid":"wxid_c","conversation_id":"wxid_b",
   "content":"在吗","msg_type":1,"is_self":false,"server_id":"2","source_offset":"o:2"}
]`

type invocation struct {
	code   int
	stdout string
	stderr string
}

func invoke(t *testing.T, args ...string) invocation {
	t.Helper()
	return invokeWithContext(t, context.Background(), args...)
}

// invokeWithContext runs the command under a context the test controls, which
// is how a test presses Ctrl-C without a real signal.
func invokeWithContext(t *testing.T, ctx context.Context, args ...string) invocation {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(ctx, args, &out, &errOut)
	return invocation{code: code, stdout: out.String(), stderr: errOut.String()}
}

// harness is one throwaway environment: an export file, a database path and a
// report path, all inside the test's temporary directory.
type harness struct {
	dir    string
	export string
	db     string
	out    string
}

func newHarness(t *testing.T) harness {
	t.Helper()
	dir := t.TempDir()
	h := harness{
		dir:    dir,
		export: filepath.Join(dir, "export.json"),
		db:     filepath.Join(dir, "data.db"),
		out:    filepath.Join(dir, "report.html"),
	}
	if err := os.WriteFile(h.export, []byte(exportJSON), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	return h
}

func (h harness) args(extra ...string) []string {
	return append([]string{"--export", h.export, "--db", h.db, "--out", h.out}, extra...)
}

func TestRun_WritesAReportAndSummarizesItInOneLine(t *testing.T) {
	h := newHarness(t)
	got := invoke(t, h.args("--skip-sentiment")...)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if strings.Count(strings.TrimSpace(got.stdout), "\n") != 0 {
		t.Errorf("stdout is not a single line: %q", got.stdout)
	}
	var parsed summary
	if err := json.Unmarshal([]byte(strings.TrimSpace(got.stdout)), &parsed); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, got.stdout)
	}
	if parsed.Parsed != 5 || parsed.Inserted != 5 {
		t.Errorf("Parsed/Inserted = %d/%d, want 5/5", parsed.Parsed, parsed.Inserted)
	}
	if parsed.Messages != 5 {
		t.Errorf("Messages = %d, want 5 (behavioral metrics cover every type)", parsed.Messages)
	}
	if parsed.Total != 4 {
		t.Errorf("Total = %d, want 4 (the image is out of the sentiment pass)", parsed.Total)
	}
	if parsed.TalkerHash == "" || strings.Contains(parsed.TalkerHash, "wxid") {
		t.Errorf("TalkerHash = %q, want a fingerprint rather than the conversation key", parsed.TalkerHash)
	}
	if parsed.ReportBytes <= 0 {
		t.Errorf("ReportBytes = %d, want the size of what was written", parsed.ReportBytes)
	}

	body, err := os.ReadFile(h.out)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	if len(body) != parsed.ReportBytes {
		t.Errorf("report on disk is %d bytes but the summary claimed %d", len(body), parsed.ReportBytes)
	}
	if refs := report.ExternalRefs(body); len(refs) != 0 {
		t.Errorf("the written report reaches the network: %v", refs)
	}
	if _, err := os.Stat(h.out + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind next to the report")
	}
}

// TestRun_PrintsTheFingerprintRatherThanTheKey guards the summary line against
// becoming a place conversation keys leak into pasted output.
func TestRun_PrintsTheFingerprintRatherThanTheKey(t *testing.T) {
	h := newHarness(t)
	got := invoke(t, h.args("--skip-sentiment")...)

	if strings.Contains(got.stdout, "wxid_peer") {
		t.Errorf("stdout carries the conversation key: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "开心") {
		t.Errorf("stdout carries message text: %q", got.stdout)
	}
}

func TestRun_RejectsSyncedPathsBeforeCreatingAnything(t *testing.T) {
	h := newHarness(t)
	synced := filepath.Join(h.dir, "OneDrive", "data.db")
	out := invoke(t, "--export", h.export, "--db", synced, "--out", h.out, "--skip-sentiment")

	if out.code != exitFailure {
		t.Fatalf("exit code = %d, want %d", out.code, exitFailure)
	}
	if !strings.Contains(strings.ToLower(out.stderr), "onedrive") {
		t.Errorf("stderr does not name the folder it refused: %q", out.stderr)
	}
	if _, err := os.Stat(synced); !os.IsNotExist(err) {
		t.Error("the database was created under a synced folder despite the refusal")
	}
}

func TestRun_RequiresATalkerWhenTheExportHoldsMoreThanOne(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.export, []byte(twoConversationExport), 0o600); err != nil {
		t.Fatal(err)
	}
	got := invoke(t, h.args("--skip-sentiment")...)

	if got.code != exitUsage {
		t.Fatalf("exit code = %d, want %d for an ambiguous talker; stderr: %s", got.code, exitUsage, got.stderr)
	}
	// The import has already happened by the time the choice is needed, and
	// the message has to say so or the user will fear a second full import.
	if !strings.Contains(got.stderr, "stored") {
		t.Errorf("stderr does not explain that the import succeeded: %q", got.stderr)
	}
	if _, err := os.Stat(h.out); !os.IsNotExist(err) {
		t.Error("a report was written for a run that could not choose a conversation")
	}
}

func TestRun_NarrowingToATalkerWorksAfterAnAmbiguousRun(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.export, []byte(twoConversationExport), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitUsage {
		t.Fatalf("first run exit code = %d, want %d", out.code, exitUsage)
	}

	got := invoke(t, h.args("--talker", "wxid_a", "--skip-sentiment")...)
	if got.code != exitOK {
		t.Fatalf("second run exit code = %d, want %d; stderr: %s", got.code, exitOK, got.stderr)
	}
	var parsed summary
	if err := json.Unmarshal([]byte(strings.TrimSpace(got.stdout)), &parsed); err != nil {
		t.Fatal(err)
	}
	// The first run imported both conversations; the second must not import
	// them again, and the rescan window must not duplicate the rows.
	if parsed.Inserted != 0 {
		t.Errorf("Inserted = %d on the second run, want 0: the rows were already stored", parsed.Inserted)
	}
	if parsed.Messages != 1 {
		t.Errorf("Messages = %d, want 1 for wxid_a alone", parsed.Messages)
	}
}

func TestRun_ListTalkersPrintsKeysAndRefusesAMissingDatabase(t *testing.T) {
	h := newHarness(t)

	missing := invoke(t, "--db", h.db, "--list-talkers")
	if missing.code != exitFailure {
		t.Errorf("exit code for a missing database = %d, want %d", missing.code, exitFailure)
	}
	if _, err := os.Stat(h.db); !os.IsNotExist(err) {
		t.Error("listing conversations created a database file")
	}

	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitOK {
		t.Fatalf("import run failed: %s", out.stderr)
	}
	listed := invoke(t, "--db", h.db, "--list-talkers")
	if listed.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", listed.code, exitOK, listed.stderr)
	}
	if strings.TrimSpace(listed.stdout) != "wxid_peer" {
		t.Errorf("listed conversations = %q, want wxid_peer", listed.stdout)
	}
}

func TestRun_UsageErrorsExitTwo(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		args []string
	}{
		{"no export", []string{"--db", h.db}},
		{"unknown flag", []string{"--export", h.export, "--db", h.db, "--frobnicate"}},
		{"positional argument", []string{"--export", h.export, "--db", h.db, "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := invoke(t, tc.args...); got.code != exitUsage {
				t.Errorf("exit code = %d, want %d; stderr: %s", got.code, exitUsage, got.stderr)
			}
		})
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	got := invoke(t, "--help")
	if got.code != exitOK {
		t.Errorf("exit code = %d, want %d for --help", got.code, exitOK)
	}
	if !strings.Contains(got.stderr, "Usage: analyze") {
		t.Errorf("help text was not printed: %q", got.stderr)
	}
}

func TestRun_ReportsAMissingExportAsAFailureNotAUsageError(t *testing.T) {
	h := newHarness(t)
	got := invoke(t, "--export", filepath.Join(h.dir, "absent.json"), "--db", h.db, "--out", h.out)

	if got.code != exitFailure {
		t.Errorf("exit code = %d, want %d", got.code, exitFailure)
	}
	if !strings.Contains(got.stderr, "open export") {
		t.Errorf("stderr does not say what could not be opened: %q", got.stderr)
	}
}

// TestRun_RefusesAnEmptyExport keeps the "nothing imported" shape out of the
// success path, which is the one outcome the whole pipeline exists to avoid.
func TestRun_RefusesAnEmptyExport(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.export, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := invoke(t, h.args("--skip-sentiment")...)

	if got.code != exitFailure {
		t.Fatalf("exit code = %d, want %d", got.code, exitFailure)
	}
	if _, err := os.Stat(h.out); !os.IsNotExist(err) {
		t.Error("a report was written for an import that stored nothing")
	}
}

// fakeNLPService stands in for the Python service, reporting the lexicon
// version it holds and echoing one score per item.
func fakeNLPService(t *testing.T, modelVersion string) *httptest.Server {
	t.Helper()
	return fakeNLPServiceWithScore(t, modelVersion, echoScore)
}

// echoScore is the healthy service: one score per item it was sent.
func echoScore(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ModelVersion string `json:"model_version"`
		Items        []struct {
			MsgID   int64  `json:"msg_id"`
			Content string `json:"content"`
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
			"tokens": []string{"开心", "[捂脸]"},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model_version": request.ModelVersion, "results": results})
}

// fakeNLPServiceWithScore serves the same readiness report but hands /score to
// the caller, so a test can make the scoring pass fail or stop mid-run without
// standing up a second kind of service.
func fakeNLPServiceWithScore(t *testing.T, modelVersion string, score http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ready", "model_loaded": false, "model_version": modelVersion,
		})
	})
	mux.HandleFunc("/score", score)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRun_ScoresThroughTheServiceAndRecordsItsVersion is the end-to-end path:
// import, behavioral metrics, sentiment, report.
func TestRun_ScoresThroughTheServiceAndRecordsItsVersion(t *testing.T) {
	h := newHarness(t)
	srv := fakeNLPService(t, "lexicon-hownet-v1")

	got := invoke(t, h.args("--nlp-url", srv.URL)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitOK, got.stderr)
	}
	var parsed summary
	if err := json.Unmarshal([]byte(strings.TrimSpace(got.stdout)), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Scored != 4 || parsed.Coverage != 4 || parsed.Partial {
		t.Errorf("Scored/Coverage/Partial = %d/%d/%v, want 4/4/false", parsed.Scored, parsed.Coverage, parsed.Partial)
	}
	if parsed.SentimentModel != "lexicon-hownet-v1" {
		t.Errorf("SentimentModel = %q, want the version the service reported", parsed.SentimentModel)
	}

	// The version has to reach the reader, not just the summary line.
	body, err := os.ReadFile(h.out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "lexicon-hownet-v1") {
		t.Error("the report does not name the model that scored it")
	}
}

// TestRun_RefusesADeadServiceInsteadOfWritingAnUnlabeledReport. A behavioral
// report is a legitimate artifact, but it must be asked for explicitly.
func TestRun_RefusesADeadServiceInsteadOfWritingAnUnlabeledReport(t *testing.T) {
	h := newHarness(t)
	srv := fakeNLPService(t, "lexicon-hownet-v1")
	srv.Close()

	got := invoke(t, h.args("--nlp-url", srv.URL)...)
	if got.code != exitFailure {
		t.Fatalf("exit code = %d, want %d", got.code, exitFailure)
	}
	if !strings.Contains(got.stderr, "--skip-sentiment") {
		t.Errorf("stderr does not tell the user how to proceed: %q", got.stderr)
	}
	if _, err := os.Stat(h.out); !os.IsNotExist(err) {
		t.Error("a report was written even though scoring failed")
	}
}

// TestRun_ResumesAfterAFailedScoringPass. The messages are imported and the
// behavioral metrics are rebuilt before scoring starts, so a scoring failure
// costs only the scoring: the same command picks up where it stopped.
func TestRun_ResumesAfterAFailedScoringPass(t *testing.T) {
	h := newHarness(t)

	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitOK {
		t.Fatalf("behavioral run failed: %s", out.stderr)
	}
	srv := fakeNLPService(t, "lexicon-hownet-v1")
	scored := invoke(t, h.args("--nlp-url", srv.URL)...)
	if scored.code != exitOK {
		t.Fatalf("scoring run failed: %s", scored.stderr)
	}
	var parsed summary
	if err := json.Unmarshal([]byte(strings.TrimSpace(scored.stdout)), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Inserted != 0 {
		t.Errorf("Inserted = %d on the second run, want 0", parsed.Inserted)
	}
	if parsed.Scored != 4 || parsed.Coverage != 4 {
		t.Errorf("Scored/Coverage = %d/%d, want 4/4", parsed.Scored, parsed.Coverage)
	}

	// The third run scores nothing because there is nothing left to score, and
	// must still report the conversation as fully covered. Calling this partial
	// is the shape the acceptance run over a real export produced: a finished
	// conversation described as half done, purely because the run itself had no
	// work to do.
	third := invoke(t, h.args("--nlp-url", srv.URL)...)
	if third.code != exitOK {
		t.Fatalf("third run failed: %s", third.stderr)
	}
	var again summary
	if err := json.Unmarshal([]byte(strings.TrimSpace(third.stdout)), &again); err != nil {
		t.Fatal(err)
	}
	if again.Scored != 0 {
		t.Errorf("scored_this_run = %d on a completed conversation, want 0", again.Scored)
	}
	if again.Coverage != 4 || again.Partial {
		t.Errorf("Coverage = %d, Partial = %v, want 4 and false: a fully scored conversation must not be reported as incomplete", again.Coverage, again.Partial)
	}
}

// TestRun_LeavesTheAggregateTablesPopulated. The report recomputes the
// behavioral metrics from the message stream, so it looks right whether or not
// the rollup tables were written — which means only the tables themselves can
// show that the rebuild ran. They are what the dashboard reads.
func TestRun_LeavesTheAggregateTablesPopulated(t *testing.T) {
	h := newHarness(t)
	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitOK {
		t.Fatalf("run failed: %s", out.stderr)
	}

	ctx := context.Background()
	dsn, err := store.DSN(h.db)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for _, tc := range []struct {
		table string
		query string
	}{
		{"daily_stats", `SELECT COUNT(*) FROM daily_stats WHERE talker = 'wxid_peer' AND model_version = ?`},
		{"activity_heatmap", `SELECT COUNT(*) FROM activity_heatmap WHERE talker = 'wxid_peer'`},
		{"conversations", `SELECT COUNT(*) FROM conversations WHERE talker = 'wxid_peer'`},
	} {
		var count int
		args := []any{}
		if strings.Contains(tc.query, "?") {
			args = append(args, store.BehaviorModelVersion)
		}
		if err := s.DB().QueryRowContext(ctx, tc.query, args...).Scan(&count); err != nil {
			t.Errorf("count %s: %v", tc.table, err)
			continue
		}
		if count == 0 {
			t.Errorf("%s has no rows for the conversation: the behavioral rebuild did not run", tc.table)
		}
	}
}

// openDB opens the database a finished run left behind, so a test can read the
// rows the command wrote rather than trusting its stdout.
func openDB(t *testing.T, path string) *store.Store {
	t.Helper()
	dsn, err := store.DSN(path)
	if err != nil {
		t.Fatalf("store.DSN() error: %v", err)
	}
	s, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRun_RecordsTheSentimentRunAsAJob is the wiring check. This command is the
// only caller of jobs.Start, so a mistake here — a job that is never finished,
// progress that is never recorded — would pass every package-level test and
// still leave a row saying the run never got anywhere.
func TestRun_RecordsTheSentimentRunAsAJob(t *testing.T) {
	h := newHarness(t)
	srv := fakeNLPService(t, "lexicon-hownet-v1")
	if got := invoke(t, h.args("--nlp-url", srv.URL)...); got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitOK, got.stderr)
	}

	ctx := context.Background()
	s := openDB(t, h.db)
	var status, modelVersion string
	var done, total int
	err := s.DB().QueryRowContext(ctx,
		`SELECT status, model_version, done, total FROM analysis_jobs WHERE kind = 'sentiment'`).
		Scan(&status, &modelVersion, &done, &total)
	if err != nil {
		t.Fatalf("read the job row: %v", err)
	}
	if status != "done" {
		t.Errorf("status = %q, want %q", status, "done")
	}
	if modelVersion != "lexicon-hownet-v1" {
		t.Errorf("model_version = %q, want the version the service reported", modelVersion)
	}
	// Four analyzable messages, and the last heartbeat lands before Finish.
	if done != 4 || total != 4 {
		t.Errorf("done/total = %d/%d, want 4/4", done, total)
	}
}

// TestRun_RecordsCoverageOnARunThatFindsNothingLeftToScore covers the case the
// per-block heartbeat cannot: a second run over a fully scored conversation
// sends no blocks at all, so a job row built only from block callbacks would
// say 0/0 and a dashboard would show a finished conversation as never started.
func TestRun_RecordsCoverageOnARunThatFindsNothingLeftToScore(t *testing.T) {
	h := newHarness(t)
	srv := fakeNLPService(t, "lexicon-hownet-v1")
	for i := 0; i < 2; i++ {
		if got := invoke(t, h.args("--nlp-url", srv.URL)...); got.code != exitOK {
			t.Fatalf("run %d failed: %s", i+1, got.stderr)
		}
	}

	var done, total int
	err := openDB(t, h.db).DB().QueryRowContext(context.Background(),
		`SELECT done, total FROM analysis_jobs WHERE kind = 'sentiment' ORDER BY id DESC LIMIT 1`).
		Scan(&done, &total)
	if err != nil {
		t.Fatalf("read the job row: %v", err)
	}
	if done != 4 || total != 4 {
		t.Errorf("done/total = %d/%d, want 4/4: the row must report the conversation's coverage, not this run's zero blocks", done, total)
	}
}

// onlySentimentJob reads the one job row a scoring run left behind.
func onlySentimentJob(t *testing.T, path string) (status, errMsg string) {
	t.Helper()
	err := openDB(t, path).DB().QueryRowContext(context.Background(),
		`SELECT status, COALESCE(error, '') FROM analysis_jobs WHERE kind = 'sentiment'`).
		Scan(&status, &errMsg)
	if err != nil {
		t.Fatalf("read the job row: %v", err)
	}
	return status, errMsg
}

// TestRun_ClosesTheJobWhenTheUserCancels is the Ctrl-C path. Cancelling the
// context is what a signal does, and every write made with that context then
// fails: database/sql checks it before it will even hand out the connection.
// The closing write is the one that must not inherit it, or the row stays
// "running" forever and the CLI says the job could not be closed — which is
// how the canceled status became unreachable in the first place.
func TestRun_ClosesTheJobWhenTheUserCancels(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := fakeNLPServiceWithScore(t, "lexicon-hownet-v1", func(w http.ResponseWriter, r *http.Request) {
		// The user presses Ctrl-C while the first block is being scored.
		cancel()
		echoScore(w, r)
	})

	got := invokeWithContext(t, ctx, h.args("--nlp-url", srv.URL)...)
	if got.code != exitFailure {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitFailure, got.stderr)
	}
	if strings.Contains(got.stderr, "could not be closed") {
		t.Errorf("stderr reports the job could not be closed: %q", got.stderr)
	}
	if strings.Contains(got.stderr, "wxid") {
		t.Errorf("stderr carries the conversation key: %q", got.stderr)
	}

	status, errMsg := onlySentimentJob(t, h.db)
	if status != "canceled" {
		t.Errorf("job status = %q, want %q: a run the user stopped must record that, not stay running", status, "canceled")
	}
	if errMsg == "" {
		t.Error("the error column is empty, want the cancellation on record")
	}
}

// TestRun_RecordsAFailedScoringJob is the other half of the same wiring: a
// service that refuses to score leaves a job saying "failed" and saying why.
// Without the error column a reader cannot tell a run that never started from
// one that died on its third block.
func TestRun_RecordsAFailedScoringJob(t *testing.T) {
	h := newHarness(t)
	srv := fakeNLPServiceWithScore(t, "lexicon-hownet-v1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "lexicon exploded", http.StatusInternalServerError)
	})

	got := invoke(t, h.args("--nlp-url", srv.URL)...)
	if got.code != exitFailure {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitFailure, got.stderr)
	}

	status, errMsg := onlySentimentJob(t, h.db)
	if status != "failed" {
		t.Errorf("job status = %q, want %q", status, "failed")
	}
	if errMsg == "" {
		t.Error("the error column is empty, want the reason the pass stopped")
	}
	if strings.Contains(errMsg, "开心") {
		t.Errorf("the error column carries message text: %q", errMsg)
	}
}

// TestRun_RefusesToStartASecondPassWhileAJobHoldsTheSlot is the contract
// between jobs.Start and its only caller. Start reports created; a false means
// another run owns the (talker, kind) slot, and this command must not send a
// second conversation's worth of blocks through the service on top of it. The
// row is left queued on purpose: Recover only repairs running jobs, so this is
// exactly the state where the slot is held by something this process did not
// create.
func TestRun_RefusesToStartASecondPassWhileAJobHoldsTheSlot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitOK {
		t.Fatalf("behavioral run failed: %s", out.stderr)
	}

	held := openDB(t, h.db)
	job, err := held.InsertJob(ctx, "wxid_peer", "sentiment", "lexicon-hownet-v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	// The behavioral run above legitimately wrote a report; clearing it is what
	// makes "no report this time" a statement about the refused run.
	if err := os.Remove(h.out); err != nil {
		t.Fatalf("remove the previous report: %v", err)
	}

	// The handler runs on the server's goroutines, so the count is atomic: a
	// regression has to fail on the assertion, not on a data race.
	var blocks atomic.Int64
	srv := fakeNLPServiceWithScore(t, "lexicon-hownet-v1", func(w http.ResponseWriter, r *http.Request) {
		blocks.Add(1)
		echoScore(w, r)
	})
	got := invoke(t, h.args("--nlp-url", srv.URL)...)

	if got.code != exitFailure {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitFailure, got.stderr)
	}
	if got := blocks.Load(); got != 0 {
		t.Errorf("the service was asked to score %d block(s), want 0: the slot was already held", got)
	}
	if !strings.Contains(got.stderr, "already holds this conversation") {
		t.Errorf("stderr does not explain why the run stopped: %q", got.stderr)
	}
	if strings.Contains(got.stderr, "wxid") {
		t.Errorf("stderr carries the conversation key: %q", got.stderr)
	}
	if _, err := os.Stat(h.out); !os.IsNotExist(err) {
		t.Error("a report was written while another job held the slot")
	}
	var rows int
	if err := openDB(t, h.db).DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM analysis_jobs`).Scan(&rows); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if rows != 1 {
		t.Errorf("analysis_jobs holds %d rows, want the single job that was already there (id %d)", rows, job.ID)
	}
}

// TestRun_CreatesNoJobWhenSentimentIsSkipped keeps the job table meaning "a long
// analysis actually ran". A behavioral-only run finishes in seconds and has
// nothing to resume.
func TestRun_CreatesNoJobWhenSentimentIsSkipped(t *testing.T) {
	h := newHarness(t)
	if got := invoke(t, h.args("--skip-sentiment")...); got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitOK, got.stderr)
	}
	var rows int
	if err := openDB(t, h.db).DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM analysis_jobs`).Scan(&rows); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if rows != 0 {
		t.Errorf("analysis_jobs holds %d rows after a behavioral-only run, want 0", rows)
	}
}

// TestRun_MarksAKilledRunAsInterruptedBeforeStartingAgain is the restart path as
// the user experiences it: the previous run died holding the slot, and the next
// invocation says so on stderr and gets on with the work instead of colliding
// with a job nobody owns.
func TestRun_MarksAKilledRunAsInterruptedBeforeStartingAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if out := invoke(t, h.args("--skip-sentiment")...); out.code != exitOK {
		t.Fatalf("behavioral run failed: %s", out.stderr)
	}

	// Leave behind exactly what a killed scoring pass leaves behind.
	stale := openDB(t, h.db)
	killed, err := stale.InsertJob(ctx, "wxid_peer", "sentiment", "lexicon-hownet-v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := stale.TransitionJob(ctx, killed.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	srv := fakeNLPService(t, "lexicon-hownet-v1")
	got := invoke(t, h.args("--nlp-url", srv.URL)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stderr, "marked 1 interrupted job") {
		t.Errorf("stderr does not report the recovery: %q", got.stderr)
	}
	if strings.Contains(got.stderr, "wxid") {
		t.Errorf("stderr carries the conversation key: %q", got.stderr)
	}

	after := openDB(t, h.db)
	var staleStatus string
	if err := after.DB().QueryRowContext(ctx,
		`SELECT status FROM analysis_jobs WHERE id = ?`, killed.ID).Scan(&staleStatus); err != nil {
		t.Fatalf("read the interrupted job: %v", err)
	}
	if staleStatus != "interrupted" {
		t.Errorf("the killed job's status = %q, want %q", staleStatus, "interrupted")
	}
	var freshStatus string
	if err := after.DB().QueryRowContext(ctx,
		`SELECT status FROM analysis_jobs WHERE kind = 'sentiment' AND id <> ?`, killed.ID).Scan(&freshStatus); err != nil {
		t.Fatalf("read the job this run created: %v", err)
	}
	if freshStatus != "done" {
		t.Errorf("this run's job status = %q, want %q", freshStatus, "done")
	}
}

func TestDefaultDBPath_IsUnderLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "test", "AppData", "Local"))
	want := filepath.Join("C:", "Users", "test", "AppData", "Local", "lover-instance", "data.db")
	if got := defaultDBPath(); got != want {
		t.Errorf("defaultDBPath() = %q, want %q", got, want)
	}
}

// TestDefaultDBPath_IsNotSynced is the reason the default is LOCALAPPDATA at
// all, so it is asserted rather than assumed.
func TestDefaultDBPath_IsNotSynced(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "test", "AppData", "Local"))
	if err := store.RejectSyncedPath(defaultDBPath()); err != nil {
		t.Errorf("the default database path is rejected by the sync guard: %v", err)
	}
}
