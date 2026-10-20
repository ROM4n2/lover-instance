package store

import (
	"context"
	"database/sql"
	"fmt"
)

// AnalyzableMsgType is the only message type the sentiment pass reads.
//
// Everything else in the export is either not text (images, voice, video,
// position) or not the sender's own words (the system notices the client
// emits, which is where a revocation lands). Scoring those would feed the
// lexicon strings nobody typed. The types left out are not discarded quietly:
// ExcludedTypeCounts reports them so the report can say how much of the
// conversation was out of scope.
const AnalyzableMsgType = 1

// AllPeriod is the word_freq period covering the whole conversation. The other
// periods are months in CST ("2026-08"), written alongside it by the same
// scoring block; see MonthTokens.
const AllPeriod = "all"

// SentimentRow is one message's score. The store owns this shape so it does
// not have to depend on the NLP client's types to record a score.
type SentimentRow struct {
	MsgID     int64
	Polarity  float64
	Emotion   string
	Intensity float64
}

// PendingMsg is a message still waiting to be scored.
//
// TsMs is carried because the monthly word-freq rows are keyed on the month of
// each message, and a block of messages can straddle a month boundary: the
// block-level token map has already lost which token came from which month by
// the time the scores come back, so the per-message timestamp has to travel
// with the message.
type PendingMsg struct {
	ID      int64
	Content string
	IsSelf  bool
	MsgType int
	TsMs    int64
}

// Coverage reports how much of a conversation has been scored, so a report can
// label a partial run instead of drawing a curve that looks complete.
type Coverage struct {
	Scored int
	Total  int
}

// PendingMessages returns unscored analyzable messages for a conversation.
//
// The anti-join is the checkpoint: a block that has been written is excluded
// from the next call, so restarting a long run costs nothing and re-running a
// finished one does no work. Nothing is held open between calls — the design
// forbids holding the single writer connection while an HTTP request is in
// flight, and this query returns before any request is made.
func (s *Store) PendingMessages(ctx context.Context, talker, modelVersion string, limit int) ([]PendingMsg, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("pending messages: limit must be positive, got %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, COALESCE(m.content, ''), m.is_self, m.msg_type, m.ts
  FROM messages m
 WHERE m.talker = ?
   AND m.msg_type = ?
   AND NOT EXISTS (
         SELECT 1 FROM sentiment s
          WHERE s.msg_id = m.id AND s.model_version = ?
       )
 ORDER BY m.ts ASC, m.id ASC
 LIMIT ?
`, talker, AnalyzableMsgType, modelVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("read pending messages: %w", err)
	}
	defer rows.Close()

	var pending []PendingMsg
	for rows.Next() {
		var msg PendingMsg
		var isSelf int
		if err := rows.Scan(&msg.ID, &msg.Content, &isSelf, &msg.MsgType, &msg.TsMs); err != nil {
			return nil, fmt.Errorf("scan pending message: %w", err)
		}
		msg.IsSelf = isSelf == 1
		pending = append(pending, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending messages: %w", err)
	}
	return pending, nil
}

// CountAnalyzable reports how many messages of this conversation the sentiment
// pass is meant to cover.
func (s *Store) CountAnalyzable(ctx context.Context, talker string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE talker = ? AND msg_type = ?`, talker, AnalyzableMsgType).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count analyzable messages: %w", err)
	}
	return count, nil
}

// MonthTokens is one month's tokens for one block, split by who sent them.
//
// It exists because the block-level token maps cannot be filed under a month:
// one block of 256 messages can straddle midnight on the 1st, so its counts
// belong to two months and there is no way to split them after the fact. The
// caller therefore re-buckets per message, and hands the result here.
type MonthTokens struct {
	Month   string
	Self    map[string]int
	Partner map[string]int
}

