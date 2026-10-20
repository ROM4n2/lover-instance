package store

import (
	"context"
	"reflect"
	"testing"
)

// TestSentimentVersions_ListsTheVersionsThatScored pins the input to the
// report's comparison section: a version nobody ran is not a version this
// conversation can be compared against. The order matters too — the report is
// supposed to render byte-identically between runs, so the list cannot come
// back in whatever order SQLite happens to scan.
func TestSentimentVersions_ListsTheVersionsThatScored(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	both := putScored(t, s, "both", trendDayMs(10), testModelVersion, 0.4)
	scoreMessage(t, s, both, otherModelVersion, ptrFloat(-0.3))
	putScored(t, s, "lexiconOnly", trendDayMs(10), testModelVersion, 0.1)
	putTyped(t, s, "unscored", trendDayMs(10), true, AnalyzableMsgType, "nobody scored me")

	versions, err := s.SentimentVersions(ctx, "c")
	if err != nil {
		t.Fatalf("SentimentVersions() error: %v", err)
	}
	// Sorted, which puts the lexicon first: "lexicon…" < "onnx…".
	want := []string{testModelVersion, otherModelVersion}
	if !reflect.DeepEqual(versions, want) {
		t.Errorf("versions = %v, want %v: deduped, sorted, and a message nobody scored contributes nothing", versions, want)
	}
}

func TestSentimentVersions_EmptyConversationReturnsAnEmptySlice(t *testing.T) {
	versions, err := migratedStore(t).SentimentVersions(context.Background(), "c")
	if err != nil {
		t.Fatalf("SentimentVersions() error: %v", err)
	}
	if versions == nil {
		t.Error("SentimentVersions() = nil, want a non-nil empty slice so a caller can rely on len()")
	}
	if len(versions) != 0 {
		t.Errorf("versions = %v, want none for a conversation with no scores", versions)
	}
}

// TestDisagreement_CountsOnlyMessagesBothVersionsScored pins the denominator.
// The figure says "how often the two versions disagree", so it can only be
// computed over the messages both of them looked at; a message only one
// version scored would otherwise be counted as agreement it never expressed.
func TestDisagreement_CountsOnlyMessagesBothVersionsScored(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	agreePos := putScored(t, s, "agreePos", trendDayMs(10), testModelVersion, 0.5)
	scoreMessage(t, s, agreePos, otherModelVersion, ptrFloat(0.2))
	agreeNeg := putScored(t, s, "agreeNeg", trendDayMs(10), testModelVersion, -0.4)
	scoreMessage(t, s, agreeNeg, otherModelVersion, ptrFloat(-0.6))
	flip1 := putScored(t, s, "flip1", trendDayMs(10), testModelVersion, 0.5)
	scoreMessage(t, s, flip1, otherModelVersion, ptrFloat(-0.1))
	flip2 := putScored(t, s, "flip2", trendDayMs(10), testModelVersion, -0.4)
	scoreMessage(t, s, flip2, otherModelVersion, ptrFloat(0.3))
	// Outside the population: scored by one version only.
	putScored(t, s, "single", trendDayMs(10), testModelVersion, 0.9)

	got, err := s.Disagreement(ctx, "c", testModelVersion, otherModelVersion)
	if err != nil {
		t.Fatalf("Disagreement() error: %v", err)
	}
	if got.Both != 4 || got.Differing != 2 {
		t.Errorf("Disagreement() = %+v, want {Both:4 Differing:2}: the singly-scored message is not evidence either way", got)
	}

	// Which version is on which side must not change the answer.
	swapped, err := s.Disagreement(ctx, "c", otherModelVersion, testModelVersion)
	if err != nil {
		t.Fatalf("Disagreement(swapped) error: %v", err)
	}
	if swapped != got {
		t.Errorf("Disagreement(swapped) = %+v, want %+v: the pair is unordered", swapped, got)
	}
}

// TestDisagreement_ZeroIsItsOwnSide is the assertion this whole definition
// turns on. Polarity is mapped to three buckets — +1 above zero, 0 at zero,
// −1 below zero — so +0.2 against 0.0 is a disagreement. Scoring it as
// agreement (comparing magnitudes, or testing only the two judged buckets)
// would understate exactly the change the report is trying to disclose: a
// model that turns「敷衍」into a confident +0.6.
func TestDisagreement_ZeroIsItsOwnSide(t *testing.T) {
	cases := []struct {
		name   string
		left   float64
		right  float64
		differ bool
	}{
		{"both positive", 0.2, 0.5, false},
		{"both negative", -0.2, -0.5, false},
		{"both zero", 0.0, 0.0, false},
		{"positive against zero", 0.2, 0.0, true},
		{"negative against zero", -0.2, 0.0, true},
		{"positive against negative", 0.2, -0.2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := migratedStore(t)
			id := putScored(t, s, "m", trendDayMs(10), testModelVersion, tc.left)
			scoreMessage(t, s, id, otherModelVersion, ptrFloat(tc.right))

			got, err := s.Disagreement(context.Background(), "c", testModelVersion, otherModelVersion)
			if err != nil {
				t.Fatalf("Disagreement() error: %v", err)
			}
			want := 0
			if tc.differ {
				want = 1
			}
			if got.Both != 1 || got.Differing != want {
				t.Errorf("%v vs %v = %+v, want {Both:1 Differing:%d}", tc.left, tc.right, got, want)
			}
		})
	}
}

// TestDisagreement_IgnoresScoresWithoutAPolarity keeps a declined score out of
// the denominator for the same reason Trend keeps it out of the mean: NULL is
// not a side of zero, it is the absence of a judgement.
func TestDisagreement_IgnoresScoresWithoutAPolarity(t *testing.T) {
	s := migratedStore(t)
	id := putScored(t, s, "abstain", trendDayMs(10), testModelVersion, 0.4)
	scoreMessage(t, s, id, otherModelVersion, nil)

	got, err := s.Disagreement(context.Background(), "c", testModelVersion, otherModelVersion)
	if err != nil {
		t.Fatalf("Disagreement() error: %v", err)
	}
	if got.Both != 0 || got.Differing != 0 {
		t.Errorf("Disagreement() = %+v, want zeroes: the model put no number on the message", got)
	}
}
