package store

import (
	"context"
	"testing"
)

// countRows runs a COUNT(*) query and returns the number, failing the test on
// error.
func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// TestResetScores_RemovesOnlyTheNamedVersion pins the scope of the one method
// that deletes data. It must clear this conversation's rows for the named model
// and nothing else: another version's scores, another conversation's scores and
// the messages themselves all have to survive. The mutation that drops the
// model_version condition from the deletes turns this red.
func TestResetScores_RemovesOnlyTheNamedVersion(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	// Two messages in the conversation under test, and one in a different one.
	put(t, s, "k1", at(2026, 8, 16, 12, 0, 0), true, "今天开心")
	put(t, s, "k2", at(2026, 8, 16, 12, 1, 0), false, "我也开心")
	exec(t, s, `INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('src', 'k3', 1792167000000, 'other', 'someone', 0, 1, 0, '', 'h3')`)

	// Two versions scoring the conversation under test.
	if err := s.SaveScoredBlock(ctx, "c", testModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: 1, Polarity: 0.5}}, map[string]int{"开心": 1}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock(%s) error: %v", testModelVersion, err)
	}
	if err := s.SaveScoredBlock(ctx, "c", otherModelVersion, AllPeriod,
		[]SentimentRow{{MsgID: 2, Polarity: 0.4}}, map[string]int{"也": 1}, nil, nil); err != nil {
		t.Fatalf("SaveScoredBlock(%s) error: %v", otherModelVersion, err)
	}
	// The named version also scored a different conversation, which must not be
	// cleared: the delete is scoped through the messages of one talker.
	exec(t, s, `INSERT INTO sentiment (msg_id, model_version, polarity) VALUES (3, '`+testModelVersion+`', 0.2)`)

	removed, err := s.ResetScores(ctx, "c", testModelVersion)
	if err != nil {
		t.Fatalf("ResetScores() error: %v", err)
	}
	// One sentiment row and one word row for the named version.
	if removed != 2 {
		t.Errorf("ResetScores() removed %d rows, want 2 (one score + one word)", removed)
	}

	if got := countRows(t, s, `SELECT COUNT(*) FROM sentiment WHERE msg_id = 1 AND model_version = ?`, testModelVersion); got != 0 {
		t.Errorf("the named version's score on message 1 survived the reset")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM sentiment WHERE msg_id = 2 AND model_version = ?`, otherModelVersion); got != 1 {
		t.Errorf("the other version's score was deleted; it must survive")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM sentiment WHERE msg_id = 3 AND model_version = ?`, testModelVersion); got != 1 {
		t.Errorf("another conversation's score was deleted; the reset must be scoped to one talker")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM word_freq_v2 WHERE talker = 'c' AND model_version = ?`, testModelVersion); got != 0 {
		t.Errorf("the named version's word rows survived the reset")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM word_freq_v2 WHERE talker = 'c' AND model_version = ?`, otherModelVersion); got != 1 {
		t.Errorf("the other version's word row was deleted; it must survive")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages`); got != 3 {
		t.Errorf("the reset changed the messages table: %d rows, want 3", got)
	}
}

// TestResetScores_RefusesAnEmptyVersion keeps the rescan from becoming a
// database-wide wipe through a missing flag value.
func TestResetScores_RefusesAnEmptyVersion(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	if _, err := s.ResetScores(ctx, "c", ""); err == nil {
		t.Fatal("ResetScores(version=\"\") = nil error, want a refusal")
	}
}
