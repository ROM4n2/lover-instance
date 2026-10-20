package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// trendDayMs places a message at 20:00 CST on the given August 2026 day.
//
// 12:00 UTC and 20:00 CST are the same calendar day, so this helper is
// deliberately blind to timezone mistakes: every assertion built on it passes
// under either bucketing. Timezone correctness is pinned separately, by putting
// a message on a day where UTC and UTC+8 disagree.
func trendDayMs(day int) int64 {
	return at(2026, time.August, day, 12, 0, 0)
}

// messageID reads back the id putTyped assigned to a message.
func messageID(t *testing.T, s *Store, key string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(
		`SELECT id FROM messages WHERE source = 'chatlog-keeper' AND source_msg_id = ?`, key).Scan(&id); err != nil {
		t.Fatalf("read id of message %s: %v", key, err)
	}
	return id
}

// scoreMessage writes a score row directly. A nil polarity is a score the
// model declined to put a number on, which is not the same as a score of zero
// and must not be averaged in as one.
func scoreMessage(t *testing.T, s *Store, msgID int64, modelVersion string, polarity *float64) {
	t.Helper()
	var value any
	if polarity != nil {
		value = *polarity
	}
	if _, err := s.db.Exec(`
INSERT INTO sentiment (msg_id, model_version, polarity, emotion, intensity)
VALUES (?, ?, ?, '', 0)
ON CONFLICT (msg_id, model_version) DO UPDATE SET polarity = excluded.polarity`,
		msgID, modelVersion, value); err != nil {
		t.Fatalf("score message %d: %v", msgID, err)
	}
}

// putScored stores one message and one score for it, returning the message id
// so that a second model version can be scored against the same message.
func putScored(t *testing.T, s *Store, key string, tsMs int64, modelVersion string, polarity float64) int64 {
	t.Helper()
	putTyped(t, s, key, tsMs, true, AnalyzableMsgType, key)
	id := messageID(t, s, key)
	scoreMessage(t, s, id, modelVersion, &polarity)
	return id
}

// pointOn returns the point for one day or fails, so a missing day is reported
// as the missing day rather than as an index out of range.
func pointOn(t *testing.T, points []TrendPoint, day string) TrendPoint {
	t.Helper()
	for _, point := range points {
		if point.Day == day {
			return point
		}
	}
	t.Fatalf("no trend point for %s, points = %v", day, points)
	return TrendPoint{}
}

// TestTrend_MatchesHandComputedWindow pins the arithmetic against numbers
// worked out by hand, including the population (divide by n) standard
// deviation, which is what "the spread of the messages we actually have" means
// here.
func TestTrend_MatchesHandComputedWindow(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putScored(t, s, "d10a", trendDayMs(10), testModelVersion, -0.5)
	putScored(t, s, "d10b", trendDayMs(10), testModelVersion, -0.5)
	putScored(t, s, "d12a", trendDayMs(12), testModelVersion, 0.0)
	putScored(t, s, "d16a", trendDayMs(16), testModelVersion, 0.5)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("points = %v, want 3 days (08-10, 08-12, 08-16)", points)
	}

	// The first day's window reaches back before the data starts, so it holds
	// exactly its own two identical values.
	first := points[0]
	if first.Day != "2026-08-10" || first.Count != 2 {
		t.Fatalf("first point = %+v, want 2026-08-10 holding 2 messages", first)
	}
	if math.Abs(first.Mean-(-0.5)) > 1e-9 || math.Abs(first.StdDev) > 1e-9 {
		t.Errorf("first mean/stddev = %v/%v, want -0.5/0", first.Mean, first.StdDev)
	}

	// Hand computation for the last day, whose window 08-10..08-16 holds
	// {-0.5, -0.5, 0, +0.5}: mean = -0.5/4 = -0.125,
	// E[x²] = 0.75/4, var = 0.1875 - 0.015625 = 0.171875.
	last := points[2]
	wantMean := -0.125
	wantStdDev := math.Sqrt(0.171875)
	if last.Day != "2026-08-16" || last.Count != 4 {
		t.Fatalf("last point = %+v, want 2026-08-16 holding 4 messages", last)
	}
	if math.Abs(last.Mean-wantMean) > 1e-9 {
		t.Errorf("last mean = %v, want %v", last.Mean, wantMean)
	}
	if math.Abs(last.StdDev-wantStdDev) > 1e-9 {
		t.Errorf("last stddev = %v, want %v (population SD, divided by n)", last.StdDev, wantStdDev)
	}
}

