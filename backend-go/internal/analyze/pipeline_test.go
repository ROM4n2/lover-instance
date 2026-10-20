package analyze

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/store"
)

const testModelVersion = "lexicon-hownet-v1"

// scoreServer is a stand-in for the NLP service. failFromBlock makes the nth
// block onward fail, so the checkpoint behaviour can be exercised.
type scoreServer struct {
	requests atomic.Int32
	items    atomic.Int32
	failFrom int32 // when > 0, requests after this many succeed and then fail
}

func (s *scoreServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		attempt := s.requests.Add(1)
		if s.failFrom > 0 && attempt >= s.failFrom {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request struct {
			ModelVersion string `json:"model_version"`
			Items        []struct {
				MsgID   int64  `json:"msg_id"`
				Content string `json:"content"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.items.Add(int32(len(request.Items)))
		results := make([]map[string]any, 0, len(request.Items))
		for _, item := range request.Items {
			results = append(results, map[string]any{
				"msg_id": item.MsgID, "polarity": 0.4, "emotion": "乐",
				"intensity": 0.3, "tokens": []string{"你好", "[捂脸]"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_version": request.ModelVersion, "results": results,
		})
	}
}

func newAnalyzeStore(t *testing.T) *store.Store {
	t.Helper()
	dsn, err := store.DSN(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("store.DSN() error: %v", err)
	}
	s, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return s
}

func insertMessage(t *testing.T, s *store.Store, key string, offsetMs int64, isSelf bool, msgType int, content string) {
	t.Helper()
	_, err := s.DB().Exec(`
INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper', ?, ?, 'c', 'wxid_sender', ?, ?, 0, ?, 'hash')`,
		key, 1792166000000+offsetMs, isSelf, msgType, content)
	if err != nil {
		t.Fatalf("insert message %s: %v", key, err)
	}
}

func newClient(t *testing.T, srv *httptest.Server) *nlp.Client {
	t.Helper()
	c, err := nlp.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("nlp.NewClient() error: %v", err)
	}
	return c
}

func TestRunSentiment_ScoresEveryAnalyzableMessage(t *testing.T) {
	ctx := context.Background()
	s := newAnalyzeStore(t)
	insertMessage(t, s, "t1", 0, true, store.AnalyzableMsgType, "你好")
	insertMessage(t, s, "t2", 1000, false, store.AnalyzableMsgType, "在吗")
	insertMessage(t, s, "img", 2000, true, 3, "")
	insertMessage(t, s, "sys", 3000, false, 10000, "对方撤回了一条消息")

	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	progress, err := RunSentiment(ctx, s, newClient(t, srv), "c", testModelVersion, 1)
	if err != nil {
		t.Fatalf("RunSentiment() error: %v", err)
	}
	if progress.Scored != 2 || progress.Total != 2 {
		t.Errorf("progress = %+v, want {Scored:2 Total:2}: only the text messages are analyzable", progress)
	}
	coverage, err := s.SentimentCoverage(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("SentimentCoverage() error: %v", err)
	}
	if coverage.Scored != 2 || coverage.Total != 2 {
		t.Errorf("coverage = %+v, want {Scored:2 Total:2}", coverage)
	}

	self, err := s.WordFrequencies(ctx, "c", store.AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies() error: %v", err)
	}
	partner, err := s.WordFrequencies(ctx, "c", store.AllPeriod, false, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(partner) error: %v", err)
	}
	if len(self) == 0 || len(partner) == 0 {
		t.Errorf("word frequencies self=%v partner=%v, want both sides populated", self, partner)
	}
	if self["你好"] != 1 || partner["你好"] != 1 {
		t.Errorf("token split wrong: self=%v partner=%v", self, partner)
	}
}

// TestRunSentiment_TwoVersionsKeepTheirOwnWordCounts is the end-to-end form of
// the defect this migration exists to remove. D5: the word freq rows had no
// model version and `addTokenCounts` added unconditionally, so rescanning the
// same conversation under the next model version added every token a second
// time and the word cloud doubled. Here both passes run over the same two
// messages and each version must hold exactly its own counts.
func TestRunSentiment_TwoVersionsKeepTheirOwnWordCounts(t *testing.T) {
	const otherVersion = "onnx-erlangshen-v1"

	ctx := context.Background()
	s := newAnalyzeStore(t)
	insertMessage(t, s, "t1", 0, true, store.AnalyzableMsgType, "你好")
	insertMessage(t, s, "t2", 1000, true, store.AnalyzableMsgType, "在吗")

	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()
	client := newClient(t, srv)

	if _, err := RunSentiment(ctx, s, client, "c", testModelVersion, 2); err != nil {
		t.Fatalf("RunSentiment(%s) error: %v", testModelVersion, err)
	}
	if _, err := RunSentiment(ctx, s, client, "c", otherVersion, 2); err != nil {
		t.Fatalf("RunSentiment(%s) error: %v", otherVersion, err)
	}

	// Two self messages, each contributing 你好 and [捂脸] once.
	first, err := s.WordFrequencies(ctx, "c", store.AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(%s) error: %v", testModelVersion, err)
	}
	second, err := s.WordFrequencies(ctx, "c", store.AllPeriod, true, otherVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(%s) error: %v", otherVersion, err)
	}
	for version, counts := range map[string]map[string]int{testModelVersion: first, otherVersion: second} {
		if counts["你好"] != 2 || counts["[捂脸]"] != 2 {
			t.Errorf("%s counts = %v, want 你好:2 [捂脸]:2 — the other version's rescan doubled them", version, counts)
		}
	}
}

// TestRunSentiment_SecondRunDoesNoWork is the resumability check: the
// anti-join must make a finished run a no-op, so restarting a long job costs
// nothing and re-running cannot double the accumulated token counts.
func TestRunSentiment_SecondRunDoesNoWork(t *testing.T) {
	ctx := context.Background()
	s := newAnalyzeStore(t)
	insertMessage(t, s, "t1", 0, true, store.AnalyzableMsgType, "你好")
	insertMessage(t, s, "t2", 1000, false, store.AnalyzableMsgType, "在吗")

	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()
	client := newClient(t, srv)

	if _, err := RunSentiment(ctx, s, client, "c", testModelVersion, 1); err != nil {
		t.Fatalf("first RunSentiment() error: %v", err)
	}
	before, _ := s.WordFrequencies(ctx, "c", store.AllPeriod, true, testModelVersion)
	requestsAfterFirst := server.requests.Load()

	progress, err := RunSentiment(ctx, s, client, "c", testModelVersion, 1)
	if err != nil {
		t.Fatalf("second RunSentiment() error: %v", err)
	}
	if got := server.requests.Load(); got != requestsAfterFirst {
		t.Errorf("second run made %d requests, want 0 more (total %d)", got-requestsAfterFirst, requestsAfterFirst)
	}
	if progress.Scored != 0 {
		t.Errorf("second run Scored = %d, want 0", progress.Scored)
	}
	after, _ := s.WordFrequencies(ctx, "c", store.AllPeriod, true, testModelVersion)
	if len(after) != len(before) {
		t.Errorf("word_freq grew on a no-op rerun: before=%v after=%v", before, after)
	}
	for word, count := range before {
		if after[word] != count {
			t.Errorf("word %q count changed from %d to %d on a no-op rerun", word, count, after[word])
		}
	}
}

// TestRunSentiment_KeepsTheCheckpointWhenALaterBlockFails is the interruption
// case: the block that succeeded is still stored, and only the failed block's
// work is missing. A run that lost the earlier block would silently redo
// everything and, worse, produce a report from a half-written state without
// saying so.
func TestRunSentiment_KeepsTheCheckpointWhenALaterBlockFails(t *testing.T) {
	ctx := context.Background()
	s := newAnalyzeStore(t)
	for i := 0; i < 4; i++ {
		insertMessage(t, s, string(rune('a'+i)), int64(i)*1000, i%2 == 0, store.AnalyzableMsgType, "你好")
	}

	// First block succeeds, everything from the second attempt on fails.
	server := &scoreServer{failFrom: 2}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	progress, err := RunSentiment(ctx, s, newClient(t, srv), "c", testModelVersion, 2)
	if err == nil {
		t.Fatal("RunSentiment() = nil error although a later block failed, want an error")
	}
	if progress.Scored != 2 {
		t.Errorf("Scored = %d, want 2 (the first block, before the failure)", progress.Scored)
	}

	coverage, err := s.SentimentCoverage(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("SentimentCoverage() error: %v", err)
	}
	if coverage.Scored != 2 {
		t.Errorf("coverage.Scored = %d, want 2: the first block's scores must survive the failure", coverage.Scored)
	}
	self, err := s.WordFrequencies(ctx, "c", store.AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies() error: %v", err)
	}
	if len(self) == 0 {
		t.Error("word_freq is empty: the first block's token counts must be committed with its scores")
	}
}

func TestRunSentiment_ResumesFromTheCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := newAnalyzeStore(t)
	for i := 0; i < 4; i++ {
		insertMessage(t, s, string(rune('a'+i)), int64(i)*1000, i%2 == 0, store.AnalyzableMsgType, "你好")
	}

	failing := &scoreServer{failFrom: 2}
	srv := httptest.NewServer(failing.handler())
	if _, err := RunSentiment(ctx, s, newClient(t, srv), "c", testModelVersion, 2); err == nil {
		t.Fatal("first RunSentiment() = nil error, want an error")
	}
	srv.Close()

	healthy := &scoreServer{}
	srv2 := httptest.NewServer(healthy.handler())
	defer srv2.Close()

	progress, err := RunSentiment(ctx, s, newClient(t, srv2), "c", testModelVersion, 2)
	if err != nil {
		t.Fatalf("resumed RunSentiment() error: %v", err)
	}
	if progress.Scored != 2 {
		t.Errorf("resumed Scored = %d, want 2: only the missing block is rescored", progress.Scored)
	}
	if got := healthy.items.Load(); got != 2 {
		t.Errorf("resumed run sent %d messages, want 2 (the unscored block only)", got)
	}
}

