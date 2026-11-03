package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/report"
	"lover-instance/backend-go/internal/store"
)

// testBaseMs is an arbitrary moment the seeded messages share. Nothing in these
// tests reads it; it only has to be a plausible timestamp inside one day bucket.
const testBaseMs = int64(1792166000000)

// healthyNLP is the /health body every test starts from: a ready service with
// one version to score with, so the report can name its model.
const healthyNLP = `{"status":"ready","model_loaded":true,"model_version":"lexicon-v1","available_versions":["lexicon-v1"]}`

// do issues one request against the engine, with an optional JSON body.
func do(t *testing.T, engine *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// seedText stores one analyzable text message and returns its id.
func seedText(t *testing.T, s *store.Store, key, talker string, tsMs int64, isSelf bool, content string) int64 {
	t.Helper()
	result, err := s.DB().Exec(`
INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper', ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		key, tsMs, talker, senderOf(isSelf), boolAsInt(isSelf), store.AnalyzableMsgType, content, "sha-"+key)
	if err != nil {
		t.Fatalf("seed message %s: %v", key, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read the id of seeded message %s: %v", key, err)
	}
	return id
}

func senderOf(isSelf bool) string {
	if isSelf {
		return "wxid_self"
	}
	return "wxid_peer"
}

func boolAsInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// decodeErrorBody decodes the one error shape this API writes.
func decodeErrorBody(t *testing.T, body []byte) errorBody {
	t.Helper()
	var envelope errorBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return envelope
}

// hexFingerprint matches report.TalkerHash's output exactly: sixteen lowercase
// hex characters.
var hexFingerprint = regexp.MustCompile(`^[0-9a-f]{16}$`)

// TestOverview_UsesTheOnlyConversationAndRefusesToGuessBetweenTwo covers both
// halves of the talker rule in one test because they are one decision: silence
// means "the one you have", and it must stop meaning that the moment there is a
// second one.
func TestOverview_UsesTheOnlyConversationAndRefusesToGuessBetweenTwo(t *testing.T) {
	s := testStore(t)
	engine := New(Options{Store: s, NLPURL: fakeNLP(t, healthyNLP).URL, Version: "test", Log: io.Discard})

	const alice = "wxid_alice_secret"
	const bob = "wxid_bob_secret"
	seedText(t, s, "k1", alice, testBaseMs, true, "你好")

	recorder := do(t, engine, http.MethodGet, "/api/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/overview = %d, want 200: a database with one conversation answers itself, %s", recorder.Code, recorder.Body)
	}
	var view overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode overview body: %v", err)
	}
	if want := report.TalkerHash(alice); view.TalkerHash != want {
		t.Errorf("talker_hash = %q, want %q", view.TalkerHash, want)
	}
	if view.View.Meta.TalkerHash != view.TalkerHash {
		t.Errorf("view.Meta.TalkerHash = %q, want %q: the top-level field is the view's own, not a second derivation",
			view.View.Meta.TalkerHash, view.TalkerHash)
	}

	// A second conversation with fewer messages: silence defaults to the main
	// conversation (alice) rather than throwing an error requiring fingerprints.
	seedText(t, s, "k2", bob, testBaseMs+1000, false, "在")

	recorder = do(t, engine, http.MethodGet, "/api/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/overview with two conversations = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var view2 overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &view2); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if view2.TalkerHash != report.TalkerHash(alice) {
		t.Errorf("expected default conversation to be alice, got %s", view2.TalkerHash)
	}

	// Verify GET /api/conversations returns both
	recorder = do(t, engine, http.MethodGet, "/api/conversations", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/conversations = %d, want 200", recorder.Code)
	}
}

// TestOverview_NeverPutsTheTalkerInTheBody is the privacy guard for this
// endpoint. The aggregate view is meant to be safe to show and to share; a wxid
// anywhere in it would undo that, and the view is assembled by a function
// (report.Collect) that this endpoint does not control the insides of — which
// is exactly why the assertion is on the serialized body rather than on a
// field.
func TestOverview_NeverPutsTheTalkerInTheBody(t *testing.T) {
	s := testStore(t)
	engine := New(Options{Store: s, NLPURL: fakeNLP(t, healthyNLP).URL, Version: "test", Log: io.Discard})

	const talker = "wxid_secret_talker"
	seedText(t, s, "k1", talker, testBaseMs, true, "你好")
	seedText(t, s, "k2", talker, testBaseMs+1000, false, "今天也不错")

	recorder := do(t, engine, http.MethodGet, "/api/overview?talker="+talker, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/overview = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	body := recorder.Body.String()
	if strings.Contains(body, talker) {
		t.Errorf("body = %s, want no raw talker anywhere: the response names the conversation by fingerprint only", body)
	}
	if !strings.Contains(body, report.TalkerHash(talker)) {
		t.Errorf("body = %s, want the fingerprint %q", body, report.TalkerHash(talker))
	}
}

// TestOverview_ReportsAnActiveRunAsAbsoluteCounts is the half-finished gate.
//
// A dashboard that drew the sentiment curve while a run was in progress would
// draw a complete-looking line over a partially scored conversation. The
// analysis block is what lets the client refuse to; it carries the row's own
// numbers so the placeholder can say "1200 / 22093" rather than a percentage
// that walks backwards when messages are imported.
func TestOverview_ReportsAnActiveRunAsAbsoluteCounts(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	engine := New(Options{Store: s, NLPURL: fakeNLP(t, healthyNLP).URL, Version: "test", Log: io.Discard})

	const talker = "wxid_talker"
	seedText(t, s, "k1", talker, testBaseMs, true, "你好")

	recorder := do(t, engine, http.MethodGet, "/api/overview", "")
	var idle overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &idle); err != nil {
		t.Fatalf("decode idle overview: %v", err)
	}
	if idle.Analysis.Running || idle.Analysis.Done != 0 || idle.Analysis.Total != 0 {
		t.Errorf("analysis = %+v, want zero values when nothing is running", idle.Analysis)
	}

	job, err := s.InsertJob(ctx, talker, jobKindSentiment, "lexicon-v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{jobs.StatusQueued}, jobs.StatusRunning, ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if err := s.SetJobProgress(ctx, job.ID, 1200, 22093); err != nil {
		t.Fatalf("SetJobProgress() error: %v", err)
	}

	recorder = do(t, engine, http.MethodGet, "/api/overview", "")
	var running overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &running); err != nil {
		t.Fatalf("decode running overview: %v", err)
	}
	if !running.Analysis.Running {
		t.Error("analysis.running = false, want true: a running job holds this conversation's slot")
	}
	if running.Analysis.Done != 1200 || running.Analysis.Total != 22093 {
		t.Errorf("analysis done/total = %d/%d, want 1200/22093: the row's absolute counts", running.Analysis.Done, running.Analysis.Total)
	}
	if strings.Contains(recorder.Body.String(), talker) {
		t.Errorf("body = %s, want no raw talker", recorder.Body)
	}
}

func TestOverview_SucceedsWhenNLPUnreachableAndAlreadyScored(t *testing.T) {
	s := testStore(t)
	engine := New(Options{Store: s, NLPURL: "http://127.0.0.1:1", Version: "test", Log: io.Discard})

	const talker = "wxid_scored"
	seedText(t, s, "k1", talker, testBaseMs, true, "hello")
	_ = s.SaveScoredBlock(context.Background(), talker, "lexicon-v1", store.AllPeriod, []store.SentimentRow{
		{MsgID: 1, Polarity: 0.5, Emotion: "好", Intensity: 0.5},
	}, nil, nil, nil)

	recorder := do(t, engine, http.MethodGet, "/api/overview", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when NLP unreachable with stored scores, got %d: %s", recorder.Code, recorder.Body)
	}
}