// TestTrend_WindowIsSevenCalendarDays pins the window edges: the seventh day
// back is in, the eighth is out.
func TestTrend_WindowIsSevenCalendarDays(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	// 2026-08-10 is seven days before the 17th, so it falls outside the window
	// the 17th reports on; 08-11 is the sixth day back and is inside it.
	putScored(t, s, "out", trendDayMs(10), testModelVersion, 0.9)
	putScored(t, s, "edge1", trendDayMs(11), testModelVersion, -0.2)
	putScored(t, s, "edge2", trendDayMs(11), testModelVersion, -0.4)
	putScored(t, s, "today", trendDayMs(17), testModelVersion, 0.2)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("points = %d, want 3 (08-10, 08-11, 08-17)", len(points))
	}

	last := pointOn(t, points, "2026-08-17")
	if last.Count != 3 {
		t.Errorf("count on the 17th = %d, want 3: the 10th is seven days back and out of the window", last.Count)
	}
	wantMean := (0.2 - 0.2 - 0.4) / 3
	if math.Abs(last.Mean-wantMean) > 1e-9 {
		t.Errorf("mean on the 17th = %v, want %v: the 10th's +0.9 must not reach the window", last.Mean, wantMean)
	}
}

// TestTrend_SkipsDaysWithoutDataInsteadOfFillingZero is the honesty guard: a
// day nobody wrote on contributes nothing at all. Padding it with a zero would
// drag the mean toward neutral and read as a mood dip that never happened.
func TestTrend_SkipsDaysWithoutDataInsteadOfFillingZero(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putScored(t, s, "early1", trendDayMs(7), testModelVersion, 0.6)
	putScored(t, s, "early2", trendDayMs(7), testModelVersion, 0.8)
	putScored(t, s, "late1", trendDayMs(16), testModelVersion, -0.3)
	putScored(t, s, "late2", trendDayMs(16), testModelVersion, -0.5)
	putScored(t, s, "late3", trendDayMs(16), testModelVersion, -0.1)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %d (%v), want 2: a day with no messages must not become a zero-filled point", len(points), points)
	}
	if points[0].Day != "2026-08-07" || points[1].Day != "2026-08-16" {
		t.Fatalf("days = %s, %s, want 2026-08-07 and 2026-08-16", points[0].Day, points[1].Day)
	}
	if points[0].Count != 2 || math.Abs(points[0].Mean-0.7) > 1e-9 {
		t.Errorf("first point = %+v, want 2 messages with mean 0.7", points[0])
	}

	late := points[1]
	if late.Count != 3 {
		t.Errorf("count on the 16th = %d, want 3: the 7th is nine days back and out of the window", late.Count)
	}
	wantMean := (-0.3 - 0.5 - 0.1) / 3
	if math.Abs(late.Mean-wantMean) > 1e-9 {
		t.Errorf("mean on the 16th = %v, want %v: neither a zero-filled day nor the 7th's messages belong in it", late.Mean, wantMean)
	}
}

func TestTrendPoint_DrawableNeedsThreeSamples(t *testing.T) {
	cases := []struct {
		count int
		want  bool
	}{
		{count: 0, want: false},
		{count: 1, want: false},
		{count: 2, want: false},
		{count: 3, want: true},
		{count: 4, want: true},
	}
	for _, c := range cases {
		if got := (TrendPoint{Count: c.count}).Drawable(); got != c.want {
			t.Errorf("Drawable() with %d samples = %v, want %v", c.count, got, c.want)
		}
	}
}

// TestTrend_MarksThinDaysAsNotDrawable covers the same threshold through the
// real query, so the count a point reports is the count Drawable judges.
func TestTrend_MarksThinDaysAsNotDrawable(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putScored(t, s, "thin1", trendDayMs(1), testModelVersion, 0.4)
	putScored(t, s, "thin2", trendDayMs(1), testModelVersion, -0.4)
	putScored(t, s, "full1", trendDayMs(20), testModelVersion, 0.4)
	putScored(t, s, "full2", trendDayMs(20), testModelVersion, 0.2)
	putScored(t, s, "full3", trendDayMs(20), testModelVersion, 0.0)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	thin := pointOn(t, points, "2026-08-01")
	if thin.Count != 2 {
		t.Fatalf("count on the 1st = %d, want 2", thin.Count)
	}
	if thin.Drawable() {
		t.Errorf("Drawable() with %d samples = true, want false: two messages are not a trend", thin.Count)
	}
	full := pointOn(t, points, "2026-08-20")
	if full.Count != 3 {
		t.Fatalf("count on the 20th = %d, want 3", full.Count)
	}
	if !full.Drawable() {
		t.Errorf("Drawable() with %d samples = false, want true", full.Count)
	}
}

// TestTrend_CountsOnlyTheRequestedModelVersion keeps the two versions of the
// same message apart, which is the whole point of model_version being part of a
// score's identity.
func TestTrend_CountsOnlyTheRequestedModelVersion(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	const otherVersion = "erlangshen-110m-v1"
	id := putScored(t, s, "both", trendDayMs(16), testModelVersion, 0.5)
	minus := -0.9
	scoreMessage(t, s, id, otherVersion, &minus)
	putScored(t, s, "lexiconOnly", trendDayMs(16), testModelVersion, 0.1)

	lexicon, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend(lexicon) error: %v", err)
	}
	if len(lexicon) != 1 {
		t.Fatalf("lexicon points = %d, want 1", len(lexicon))
	}
	if lexicon[0].Count != 2 || math.Abs(lexicon[0].Mean-0.3) > 1e-9 {
		t.Errorf("lexicon point = %+v, want 2 messages with mean 0.3 (the model's -0.9 excluded)", lexicon[0])
	}

	model, err := s.Trend(ctx, "c", otherVersion)
	if err != nil {
		t.Fatalf("Trend(model) error: %v", err)
	}
	if len(model) != 1 {
		t.Fatalf("model points = %d, want 1", len(model))
	}
	if model[0].Count != 1 || math.Abs(model[0].Mean-(-0.9)) > 1e-9 {
		t.Errorf("model point = %+v, want 1 message with mean -0.9 (the lexicon's scores excluded)", model[0])
	}
}

