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
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
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
	if parsed.Analyzable != 4 {
		t.Errorf("Analyzable = %d, want 4 (the image is out of the sentiment pass)", parsed.Analyzable)
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
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ready", "model_loaded": false, "model_version": modelVersion,
		})
	})
	mux.HandleFunc("/score", func(w http.ResponseWriter, r *http.Request) {
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
	})
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
	if parsed.Scored != 4 || parsed.Partial {
		t.Errorf("Scored = %d, Partial = %v, want 4 and false", parsed.Scored, parsed.Partial)
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
	if parsed.Scored != 4 {
		t.Errorf("Scored = %d, want all 4 analyzable messages", parsed.Scored)
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