// SaveScoredBlock records one batch's outcome: the per-message scores and the
// token counts they contributed.
//
// Scores and counts land in the same transaction on purpose. The counts are
// accumulated with `cnt = cnt + excluded.cnt` rather than recomputed, so the
// anti-join must be the only thing deciding which batches contribute — if a
// batch were written twice the counts would double. One transaction makes that
// atomic, and the anti-join makes it happen at most once.
//
// monthly holds the same block's tokens re-split by message month. The all-time
// rows come from period as before; each monthly entry writes its own YYYY-MM
// period in the same transaction, so a report can never read a month that is
// only half of what the all-time row already counts.
func (s *Store) SaveScoredBlock(ctx context.Context, talker, modelVersion, period string, rows []SentimentRow, selfTokens, partnerTokens map[string]int, monthly []MonthTokens) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scored block: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := writeSentimentRows(ctx, tx, modelVersion, rows); err != nil {
		return err
	}
	if err := addTokenCounts(ctx, tx, talker, period, modelVersion, true, selfTokens); err != nil {
		return err
	}
	if err := addTokenCounts(ctx, tx, talker, period, modelVersion, false, partnerTokens); err != nil {
		return err
	}
	for _, bucket := range monthly {
		// An empty month would file the counts under a period no reader asks
		// for; skipping it keeps a caller bug from creating a junk period
		// rather than hiding real tokens.
		if bucket.Month == "" {
			continue
		}
		if err := addTokenCounts(ctx, tx, talker, bucket.Month, modelVersion, true, bucket.Self); err != nil {
			return err
		}
		if err := addTokenCounts(ctx, tx, talker, bucket.Month, modelVersion, false, bucket.Partner); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scored block: %w", err)
	}
	return nil
}

func writeSentimentRows(ctx context.Context, tx *sql.Tx, modelVersion string, rows []SentimentRow) error {
	if len(rows) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO sentiment (msg_id, model_version, polarity, emotion, intensity)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (msg_id, model_version) DO UPDATE SET
  polarity = excluded.polarity, emotion = excluded.emotion, intensity = excluded.intensity
`)
	if err != nil {
		return fmt.Errorf("prepare sentiment insert: %w", err)
	}
	defer statement.Close()
	for _, row := range rows {
		if _, err := statement.ExecContext(ctx, row.MsgID, modelVersion, row.Polarity, row.Emotion, row.Intensity); err != nil {
			return fmt.Errorf("insert sentiment for message %d: %w", row.MsgID, err)
		}
	}
	return nil
}

// addTokenCounts accumulates one side's tokens for one model version.
//
// model_version is part of the conflict target, not just a stored value: the
// accumulation is unconditional on the row it lands in, so the version has to
// be part of what identifies that row. Without it a rescan under a new version
// would add its tokens onto the previous version's rows and every word cloud
// figure would double.
func addTokenCounts(ctx context.Context, tx *sql.Tx, talker, period, modelVersion string, isSelf bool, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO word_freq_v2 (talker, period, is_self, model_version, word, cnt)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (talker, period, is_self, model_version, word) DO UPDATE SET cnt = cnt + excluded.cnt
`)
	if err != nil {
		return fmt.Errorf("prepare word_freq_v2 upsert: %w", err)
	}
	defer statement.Close()
	for word, count := range counts {
		if count == 0 {
			continue
		}
		if _, err := statement.ExecContext(ctx, talker, period, boolToInt(isSelf), modelVersion, word, count); err != nil {
			return fmt.Errorf("accumulate word_freq_v2 for %q: %w", word, err)
		}
	}
	return nil
}

// SentimentCoverage reports scored against analyzable.
func (s *Store) SentimentCoverage(ctx context.Context, talker, modelVersion string) (Coverage, error) {
	var coverage Coverage
	err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM messages WHERE talker = ? AND msg_type = ?),
  (SELECT COUNT(*)
     FROM messages m
     JOIN sentiment s ON s.msg_id = m.id AND s.model_version = ?
    WHERE m.talker = ? AND m.msg_type = ?)
`, talker, AnalyzableMsgType, modelVersion, talker, AnalyzableMsgType).Scan(&coverage.Total, &coverage.Scored)
	if err != nil {
		return Coverage{}, fmt.Errorf("read sentiment coverage: %w", err)
	}
	return coverage, nil
}

// ExcludedTypeCounts reports the message types the sentiment pass skipped, so
// the omission is visible in the report rather than silent.
func (s *Store) ExcludedTypeCounts(ctx context.Context, talker string) ([]TypeCount, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT msg_type, COUNT(*)
  FROM messages
 WHERE talker = ? AND msg_type <> ?
 GROUP BY msg_type
 ORDER BY msg_type ASC
`, talker, AnalyzableMsgType)
	if err != nil {
		return nil, fmt.Errorf("read excluded type counts: %w", err)
	}
	defer rows.Close()
	var counts []TypeCount
	for rows.Next() {
		var entry TypeCount
		if err := rows.Scan(&entry.MsgType, &entry.Count); err != nil {
			return nil, fmt.Errorf("scan excluded type count: %w", err)
		}
		counts = append(counts, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate excluded type counts: %w", err)
	}
	return counts, nil
}

