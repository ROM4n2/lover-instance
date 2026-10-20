package store

import (
	"context"
	"testing"
	"time"
)

// otherModelVersion is a second sentiment version. Two versions coexisting is
// the whole reason word_freq needed a version dimension: a rescan under a new
// version must land in its own rows.
const otherModelVersion = "onnx-erlangshen-v1"

// TestSaveScoredBlock_KeysWordFreqByModelVersion is the defect guard for D5:
// the same messages scored under two model versions must produce two rows with
// their own counts, and a read must return one version's numbers only. With the
// old unconditional `cnt = cnt + excluded.cnt` on a version-less key, the
// second pass added its tokens onto the first pass's rows and the word cloud
// doubled.
func TestSaveScoredBlock_KeysWordFreqByModelVersion(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "你好")

	block := []SentimentRow{{MsgID: 1, Polarity: 0.4, Emotion: "乐", Intensity: 0.3}}
	self := map[string]int{"你好": 1, "[捂脸]": 2}
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod, block, self, nil); err != nil {
		t.Fatalf("SaveScoredBlock(%s) error: %v", testModelVersion, err)
	}
	// The same batch, rescanned under the next model version.
	if err := s.SaveScoredBlock(ctx, "c", otherModelVersion, AllPeriod, block, self, nil); err != nil {
		t.Fatalf("SaveScoredBlock(%s) error: %v", otherModelVersion, err)
	}

	first, err := s.WordFrequencies(ctx, "c", AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(%s) error: %v", testModelVersion, err)
	}
	second, err := s.WordFrequencies(ctx, "c", AllPeriod, true, otherModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(%s) error: %v", otherModelVersion, err)
	}
	if first["你好"] != 1 || first["[捂脸]"] != 2 {
		t.Errorf("version %s counts = %v, want 你好:1 [捂脸]:2 — a rescan under another version must not touch them",
			testModelVersion, first)
	}
	if second["你好"] != 1 || second["[捂脸]"] != 2 {
		t.Errorf("version %s counts = %v, want 你好:1 [捂脸]:2 of its own", otherModelVersion, second)
	}

	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM word_freq_v2 WHERE talker = 'c'`).Scan(&rows); err != nil {
		t.Fatalf("count word_freq_v2 rows: %v", err)
	}
	if rows != 4 {
		t.Errorf("word_freq_v2 holds %d rows, want 4 (two words x two versions)", rows)
	}
}

// TestWordFrequencies_ReturnsNothingForAVersionThatNeverRan pins the other half
// of the key: an unknown version reads empty rather than borrowing another
// version's numbers. A fallback to "the only version present" would make a
// report claim figures for a model that never scored anything.
func TestWordFrequencies_ReturnsNothingForAVersionThatNeverRan(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	put(t, s, "k1", at(2026, time.August, 16, 12, 0, 0), true, "你好")
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: 1, Polarity: 0.4}}, map[string]int{"你好": 1}, nil); err != nil {
		t.Fatalf("SaveScoredBlock() error: %v", err)
	}

	counts, err := s.WordFrequencies(ctx, "c", AllPeriod, true, otherModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies() error: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts for %s = %v, want empty: it never produced any tokens", otherModelVersion, counts)
	}
}

// TestMigrate_CopiesWordFreqIntoV2ForTheSingleModelVersion covers the upgrade of
// an existing database. The legacy table has no version column, so its rows can
// only be attributed when the sentiment table names exactly one version — that
// is the version that produced them.
func TestMigrate_CopiesWordFreqIntoV2ForTheSingleModelVersion(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t) // first Migrate ran on an empty database, so it copied nothing

	exec(t, s, `INSERT INTO word_freq (talker, period, is_self, word, cnt) VALUES ('c', 'all', 1, '你好', 7)`)
	exec(t, s, `INSERT INTO word_freq (talker, period, is_self, word, cnt) VALUES ('c', 'all', 1, '[捂脸]', 2)`)
	exec(t, s, `INSERT INTO word_freq (talker, period, is_self, word, cnt) VALUES ('c', 'all', 0, '难过', 3)`)
	exec(t, s, `INSERT INTO sentiment (msg_id, model_version, polarity) VALUES (1, '`+testModelVersion+`', 0.5)`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	self, err := s.WordFrequencies(ctx, "c", AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(self) error: %v", err)
	}
	partner, err := s.WordFrequencies(ctx, "c", AllPeriod, false, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies(partner) error: %v", err)
	}
	if self["你好"] != 7 || self["[捂脸]"] != 2 {
		t.Errorf("migrated self counts = %v, want 你好:7 [捂脸]:2 from the legacy table", self)
	}
	if partner["难过"] != 3 {
		t.Errorf("migrated partner counts = %v, want 难过:3 from the legacy table", partner)
	}

	// The legacy table is kept read-only, never dropped or rewritten: the export
	// is the only way back, and a migration that deleted it would destroy the
	// only copy of data it had not managed to attribute.
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM word_freq WHERE talker = 'c'`).Scan(&legacy); err != nil {
		t.Fatalf("count legacy word_freq rows: %v", err)
	}
	if legacy != 3 {
		t.Errorf("legacy word_freq holds %d rows after migrating, want the original 3 left untouched", legacy)
	}

	// Idempotence: Migrate runs on every startup, so a second run must not
	// duplicate the rows or double the counts.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error: %v", err)
	}
	again, err := s.WordFrequencies(ctx, "c", AllPeriod, true, testModelVersion)
	if err != nil {
		t.Fatalf("WordFrequencies() after the second Migrate error: %v", err)
	}
	if again["你好"] != 7 || again["[捂脸]"] != 2 {
		t.Errorf("counts after a second Migrate = %v, want unchanged 你好:7 [捂脸]:2 — the copy must be idempotent", again)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM word_freq_v2`).Scan(&rows); err != nil {
		t.Fatalf("count word_freq_v2 rows: %v", err)
	}
	if rows != 3 {
		t.Errorf("word_freq_v2 holds %d rows after two Migrate runs, want 3", rows)
	}
}

// TestMigrate_DoesNotCopyWordFreqWhenTheVersionIsAmbiguous is the honest half of
// the migration: with two model versions in the sentiment table there is no way
// to tell which one produced the legacy counts. Guessing would attribute a word
// cloud to a model that never produced it, which is exactly the mislabelling
// this task removes — so nothing is copied.
func TestMigrate_DoesNotCopyWordFreqWhenTheVersionIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	exec(t, s, `INSERT INTO word_freq (talker, period, is_self, word, cnt) VALUES ('c', 'all', 1, '你好', 7)`)
	exec(t, s, `INSERT INTO sentiment (msg_id, model_version, polarity) VALUES (1, '`+testModelVersion+`', 0.5)`)
	exec(t, s, `INSERT INTO sentiment (msg_id, model_version, polarity) VALUES (1, '`+otherModelVersion+`', 0.6)`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM word_freq_v2`).Scan(&rows); err != nil {
		t.Fatalf("count word_freq_v2 rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("word_freq_v2 holds %d rows with two versions in sentiment, want 0: the legacy counts cannot be attributed", rows)
	}
}

// TestMigrate_CopiesNothingWhenNoSentimentWasEverScored covers a database that
// was ingested but never scored, which is a normal intermediate state. There is
// no version to name, so the copy is skipped and Migrate still succeeds.
func TestMigrate_CopiesNothingWhenNoSentimentWasEverScored(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	exec(t, s, `INSERT INTO word_freq (talker, period, is_self, word, cnt) VALUES ('c', 'all', 1, '你好', 7)`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() with an empty sentiment table error: %v", err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM word_freq_v2`).Scan(&rows); err != nil {
		t.Fatalf("count word_freq_v2 rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("word_freq_v2 holds %d rows but no model version has ever scored anything, want 0", rows)
	}
}

// exec runs one statement against the store as a fixture step.
func exec(t *testing.T, s *Store, statement string) {
	t.Helper()
	if _, err := s.db.Exec(statement); err != nil {
		t.Fatalf("fixture statement %q: %v", statement, err)
	}
}
