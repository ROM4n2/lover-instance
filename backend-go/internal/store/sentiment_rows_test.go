package store

import (
	"context"
	"testing"
	"time"
)

const testModelVersion = "lexicon-hownet-v1"

// putTyped stores a message with an explicit type so a test can place
// non-analyzable messages alongside text.
func putTyped(t *testing.T, s *Store, key string, tsMs int64, isSelf bool, msgType int, content string) {
	t.Helper()
	_, err := s.db.Exec(`
INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper', ?, ?, 'c', ?, ?, ?, 0, ?, 'hash')`,
		key, tsMs, speaker(isSelf), boolToInt(isSelf), msgType, content)
	if err != nil {
		t.Fatalf("insert typed message %s: %v", key, err)
	}
}

func TestPendingMessages_SkipsNonTextAndAlreadyScored(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	putTyped(t, s, "text1", base, true, AnalyzableMsgType, "hi")
	putTyped(t, s, "sticker", base+1000, true, 47, "[捂脸]")
	putTyped(t, s, "system", base+2000, false, 10000, "对方撤回了一条消息")
	putTyped(t, s, "text2", base+3000, false, AnalyzableMsgType, "hello")

	pending, err := s.PendingMessages(ctx, "c", testModelVersion, 10)
	if err != nil {
		t.Fatalf("PendingMessages() error: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d messages, want 2 (only the text messages)", len(pending))
	}
	for _, msg := range pending {
		if msg.MsgType != AnalyzableMsgType {
			t.Errorf("pending message %d has type %d, want text only", msg.ID, msg.MsgType)
		}
	}

	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: pending[0].ID, Polarity: 0.5, Emotion: "乐", Intensity: 0.4}}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	after, err := s.PendingMessages(ctx, "c", testModelVersion, 10)
	if err != nil {
		t.Fatalf("PendingMessages() error: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("pending after scoring one = %d, want 1: a scored message must drop out", len(after))
	}
}

func TestPendingMessages_RejectsANonPositiveLimit(t *testing.T) {
	if _, err := migratedStore(t).PendingMessages(context.Background(), "c", testModelVersion, 0); err == nil {
		t.Error("PendingMessages(limit=0) = nil error, want an error")
	}
}

// TestSaveScoredBlock_AccumulatesTokenCounts pins the accumulation semantics
// the resume path depends on: counts are added, never recomputed, so the
// anti-join must be the only thing deciding which blocks contribute.
func TestSaveScoredBlock_AccumulatesTokenCounts(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "你好")

	block := []SentimentRow{{MsgID: 1, Polarity: 0.2, Emotion: "好", Intensity: 0.3}}
	self := map[string]int{"你好": 1, "[捂脸]": 2}
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, block, self, nil); err != nil {
		t.Fatalf("first SaveScoredBlock() error: %v", err)
	}
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, block, self, nil); err != nil {
		t.Fatalf("second SaveScoredBlock() error: %v", err)
	}

	counts, err := s.WordFrequencies(ctx, "c", AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies() error: %v", err)
	}
	if counts["你好"] != 2 || counts["[捂脸]"] != 4 {
		t.Errorf("counts = %v, want 你好:2 and [捂脸]:4 after two accumulations", counts)
	}
	partner, err := s.WordFrequencies(ctx, "c", AllPeriod, false, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(partner) error: %v", err)
	}
	if len(partner) != 0 {
		t.Errorf("partner counts = %v, want empty", partner)
	}
}

func TestSentimentCoverage_ReportsScoredAgainstAnalyzable(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	putTyped(t, s, "t1", base, true, AnalyzableMsgType, "a")
	putTyped(t, s, "t2", base+1000, false, AnalyzableMsgType, "b")
	putTyped(t, s, "t3", base+2000, true, AnalyzableMsgType, "c")
	putTyped(t, s, "img", base+3000, false, 3, "")

	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: 1, Polarity: 0.1, Emotion: "好"}}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	coverage, err := s.SentimentCoverage(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("SentimentCoverage() error: %v", err)
	}
	if coverage.Scored != 1 || coverage.Total != 3 {
		t.Errorf("coverage = %+v, want {Scored:1 Total:3}: an image is not a scored text message", coverage)
	}
}

