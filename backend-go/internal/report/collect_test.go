package report

import (
	"context"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lover-instance/backend-go/internal/model"
	"lover-instance/backend-go/internal/store"
)

const (
	testModelVersion = "lexicon-hownet-v1"
	testTalker       = "wxid_peer_b"
)

func newStore(t *testing.T) *store.Store {
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

// seedConversation stores a small conversation and scores it, so Collect has
// both behavioral and sentiment input to work from.
func seedConversation(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC).UnixMilli()

	messages := []model.CanonicalMessage{
		{UniqueKey: "k1", TimestampMs: base, Talker: testTalker, Sender: "wxid_self", IsSelf: true, Type: 1, Content: "今天特别开心"},
		{UniqueKey: "k2", TimestampMs: base + 30_000, Talker: testTalker, Sender: "wxid_peer", IsSelf: false, Type: 1, Content: "我也开心"},
		{UniqueKey: "k3", TimestampMs: base + 120_000, Talker: testTalker, Sender: "wxid_self", IsSelf: true, Type: 1, Content: "好难过"},
		{UniqueKey: "k4", TimestampMs: base + 180_000, Talker: testTalker, Sender: "wxid_peer", IsSelf: false, Type: 1, Content: "别难过"},
	}
	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", messages); err != nil {
		t.Fatalf("UpsertMessages() error: %v", err)
	}
	// A non-text message, so the exclusion list has something to report.
	if _, err := s.DB().Exec(`INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper','img',?, ?, 'wxid_peer', 0, 3, 0, '', 'h')`, base+240_000, testTalker); err != nil {
		t.Fatalf("insert image message: %v", err)
	}

	rows := []store.SentimentRow{
		{MsgID: 1, Polarity: 0.8, Emotion: "乐", Intensity: 0.6},
		{MsgID: 2, Polarity: 0.7, Emotion: "乐", Intensity: 0.5},
		{MsgID: 3, Polarity: -0.6, Emotion: "哀", Intensity: 0.5},
		{MsgID: 4, Polarity: 0.0, Emotion: "neutral", Intensity: 0.2},
	}
	self := map[string]int{"今天": 1, "特别": 1, "开心": 1, "[捂脸]": 2, "难过": 1}
	partner := map[string]int{"开心": 1, "难过": 1, "[旺柴]": 3}
	if err := s.SaveScoredBlock(ctx, testTalker, testModelVersion, store.AllPeriod, rows, self, partner); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}
	if err := s.RebuildBehaviorMetrics(ctx, testTalker); err != nil {
		t.Fatalf("RebuildBehaviorMetrics() error: %v", err)
	}
	if err := s.RebuildSentimentMetrics(ctx, testTalker, testModelVersion); err != nil {
		t.Fatalf("RebuildSentimentMetrics() error: %v", err)
	}
}

func collect(t *testing.T, s *store.Store) View {
	t.Helper()
	view, err := Collect(context.Background(), s, testTalker, store.BehaviorModelVersion, testModelVersion)
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	return view
}

func TestCollect_ProducesAPopulatedView(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	// Behavioral totals cover every message, text or not: the image counts as a
	// partner message even though the sentiment pass skips it. Coverage is where
	// the exclusion is reported, not here.
	if view.Summary.SelfMsgs != 2 || view.Summary.PartnerMsgs != 3 {
		t.Errorf("message split = %d/%d, want 2/3 (the image is a partner message too)", view.Summary.SelfMsgs, view.Summary.PartnerMsgs)
	}
	if view.Summary.DayCount != 1 {
		t.Errorf("DayCount = %d, want 1", view.Summary.DayCount)
	}
	if len(view.Heatmap) != 168 {
		t.Errorf("len(Heatmap) = %d, want 168", len(view.Heatmap))
	}
	if view.Meta.RangeStartMs == 0 || view.Meta.RangeEndMs <= view.Meta.RangeStartMs {
		t.Errorf("range = %d..%d, want a populated, ordered span", view.Meta.RangeStartMs, view.Meta.RangeEndMs)
	}
	if view.Meta.TalkerHash == testTalker || view.Meta.TalkerHash == "" {
		t.Errorf("TalkerHash = %q, want a hash rather than the conversation key", view.Meta.TalkerHash)
	}
}

// TestCollect_WordCloudsArePopulated is the guard against the defect the plan
// review found: with no producer for word_freq the clouds render empty and
// every test still passes because they only asserted a container existed.
func TestCollect_WordCloudsArePopulated(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	if len(view.Words.Self) == 0 {
		t.Fatal("Words.Self is empty: the word cloud would render blank")
	}
	if len(view.Words.Partner) == 0 {
		t.Fatal("Words.Partner is empty: the word cloud would render blank")
	}
	if len(view.Words.Self) > TopWords || len(view.Words.Partner) > TopWords {
		t.Errorf("clouds have %d/%d entries, want at most %d", len(view.Words.Self), len(view.Words.Partner), TopWords)
	}
	for _, entry := range append(append([]WordCount{}, view.Words.Self...), view.Words.Partner...) {
		if emojiToken.MatchString(entry.Word) {
			t.Errorf("word cloud contains the expression token %q: expressions belong in EmojiView", entry.Word)
		}
	}
	// Ranking must be by count, descending.
	for i := 1; i < len(view.Words.Self); i++ {
		if view.Words.Self[i-1].Count < view.Words.Self[i].Count {
			t.Fatalf("Words.Self is not ranked by count: %+v", view.Words.Self)
		}
	}
}