// TestTrend_BucketsDaysInBeijingTimeNotUTC pins the day boundary. 15:00 UTC is
// 23:00 CST on the same day; 16:00 UTC is midnight CST on the next one. A UTC
// bucket would put both on the 16th.
func TestTrend_BucketsDaysInBeijingTimeNotUTC(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putScored(t, s, "evening", at(2026, time.August, 16, 15, 0, 0), testModelVersion, 0.2)
	putScored(t, s, "midnight", at(2026, time.August, 16, 16, 0, 0), testModelVersion, -0.2)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %v, want two CST days (08-16 and 08-17); UTC bucketing would report the 16th alone", points)
	}
	// The 16th holds its own window, which starts before the data does, so a
	// count of one says the midnight message was filed under the 17th.
	evening := pointOn(t, points, "2026-08-16")
	if evening.Count != 1 || math.Abs(evening.Mean-0.2) > 1e-9 {
		t.Errorf("point on the 16th = %+v, want the 23:00 CST message alone: 16:00 UTC is midnight on the 17th in CST", evening)
	}
	if points[1].Day != "2026-08-17" {
		t.Errorf("second day = %s, want 2026-08-17", points[1].Day)
	}
}

// TestTrend_IgnoresScoresWithoutAPolarity records the exclusion: a NULL is a
// score with no number behind it, and counting it as a sample would credit the
// curve with evidence it does not have.
func TestTrend_IgnoresScoresWithoutAPolarity(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putScored(t, s, "scored", trendDayMs(16), testModelVersion, 0.4)
	putTyped(t, s, "abstain", trendDayMs(16), true, AnalyzableMsgType, "abstain")
	scoreMessage(t, s, messageID(t, s, "abstain"), testModelVersion, nil)

	points, err := s.Trend(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
	if points[0].Count != 1 || math.Abs(points[0].Mean-0.4) > 1e-9 {
		t.Errorf("point = %+v, want 1 sample with mean 0.4 (the NULL polarity is not a sample)", points[0])
	}
}

// TestTrend_OmitsADayWhoseScoresAreAllNull pins the other half of the NULL
// rule: a score with no number is not a sample, so a day where every score is
// NULL is a day with no measurements — not a day that measured zero. The
// query filters before it groups, which is what makes this hold for a whole
// day and not just for one row inside it.
func TestTrend_OmitsADayWhoseScoresAreAllNull(t *testing.T) {
	s := migratedStore(t)
	putTyped(t, s, "unscored", trendDayMs(3), true, AnalyzableMsgType, "unscored")
	scoreMessage(t, s, messageID(t, s, "unscored"), testModelVersion, nil)

	points, err := s.Trend(context.Background(), "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("points = %v, want none: a day with no numeric score measured nothing, it did not measure neutral", points)
	}
}

// TestTrend_CountsEveryScoredMessageRegardlessOfType records a deliberate
// choice: Trend reads the same scored rows the daily rollup does, so it does not
// re-apply the analyzable-type filter that PendingMessages uses. In practice
// the sentiment pass only scores type 1, so the two agree; a score parked on
// another type is counted here. Changing one without the other would make the
// curve and the daily table disagree, which is the failure this test exists to
// prevent.
func TestTrend_CountsEveryScoredMessageRegardlessOfType(t *testing.T) {
	s := migratedStore(t)
	putTyped(t, s, "notice", trendDayMs(3), true, 10000, "a system notice")
	scoreMessage(t, s, messageID(t, s, "notice"), testModelVersion, ptrFloat(0.5))

	points, err := s.Trend(context.Background(), "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if len(points) != 1 || points[0].Count != 1 {
		t.Fatalf("points = %v, want one point with one sample: Trend shares the daily rollup's row set", points)
	}
}

func ptrFloat(value float64) *float64 { return &value }

func TestTrend_EmptyStoreReturnsNoPoints(t *testing.T) {
	points, err := migratedStore(t).Trend(context.Background(), "c", testModelVersion)
	if err != nil {
		t.Fatalf("Trend() error: %v", err)
	}
	if points == nil {
		t.Error("Trend() = nil, want a non-nil empty slice so callers can rely on len()")
	}
	if len(points) != 0 {
		t.Errorf("Trend() = %v, want no points for a conversation with no scores", points)
	}
}