// RebuildSentimentMetrics writes the sentiment columns into daily_stats for
// this model version.
//
// These rows carry the model version, not "behavior-v1", so a day ends up with
// one row of counts and another of polarity. That is the schema's design and
// both are needed; BehaviorMetrics covers the counts and this covers the
// polarity, and a reader must take them from both rows rather than picking one.
func (s *Store) RebuildSentimentMetrics(ctx context.Context, talker, modelVersion string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sentiment aggregate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM daily_stats WHERE talker = ? AND model_version = ?`, talker, modelVersion); err != nil {
		return fmt.Errorf("clear previous sentiment rows: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO daily_stats (talker, day, model_version, avg_polarity, pos_count, neg_count, updated_at)
SELECT m.talker,
       strftime('%Y-%m-%d', m.ts / 1000, 'unixepoch', '+8 hours'),
       ?,
       AVG(s.polarity),
       SUM(CASE WHEN s.polarity > 0 THEN 1 ELSE 0 END),
       SUM(CASE WHEN s.polarity < 0 THEN 1 ELSE 0 END),
       datetime('now')
  FROM sentiment s
  JOIN messages m ON m.id = s.msg_id
 WHERE m.talker = ? AND s.model_version = ?
 GROUP BY m.talker, strftime('%Y-%m-%d', m.ts / 1000, 'unixepoch', '+8 hours')
`, modelVersion, talker, modelVersion)
	if err != nil {
		return fmt.Errorf("aggregate sentiment by day: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sentiment aggregate: %w", err)
	}
	return nil
}

// EmotionDistribution counts scored messages per emotion label, which is what
// the report's seven-emotion breakdown and radar are drawn from.
func (s *Store) EmotionDistribution(ctx context.Context, talker, modelVersion string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.emotion, COUNT(*)
  FROM sentiment s
  JOIN messages m ON m.id = s.msg_id
 WHERE m.talker = ? AND s.model_version = ?
 GROUP BY s.emotion
`, talker, modelVersion)
	if err != nil {
		return nil, fmt.Errorf("read emotion distribution: %w", err)
	}
	defer rows.Close()
	distribution := make(map[string]int)
	for rows.Next() {
		var emotion sql.NullString
		var count int
		if err := rows.Scan(&emotion, &count); err != nil {
			return nil, fmt.Errorf("scan emotion count: %w", err)
		}
		key := "neutral"
		if emotion.Valid && emotion.String != "" {
			key = emotion.String
		}
		distribution[key] += count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate emotion distribution: %w", err)
	}
	return distribution, nil
}

// SentimentTotals splits the scored messages by polarity sign.
//
// It counts from the per-message scores rather than summing the daily rollup,
// so the polarity split is available even before RebuildSentimentMetrics has
// run and cannot disagree with the per-message data it is derived from.
func (s *Store) SentimentTotals(ctx context.Context, talker, modelVersion string) (positive, negative, neutral int, err error) {
	err = s.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN s.polarity > 0 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN s.polarity < 0 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN s.polarity = 0 OR s.polarity IS NULL THEN 1 ELSE 0 END), 0)
  FROM sentiment s
  JOIN messages m ON m.id = s.msg_id
 WHERE m.talker = ? AND s.model_version = ?
`, talker, modelVersion).Scan(&positive, &negative, &neutral)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read sentiment totals: %w", err)
	}
	return positive, negative, neutral, nil
}

// WordFrequencies reads the accumulated token counts for one side of one model
// version.
//
// The version is required rather than optional: a count belongs to the model
// that produced its tokens, and a report that showed one version's word cloud
// under another version's name would be wrong in a way no reader could see.
func (s *Store) WordFrequencies(ctx context.Context, talker, period string, isSelf bool, modelVersion string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT word, cnt FROM word_freq_v2 WHERE talker = ? AND period = ? AND is_self = ? AND model_version = ?`,
		talker, period, boolToInt(isSelf), modelVersion)
	if err != nil {
		return nil, fmt.Errorf("read word frequencies: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var word string
		var count int
		if err := rows.Scan(&word, &count); err != nil {
			return nil, fmt.Errorf("scan word frequency: %w", err)
		}
		counts[word] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate word frequencies: %w", err)
	}
	return counts, nil
}
