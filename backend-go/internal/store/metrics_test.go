package store

import (
	"context"
	"testing"
	"time"

	"lover-instance/backend-go/internal/model"
)

// at returns an epoch-millisecond timestamp for a UTC instant.
func at(year int, month time.Month, day, hour, minute, second int) int64 {
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC).UnixMilli()
}

// put stores one message with an explicit timestamp so a test can place a
// message exactly where it needs it.
func put(t *testing.T, s *Store, key string, tsMs int64, isSelf bool, content string) {
	t.Helper()
	msg := model.CanonicalMessage{
		UniqueKey:   key,
		TimestampMs: tsMs,
		Talker:      "c",
		Sender:      speaker(isSelf),
		IsSelf:      isSelf,
		Type:        1,
		Content:     content,
	}
	if _, err := s.UpsertMessages(context.Background(), "chatlog-keeper", []model.CanonicalMessage{msg}); err != nil {
		t.Fatalf("UpsertMessages(%s) error: %v", key, err)
	}
}

// calendarIndex numbers a calendar date from Monday, computed independently of
// the bucket helper so the test checks the conversion, not a shared formula.
func calendarIndex(year int, month time.Month, day int) int {
	return (int(time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Weekday()) + 6) % 7
}

func nonzeroCell(t *testing.T, cells []HeatCell) HeatCell {
	t.Helper()
	var found []HeatCell
	for _, cell := range cells {
		if cell.SelfCount+cell.PartnerCount > 0 {
			found = append(found, cell)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d non-empty heat cells, want exactly 1: %+v", len(found), found)
	}
	return found[0]
}

// TestBehaviorMetrics_BucketsInBeijingTime pins the day boundary. A message at
// 23:30 UTC is 07:30 the NEXT day in CST; bucketing in UTC would file it under
// the previous day and shift every evening count by eight hours.
func TestBehaviorMetrics_BucketsInBeijingTime(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	// 2026-08-15 23:30 UTC == 2026-08-16 07:30 CST
	put(t, s, "k1", at(2026, time.August, 15, 23, 30, 0), true, "late")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if len(metrics.Daily) != 1 || metrics.Daily[0].Day != "2026-08-16" {
		t.Fatalf("Daily = %+v, want a single row for 2026-08-16", metrics.Daily)
	}
	cell := nonzeroCell(t, metrics.Heatmap)
	wantIndex := calendarIndex(2026, time.August, 16)
	if cell.Weekday != wantIndex || cell.Hour != 7 {
		t.Errorf("heat cell = weekday %d hour %d, want weekday %d hour 7", cell.Weekday, cell.Hour, wantIndex)
	}
}

// TestBehaviorMetrics_MidnightBoundaryIsTheNextDay covers the other side of the
// same boundary: 16:00 UTC is exactly midnight in CST.
func TestBehaviorMetrics_MidnightBoundaryIsTheNextDay(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	// 2026-08-15 16:00 UTC == 2026-08-16 00:00 CST
	put(t, s, "k1", at(2026, time.August, 15, 16, 0, 0), false, "midnight")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if len(metrics.Daily) != 1 || metrics.Daily[0].Day != "2026-08-16" {
		t.Fatalf("Daily = %+v, want a single row for 2026-08-16", metrics.Daily)
	}
	cell := nonzeroCell(t, metrics.Heatmap)
	if want := calendarIndex(2026, time.August, 16); cell.Weekday != want || cell.Hour != 0 {
		t.Errorf("heat cell = weekday %d hour %d, want weekday %d hour 0", cell.Weekday, cell.Hour, want)
	}
	if metrics.LateNightRatio != 0 {
		t.Errorf("LateNightRatio = %v for a midnight message, want 0", metrics.LateNightRatio)
	}
}

func TestBehaviorMetrics_HeatmapIsAlwaysTheWholeGrid(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "hi")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if len(metrics.Heatmap) != 168 {
		t.Fatalf("len(Heatmap) = %d, want 168", len(metrics.Heatmap))
	}
	for i, cell := range metrics.Heatmap {
		if cell.Weekday != i/24 || cell.Hour != i%24 {
			t.Fatalf("cell %d = weekday %d hour %d, want the grid laid out densely", i, cell.Weekday, cell.Hour)
		}
		if cell.Weekday < 0 || cell.Weekday > 6 || cell.Hour < 0 || cell.Hour > 23 {
			t.Fatalf("cell %d = weekday %d hour %d, out of range", i, cell.Weekday, cell.Hour)
		}
	}
}

