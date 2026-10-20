package store

import (
	"context"
	"testing"
	"time"
)

// putMonth writes one month's tokens for one side through the real write path,
// so a reader test never sets up rows the writer could not have produced.
func putMonth(t *testing.T, s *Store, modelVersion, month string, isSelf bool, counts map[string]int) {
	t.Helper()
	bucket := MonthTokens{Month: month}
	if isSelf {
		bucket.Self = counts
	} else {
		bucket.Partner = counts
	}
	if err := s.SaveScoredBlock(context.Background(), "c", modelVersion, AllPeriod, nil, nil, nil, []MonthTokens{bucket}); err != nil {
		t.Fatalf("SaveScoredBlock(%s, %s) error: %v", modelVersion, month, err)
	}
}

// TestMonthlyWordFrequencies_RanksEachMonthSeparately is the shape the monthly
// chart needs: months ascending, each one ranked on its own counts. A ranking
// that pooled the months would answer "the most frequent words overall" a second
// time under a heading that promises change over time.
func TestMonthlyWordFrequencies_RanksEachMonthSeparately(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putMonth(t, s, testModelVersion, "2026-08", true, map[string]int{"你好": 2})
	putMonth(t, s, testModelVersion, "2026-07", true, map[string]int{"你好": 5, "aa": 3, "bb": 3})

	months, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, true, 10)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies() error: %v", err)
	}
	if len(months) != 2 {
		t.Fatalf("months = %+v, want 2 (2026-07 then 2026-08)", months)
	}
	if months[0].Month != "2026-07" || months[1].Month != "2026-08" {
		t.Fatalf("month order = %q, %q, want ascending 2026-07, 2026-08", months[0].Month, months[1].Month)
	}

	// Count descending, then word ascending on the tie, which is the same
	// tie-break the all-time word cloud uses so two runs cannot disagree.
	want := []WordCount{{Word: "你好", Count: 5}, {Word: "aa", Count: 3}, {Word: "bb", Count: 3}}
	if len(months[0].Words) != len(want) {
		t.Fatalf("2026-07 words = %+v, want %+v", months[0].Words, want)
	}
	for i, entry := range want {
		if months[0].Words[i] != entry {
			t.Errorf("2026-07 word #%d = %+v, want %+v (count desc, word asc)", i, months[0].Words[i], entry)
		}
	}
	if len(months[1].Words) != 1 || months[1].Words[0] != (WordCount{Word: "你好", Count: 2}) {
		t.Errorf("2026-08 words = %+v, want 你好:2 alone", months[1].Words)
	}

	// Repeated reads must be byte-identical: the ranking is derived, not
	// remembered from whatever order the rows happened to come back in.
	again, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, true, 10)
	if err != nil {
		t.Fatalf("second MonthlyWordFrequencies() error: %v", err)
	}
	for monthIndex := range months {
		for wordIndex := range months[monthIndex].Words {
			if again[monthIndex].Words[wordIndex] != months[monthIndex].Words[wordIndex] {
				t.Fatalf("second read differs at month %d word %d: %+v vs %+v",
					monthIndex, wordIndex, again[monthIndex].Words[wordIndex], months[monthIndex].Words[wordIndex])
			}
		}
	}
}

func TestMonthlyWordFrequencies_HonoursTheLimit(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putMonth(t, s, testModelVersion, "2026-07", true, map[string]int{"你好": 5, "aa": 3, "bb": 3})

	months, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, true, 2)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies(limit=2) error: %v", err)
	}
	if len(months) != 1 {
		t.Fatalf("months = %+v, want one month", months)
	}
	want := []WordCount{{Word: "你好", Count: 5}, {Word: "aa", Count: 3}}
	if len(months[0].Words) != 2 {
		t.Fatalf("2026-07 words = %+v, want the top 2 only", months[0].Words)
	}
	for i, entry := range want {
		if months[0].Words[i] != entry {
			t.Errorf("word #%d = %+v, want %+v", i, months[0].Words[i], entry)
		}
	}
}

func TestMonthlyWordFrequencies_RejectsANonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := migratedStore(t).MonthlyWordFrequencies(context.Background(), "c", testModelVersion, true, limit); err == nil {
			t.Errorf("MonthlyWordFrequencies(limit=%d) = nil error, want an error", limit)
		}
	}
}