func TestRunSentiment_StopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newAnalyzeStore(t)
	for i := 0; i < 4; i++ {
		insertMessage(t, s, string(rune('a'+i)), int64(i)*1000, false, store.AnalyzableMsgType, "你好")
	}
	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	cancel()
	if _, err := RunSentiment(ctx, s, newClient(t, srv), "c", testModelVersion, 2); err == nil {
		t.Fatal("RunSentiment() with a cancelled context = nil error, want an error")
	}
	if got := server.requests.Load(); got != 0 {
		t.Errorf("cancelled run made %d requests, want 0", got)
	}
}

func TestRunSentiment_RejectsAnOutOfRangeBatchSize(t *testing.T) {
	s := newAnalyzeStore(t)
	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()
	client := newClient(t, srv)

	for _, size := range []int{0, -1, nlp.MaxBatchSize + 1} {
		if _, err := RunSentiment(context.Background(), s, client, "c", testModelVersion, size); err == nil {
			t.Errorf("RunSentiment(batchSize=%d) = nil error, want an error", size)
		}
	}
	if got := server.requests.Load(); got != 0 {
		t.Errorf("an invalid batch size sent %d requests, want 0", got)
	}
}

// TestRunSentimentProgress_ReportsEveryCommittedBlock is what the job heartbeat
// rides on: the callback runs once per block that actually committed, and the
// last call agrees with the returned Progress. A callback that fired before the
// commit, or missed the final block, would leave the job row describing
// progress the database does not hold.
func TestRunSentimentProgress_ReportsEveryCommittedBlock(t *testing.T) {
	ctx := context.Background()
	s := newAnalyzeStore(t)
	for i := 0; i < 5; i++ {
		insertMessage(t, s, string(rune('a'+i)), int64(i)*1000, i%2 == 0, store.AnalyzableMsgType, "你好")
	}

	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	type step struct{ scored, total int }
	var calls []step
	progress, err := RunSentimentProgress(ctx, s, newClient(t, srv), "c", testModelVersion, 2, func(scored, total int) {
		calls = append(calls, step{scored: scored, total: total})
	})
	if err != nil {
		t.Fatalf("RunSentimentProgress() error: %v", err)
	}

	// Five messages in blocks of two commit three times.
	want := []step{{2, 5}, {4, 5}, {5, 5}}
	if len(calls) != len(want) {
		t.Fatalf("callback ran %d times (%v), want %d: one call per committed block", len(calls), calls, len(want))
	}
	for i, got := range calls {
		if got != want[i] {
			t.Errorf("callback #%d = %+v, want %+v", i, got, want[i])
		}
	}
	last := calls[len(calls)-1]
	if last.scored != progress.Scored || last.total != progress.Total {
		t.Errorf("last callback = %+v but Progress = %+v: the job row and the returned counts must agree",
			last, progress)
	}
}