func TestBehaviorMetrics_SameSecondReplyHasZeroLatency(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	ts := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", ts, true, "a")
	put(t, s, "k2", ts, false, "b")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.Reply.SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1", metrics.Reply.SampleCount)
	}
	if metrics.Reply.MedianMs != 0 || metrics.Reply.P90Ms != 0 {
		t.Errorf("latency = %d/%d, want 0/0 for a same-second reply", metrics.Reply.MedianMs, metrics.Reply.P90Ms)
	}
	if metrics.Reply.FastRatio != 1 {
		t.Errorf("FastRatio = %v, want 1", metrics.Reply.FastRatio)
	}
}

// TestBehaviorMetrics_ReplyLatencyOnlyCountsAlternation keeps two consecutive
// messages from the same side from reading as a reply.
func TestBehaviorMetrics_ReplyLatencyOnlyCountsAlternation(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "a")
	put(t, s, "k2", base+1000, true, "b") // same side: not a reply
	put(t, s, "k3", base+4000, false, "c")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.Reply.SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1: two consecutive own messages are not a reply", metrics.Reply.SampleCount)
	}
	if metrics.Reply.MedianMs != 3000 {
		t.Errorf("MedianMs = %d, want 3000 (measured from the last own message)", metrics.Reply.MedianMs)
	}
}

func TestBehaviorMetrics_SplitsConversationsOnAGap(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "a")
	put(t, s, "k2", base+4*60*1000, false, "b")  // 4 minutes after k1: same conversation
	put(t, s, "k3", base+10*60*1000, false, "c") // 6 minutes after k2: a new one, opened by the partner

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if len(metrics.Conversations) != 2 {
		t.Fatalf("len(Conversations) = %d, want 2", len(metrics.Conversations))
	}
	if metrics.Conversations[0].MsgCount != 2 {
		t.Errorf("first conversation has %d messages, want 2", metrics.Conversations[0].MsgCount)
	}
	if metrics.Conversations[1].MsgCount != 1 {
		t.Errorf("second conversation has %d messages, want 1", metrics.Conversations[1].MsgCount)
	}
	if metrics.Conversations[0].Initiator != SelfSpeaker {
		t.Errorf("first conversation initiator = %q, want %q", metrics.Conversations[0].Initiator, SelfSpeaker)
	}
	if metrics.InitiatorRatio != 0.5 {
		t.Errorf("InitiatorRatio = %v, want 0.5", metrics.InitiatorRatio)
	}
}

func TestBehaviorMetrics_AttributesTheFirstAndLastMessageOfEachDay(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 9, 0, 0)
	put(t, s, "k1", base, true, "opens") // first
	put(t, s, "k2", base+60*1000, false, "middle")
	put(t, s, "k3", base+120*1000, false, "closes") // last

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if len(metrics.Daily) != 1 {
		t.Fatalf("len(Daily) = %d, want 1", len(metrics.Daily))
	}
	day := metrics.Daily[0]
	if !day.FirstIsSelf {
		t.Error("FirstIsSelf = false, want true: the first message of the day was this user's")
	}
	if day.LastIsSelf {
		t.Error("LastIsSelf = true, want false: the last message of the day was the partner's")
	}
}

func TestBehaviorMetrics_CountsCharsPerSide(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "abcd")      // 4
	put(t, s, "k2", base+1000, true, "ef")   // 2  => self 6
	put(t, s, "k3", base+2000, false, "xyz") // 3  => partner 3

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.TurnBalanceCharRatio != 2 {
		t.Errorf("TurnBalanceCharRatio = %v, want 2", metrics.TurnBalanceCharRatio)
	}
	// Two short messages against one long one: the count ratio and the
	// character ratio answer differently, which is why the design asks for
	// both rather than letting one stand in for the other.
	if metrics.TurnBalanceMsgRatio != 2 {
		t.Errorf("TurnBalanceMsgRatio = %v, want 2", metrics.TurnBalanceMsgRatio)
	}
	if metrics.AvgSelfLen != 3 {
		t.Errorf("AvgSelfLen = %v, want 3", metrics.AvgSelfLen)
	}
	if metrics.AvgPartnerLen != 3 {
		t.Errorf("AvgPartnerLen = %v, want 3", metrics.AvgPartnerLen)
	}
}

// TestBehaviorMetrics_TurnBalanceRatiosCanDisagree is the case the report has
// to be able to show: many short messages on one side, fewer long ones on the
// other. A single "turn balance" number would have to pick one and quietly
// misdescribe the conversation.
func TestBehaviorMetrics_TurnBalanceRatiosCanDisagree(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "好")
	put(t, s, "k2", base+1000, true, "好")
	put(t, s, "k3", base+2000, true, "好")
	put(t, s, "k4", base+3000, true, "好")
	put(t, s, "k5", base+4000, false, "今天真的很忙我一直在开会所以现在才回你")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.TurnBalanceMsgRatio != 4 {
		t.Errorf("TurnBalanceMsgRatio = %v, want 4 messages to 1", metrics.TurnBalanceMsgRatio)
	}
	if metrics.TurnBalanceCharRatio >= 1 {
		t.Errorf("TurnBalanceCharRatio = %v, want under 1: fewer characters to more messages", metrics.TurnBalanceCharRatio)
	}
}