// TestMonthlyWordFrequencies_EmptyDatabaseReturnsAnEmptySlice pins the historical
// case: data scored before monthly periods existed only has the all-time row.
// The store returns no months at all — it does not synthesise a month from the
// all-time row and does not return an error, because "nothing to show yet" is
// the caller's fact to explain.
func TestMonthlyWordFrequencies_EmptyDatabaseReturnsAnEmptySlice(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putMonth(t, s, testModelVersion, "2026-07", true, map[string]int{"你好": 5})
	// And an all-time row, as historical data would have.
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, nil,
		map[string]int{"你好": 1}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock(all) error: %v", err)
	}

	exec(t, s, `DELETE FROM word_freq_v2 WHERE period <> 'all'`)
	months, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, true, 10)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies() error: %v", err)
	}
	if months == nil {
		t.Fatal("MonthlyWordFrequencies() = nil, want an empty slice")
	}
	if len(months) != 0 {
		t.Errorf("months = %+v, want none: the all-time row is not a month", months)
	}
}

// TestMonthlyWordFrequencies_KeepsVersionsApart is the monthly form of D5: a
// month belongs to the model that produced it. Merging the versions would
// double every count under one version's name, which no reader could detect.
func TestMonthlyWordFrequencies_KeepsVersionsApart(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putMonth(t, s, testModelVersion, "2026-07", true, map[string]int{"你好": 4})
	putMonth(t, s, otherModelVersion, "2026-07", true, map[string]int{"在吗": 9})

	first, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, true, 10)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies(%s) error: %v", testModelVersion, err)
	}
	second, err := s.MonthlyWordFrequencies(ctx, "c", otherModelVersion, true, 10)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies(%s) error: %v", otherModelVersion, err)
	}

	if len(first) != 1 || len(first[0].Words) != 1 || first[0].Words[0] != (WordCount{Word: "你好", Count: 4}) {
		t.Errorf("%s monthly words = %+v, want only 你好:4", testModelVersion, first)
	}
	if len(second) != 1 || len(second[0].Words) != 1 || second[0].Words[0] != (WordCount{Word: "在吗", Count: 9}) {
		t.Errorf("%s monthly words = %+v, want only 在吗:9", otherModelVersion, second)
	}
}

// TestMonthlyWordFrequencies_SeparatesTheTwoSides keeps one speaker's words out
// of the other's chart.
func TestMonthlyWordFrequencies_SeparatesTheTwoSides(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	putMonth(t, s, testModelVersion, "2026-07", true, map[string]int{"你好": 1})
	putMonth(t, s, testModelVersion, "2026-07", false, map[string]int{"在吗": 1})

	partner, err := s.MonthlyWordFrequencies(ctx, "c", testModelVersion, false, 10)
	if err != nil {
		t.Fatalf("MonthlyWordFrequencies(partner) error: %v", err)
	}
	if len(partner) != 1 || len(partner[0].Words) != 1 || partner[0].Words[0].Word != "在吗" {
		t.Errorf("partner monthly words = %+v, want only 在吗", partner)
	}
}

// TestMonthOf_UsesBeijingMonths pins the boundary store.MonthOf exists for:
// 2026-08-31 23:50 and 2026-09-01 00:10 CST are two months, although both are
// August in UTC.
func TestMonthOf_UsesBeijingMonths(t *testing.T) {
	beijing := time.FixedZone("CST", 8*60*60)
	lateAugust := time.Date(2026, time.August, 31, 23, 50, 0, 0, beijing).UnixMilli()
	earlySeptember := time.Date(2026, time.September, 1, 0, 10, 0, 0, beijing).UnixMilli()

	if got := MonthOf(lateAugust); got != "2026-08" {
		t.Errorf("MonthOf(2026-08-31 23:50 CST) = %q, want 2026-08", got)
	}
	if got := MonthOf(earlySeptember); got != "2026-09" {
		t.Errorf("MonthOf(2026-09-01 00:10 CST) = %q, want 2026-09 (UTC would say 2026-08)", got)
	}
}
