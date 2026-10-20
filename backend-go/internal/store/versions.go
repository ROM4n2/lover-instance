package store

import (
	"context"
	"fmt"
)

// SentimentVersions lists the model versions that actually scored this
// conversation, oldest name first.
//
// The list comes from the score rows rather than from a registry of known
// versions: a version that was never run on this conversation is not a version
// the report can compare against. Two passes under the same version share one
// entry, and the ordering is fixed so a report rendered twice is identical.
func (s *Store) SentimentVersions(ctx context.Context, talker string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT s.model_version
  FROM sentiment s
  JOIN messages m ON m.id = s.msg_id
 WHERE m.talker = ?
 ORDER BY s.model_version ASC
`, talker)
	if err != nil {
		return nil, fmt.Errorf("read sentiment versions: %w", err)
	}
	defer rows.Close()

	versions := make([]string, 0, 2)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan sentiment version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sentiment versions: %w", err)
	}
	return versions, nil
}

// Disagreement is how often two model versions put the same message on a
// different side.
//
// Both is the population — the messages both versions scored — and Differing is
// how many of those they read differently. The pair is reported together
// because a rate without its denominator hides how little of the conversation
// the two versions actually overlap on.
type Disagreement struct {
	Both      int // messages scored by both versions
	Differing int // messages the two versions placed on different sides
}

// Disagreement compares two versions over the messages both of them scored.
//
// Only messages with a numeric score from both versions take part. The join is
// on the message, so the count is over the same messages the two passes both
// saw rather than over two independent totals; a message only one version
// scored is not agreement, it is missing evidence.
//
// "Differing" is decided on three buckets: +1 for a polarity above zero, 0 for
// exactly zero, −1 for below zero. The buckets are deliberately coarse, because
// that is the only reading the two versions share — their magnitudes are not
// comparable, since the lexicon's polarity is a degree factor while a model's
// `intensity` is |polarity| (Task 3 of the M2 plan), so comparing magnitudes
// would report a difference that says more about the two scales than about the
// two judgements.
//
// The middle bucket is a side, not an absence: +0.2 against 0.0 is a
// disagreement. Treating zero as "no opinion" would hide exactly the change
// worth disclosing — a model that turns a message the lexicon called neutral
// into a confident +0.6. A NULL polarity, by contrast, is not a side at all: it
// is a score the version declined to put a number on, so those rows are
// filtered out before counting.
func (s *Store) Disagreement(ctx context.Context, talker, left, right string) (Disagreement, error) {
	// The bucketing is written out in SQL as well as in SentimentSide: the
	// aggregate has to happen in the database, because pulling every
	// overlapping message into Go to compare two floats would read the whole
	// conversation for two integers.
	var result Disagreement
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN
         (CASE WHEN l.polarity > 0 THEN 1 WHEN l.polarity < 0 THEN -1 ELSE 0 END)
         <>
         (CASE WHEN r.polarity > 0 THEN 1 WHEN r.polarity < 0 THEN -1 ELSE 0 END)
       THEN 1 ELSE 0 END), 0)
  FROM sentiment l
  JOIN sentiment r ON r.msg_id = l.msg_id AND r.model_version = ?
  JOIN messages m ON m.id = l.msg_id
 WHERE m.talker = ?
   AND l.model_version = ?
   AND l.polarity IS NOT NULL
   AND r.polarity IS NOT NULL
`, right, talker, left).Scan(&result.Both, &result.Differing)
	if err != nil {
		return Disagreement{}, fmt.Errorf("read disagreement between %q and %q: %w", left, right, err)
	}
	return result, nil
}