// TestBehaviorMetrics_TurnBalanceIsZeroWithoutAPartner keeps the ratios finite
// when one side is absent, so the report never divides by a zero count.
func TestBehaviorMetrics_TurnBalanceIsZeroWithoutAPartner(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "一个人说话")
	put(t, s, "k2", base+1000, true, "继续说")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.TurnBalanceCharRatio != 0 || metrics.TurnBalanceMsgRatio != 0 {
		t.Errorf("ratios = %v/%v, want 0/0 rather than an infinity",
			metrics.TurnBalanceCharRatio, metrics.TurnBalanceMsgRatio)
	}
}

func TestBehaviorMetrics_CountsLateNightMessages(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	// 2026-08-16 15:30 UTC == 23:30 CST -> late night. 12:00 UTC == 20:00 CST -> not.
	put(t, s, "k1", at(2026, time.August, 16, 15, 30, 0), true, "late")
	put(t, s, "k2", at(2026, time.August, 16, 12, 0, 0), false, "evening")

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	if metrics.LateNightRatio != 0.5 {
		t.Errorf("LateNightRatio = %v, want 0.5", metrics.LateNightRatio)
	}
}

func TestBehaviorMetrics_HistogramsMessageTypes(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "text")
	if _, err := s.db.Exec(`INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1) VALUES ('chatlog-keeper','sticker',?, 'c','wxid_peer',0,47,0,'[捂脸]','x')`, at(2026, time.August, 16, 12, 1, 0)); err != nil {
		t.Fatalf("insert sticker: %v", err)
	}

	metrics, err := s.BehaviorMetrics(ctx, "c")
	if err != nil {
		t.Fatalf("BehaviorMetrics() error: %v", err)
	}
	got := map[int]int{}
	for _, entry := range metrics.Types {
		got[entry.MsgType] = entry.Count
	}
	if got[1] != 1 || got[47] != 1 {
		t.Errorf("Types = %v, want one text and one sticker", got)
	}
}

// TestRebuildBehaviorMetrics_IsIdempotent guards against the counts doubling on
// a second import or a second rebuild, which is what an append-style write
// would do.
func TestRebuildBehaviorMetrics_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	base := at(2026, time.August, 16, 12, 0, 0)
	put(t, s, "k1", base, true, "a")
	put(t, s, "k2", base+6*60*1000, false, "b")

	for i := 0; i < 2; i++ {
		if err := s.RebuildBehaviorMetrics(ctx, "c"); err != nil {
			t.Fatalf("RebuildBehaviorMetrics() run %d error: %v", i+1, err)
		}
	}

	for table, want := range map[string]int{"daily_stats": 1, "activity_heatmap": 168, "conversations": 2} {
		var got int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE talker = 'c'`).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s has %d rows after two rebuilds, want %d", table, got, want)
		}
	}
}

// TestRebuildBehaviorMetrics_WritesTheBehaviorVersionRow pins the version label
// the report layer depends on: the behavioral columns live in a row labelled
// behavior-v1, and the needed columns are actually populated.
func TestRebuildBehaviorMetrics_WritesTheBehaviorVersionRow(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "a")

	if err := s.RebuildBehaviorMetrics(ctx, "c"); err != nil {
		t.Fatalf("RebuildBehaviorMetrics() error: %v", err)
	}
	var selfMsgs int
	var firstIsSelf int
	err := s.db.QueryRowContext(ctx,
		`SELECT self_msgs, first_is_self FROM daily_stats WHERE talker='c' AND model_version=?`, BehaviorModelVersion).Scan(&selfMsgs, &firstIsSelf)
	if err != nil {
		t.Fatalf("read behavior row: %v", err)
	}
	if selfMsgs != 1 || firstIsSelf != 1 {
		t.Errorf("behavior row = self_msgs %d first_is_self %d, want 1 and 1", selfMsgs, firstIsSelf)
	}
}

func TestPercentile_NearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if got := percentile(sorted, 0.5); got != 60 {
		t.Errorf("median = %d, want 60 (nearest rank, no interpolation)", got)
	}
	if got := percentile(sorted, 0.9); got != 100 {
		t.Errorf("p90 = %d, want 100", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil) = %d, want 0", got)
	}
}