// TestRunSentimentProgress_DoesNotCallBackWithoutBlocks keeps "nothing to
// score" from looking like one block of progress, which would make a finished
// conversation report a scored count it does not have.
func TestRunSentimentProgress_DoesNotCallBackWithoutBlocks(t *testing.T) {
	s := newAnalyzeStore(t)
	insertMessage(t, s, "img", 0, true, 3, "")

	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	calls := 0
	progress, err := RunSentimentProgress(context.Background(), s, newClient(t, srv), "c", testModelVersion, 10, func(scored, total int) {
		calls++
	})
	if err != nil {
		t.Fatalf("RunSentimentProgress() error: %v", err)
	}
	if calls != 0 {
		t.Errorf("callback ran %d times, want 0: no block committed", calls)
	}
	if progress.Scored != 0 || progress.Total != 0 {
		t.Errorf("progress = %+v, want zeros", progress)
	}
}

func TestRunSentiment_OnAConversationWithNoTextIsNotAnError(t *testing.T) {
	s := newAnalyzeStore(t)
	insertMessage(t, s, "img", 0, true, 3, "")
	server := &scoreServer{}
	srv := httptest.NewServer(server.handler())
	defer srv.Close()

	progress, err := RunSentiment(context.Background(), s, newClient(t, srv), "c", testModelVersion, 10)
	if err != nil {
		t.Fatalf("RunSentiment() error: %v", err)
	}
	if progress.Scored != 0 || progress.Total != 0 {
		t.Errorf("progress = %+v, want zeros: there is nothing to score, which is not a failure", progress)
	}
}