func TestExcludedTypeCounts_ReportsWhatWasLeftOut(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	putTyped(t, s, "t1", base, true, AnalyzableMsgType, "a")
	putTyped(t, s, "img1", base+1000, false, 3, "")
	putTyped(t, s, "img2", base+2000, true, 3, "")
	putTyped(t, s, "sys", base+3000, false, 10000, "对方撤回了一条消息")

	counts, err := s.ExcludedTypeCounts(ctx, "c")
	if err != nil {
		t.Fatalf("ExcludedTypeCounts() error: %v", err)
	}
	got := map[int]int{}
	for _, entry := range counts {
		got[entry.MsgType] = entry.Count
	}
	if len(got) != 2 || got[3] != 2 || got[10000] != 1 {
		t.Errorf("excluded counts = %v, want {3:2, 10000:1} and no text type", got)
	}
}

// TestRebuildSentimentMetrics_WritesItsOwnVersionRow documents why a day has
// two rows: the behavioral columns carry behavior-v1, these carry the model
// version, and a reader has to take counts and polarity from both.
func TestRebuildSentimentMetrics_WritesItsOwnVersionRow(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "happy")
	put(t, s, "k2", at(2026, time.August, 16, 12, 1, 0), false, "sad")

	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, []SentimentRow{
		{MsgID: 1, Polarity: 0.8, Emotion: "乐", Intensity: 0.6},
		{MsgID: 2, Polarity: -0.7, Emotion: "哀", Intensity: 0.5},
	}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	if err := s.RebuildBehaviorMetrics(ctx, "c"); err != nil {
		t.Fatalf("RebuildBehaviorMetrics() error: %v", err)
	}
	if err := s.RebuildSentimentMetrics(ctx, "c", testModelVersion); err != nil {
		t.Fatalf("RebuildSentimentMetrics() error: %v", err)
	}

	var avgPolarity float64
	var posCount, negCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT avg_polarity, pos_count, neg_count FROM daily_stats WHERE talker='c' AND model_version=?`,
		testModelVersion).Scan(&avgPolarity, &posCount, &negCount)
	if err != nil {
		t.Fatalf("read sentiment row: %v", err)
	}
	if posCount != 1 || negCount != 1 {
		t.Errorf("pos/neg = %d/%d, want 1/1", posCount, negCount)
	}

	// And the behavioral row exists separately, with its counts.
	var selfMsgs int
	err = s.db.QueryRowContext(ctx,
		`SELECT self_msgs FROM daily_stats WHERE talker='c' AND model_version=?`, BehaviorModelVersion).Scan(&selfMsgs)
	if err != nil {
		t.Fatalf("read behavior row: %v", err)
	}
	if selfMsgs != 1 {
		t.Errorf("behavior row self_msgs = %d, want 1", selfMsgs)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_stats WHERE talker='c'`).Scan(&total); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if total != 2 {
		t.Errorf("daily_stats has %d rows for one day, want 2 (one per model version)", total)
	}
}

func TestRebuildSentimentMetrics_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "happy")
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: 1, Polarity: 0.8, Emotion: "乐"}}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RebuildSentimentMetrics(ctx, "c", testModelVersion); err != nil {
			t.Fatalf("RebuildSentimentMetrics() run %d error: %v", i+1, err)
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_stats WHERE talker='c'`).Scan(&total); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if total != 1 {
		t.Errorf("daily_stats has %d rows after two rebuilds, want 1", total)
	}
}

func TestEmotionDistribution_CountsPerLabel(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "a")
	put(t, s, "k2", at(2026, time.August, 16, 12, 1, 0), false, "b")
	put(t, s, "k3", at(2026, time.August, 16, 12, 2, 0), true, "c")
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, []SentimentRow{
		{MsgID: 1, Emotion: "乐"}, {MsgID: 2, Emotion: "哀"}, {MsgID: 3, Emotion: "乐"},
	}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	distribution, err := s.EmotionDistribution(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("EmotionDistribution() error: %v", err)
	}
	if distribution["乐"] != 2 || distribution["哀"] != 1 {
		t.Errorf("distribution = %v, want 乐:2 哀:1", distribution)
	}
}