func TestCollect_EmojiViewHoldsOnlyExpressionTokens(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	if len(view.Emoji.Self) == 0 || len(view.Emoji.Partner) == 0 {
		t.Fatalf("emoji tally is empty: self=%v partner=%v", view.Emoji.Self, view.Emoji.Partner)
	}
	for _, entry := range append(append([]WordCount{}, view.Emoji.Self...), view.Emoji.Partner...) {
		if !emojiToken.MatchString(entry.Word) {
			t.Errorf("EmojiView contains the plain word %q", entry.Word)
		}
	}
	if view.Emoji.Self[0].Word != "[捂脸]" || view.Emoji.Self[0].Count != 2 {
		t.Errorf("Emoji.Self = %+v, want [捂脸] with count 2 first", view.Emoji.Self)
	}
}

func TestCollect_EmotionViewIsAlignedToOneOrder(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	if len(view.Emotion.Radar) != len(Emotions) {
		t.Fatalf("len(Radar) = %d, want %d", len(view.Emotion.Radar), len(Emotions))
	}
	total := 0
	for label, count := range view.Emotion.Counts {
		total += count
		if label == NeutralLabel {
			continue
		}
		if !contains(Emotions, label) {
			t.Errorf("Counts has the unexpected label %q", label)
		}
	}
	if total != view.Emotion.ScoredTotal {
		t.Errorf("counts sum to %d but ScoredTotal is %d", total, view.Emotion.ScoredTotal)
	}
	sum := 0.0
	for _, share := range view.Emotion.Shares {
		sum += share
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("shares sum to %v, want 1", sum)
	}
	// The synthetic data is two positive, one negative, one neutral. The neutral
	// message is excluded from the ratio rather than counted as agreement.
	if math.Abs(view.Emotion.PositiveRatio-2.0/3.0) > 1e-9 {
		t.Errorf("PositiveRatio = %v, want 2/3", view.Emotion.PositiveRatio)
	}
	if view.Emotion.NeutralCount != 1 {
		t.Errorf("NeutralCount = %d, want 1", view.Emotion.NeutralCount)
	}
	if view.Emotion.Radar[emotionIndex("乐")] <= 0 {
		t.Errorf("Radar shows no 乐 share: %+v", view.Emotion.Radar)
	}
	// Neutral is deliberately not a spoke on the radar: it is the absence of a
	// matched emotion word, not an emotional state.
	if got := len(view.Emotion.Radar); got != len(Emotions) {
		t.Errorf("len(Radar) = %d, want %d so neutral cannot appear as an eighth spoke", got, len(Emotions))
	}
}

func TestCollect_CoverageReportsTheExcludedTypes(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	if view.Coverage.Total != 4 {
		t.Errorf("Coverage.Total = %d, want 4 analyzable messages", view.Coverage.Total)
	}
	if view.Coverage.Scored != 4 || view.Coverage.Partial {
		t.Errorf("Coverage = %+v, want a complete pass", view.Coverage)
	}
	if len(view.Coverage.ExcludedTypes) != 1 || view.Coverage.ExcludedTypes[0].MsgType != 3 {
		t.Errorf("ExcludedTypes = %+v, want the single image reported", view.Coverage.ExcludedTypes)
	}
}

func TestCollect_MarksAPartialCoverage(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	// Score nothing for this run's model version, so coverage is 0 of 4.
	view, err := Collect(context.Background(), s, testTalker, store.BehaviorModelVersion, "some-other-model")
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	if !view.Coverage.Partial {
		t.Error("Coverage.Partial = false, want true when nothing has been scored")
	}
	if view.Coverage.Scored != 0 || view.Coverage.Total != 4 {
		t.Errorf("Coverage = %+v, want {Scored:0 Total:4}", view.Coverage)
	}
}

func TestCollect_DisclaimerIsFixedAndHonest(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	if view.Disclaimer == "" {
		t.Fatal("Disclaimer is empty")
	}
	if view.Disclaimer != DisclaimerText {
		t.Error("Disclaimer was not the fixed constant")
	}
	for _, want := range []string{"非关系诊断", "误判", "征得"} {
		if !strings.Contains(view.Disclaimer, want) {
			t.Errorf("Disclaimer is missing %q: %q", want, view.Disclaimer)
		}
	}
}

// TestView_CarriesNoPerMessageData walks the whole payload by reflection. The
// report file is meant to be sent to someone else, so a field carrying message
// bodies would put the conversation itself in their hands.
func TestView_CarriesNoPerMessageData(t *testing.T) {
	forbidden := map[string]bool{
		"Content": true, "Messages": true, "Raw": true, "RawJSON": true,
		"Body": true, "Text": true, "Message": true, "Bodies": true,
	}
	names := map[string]bool{}
	collectFieldNames(reflect.TypeOf(View{}), names, map[reflect.Type]bool{})

	for name := range forbidden {
		if names[name] {
			t.Errorf("View carries a field named %q; the report must hold aggregates only", name)
		}
	}
	// Sanity: the walker actually walked something.
	if len(names) < 20 {
		t.Fatalf("the reflection walk found only %d field names, so it is not walking the payload", len(names))
	}
}

func collectFieldNames(t reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	switch t.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
		collectFieldNames(t.Elem(), out, seen)
		return
	case reflect.Struct:
	default:
		return
	}
	if seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		out[field.Name] = true
		collectFieldNames(field.Type, out, seen)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func emotionIndex(label string) int {
	for i, value := range Emotions {
		if value == label {
			return i
		}
	}
	return -1
}

func TestTalkerHash_IsStableAndNonReversible(t *testing.T) {
	if TalkerHash(testTalker) != TalkerHash(testTalker) {
		t.Error("TalkerHash is not stable across calls")
	}
	if TalkerHash("a") == TalkerHash("b") {
		t.Error("TalkerHash collides for different conversations")
	}
	if strings.Contains(TalkerHash(testTalker), testTalker) {
		t.Error("TalkerHash contains the original key")
	}
}
