package store

import (
	"context"
	"fmt"
)

// WordCount is one word and how often it was counted. The store defines its own
// shape rather than borrowing the report's, so a caller can ask for a ranking
// without depending on the presentation layer.
type WordCount struct {
	Word  string
	Count int
}

// MonthWords is one month's ranking: at most limit words, most frequent first.
//
// Month is the YYYY-MM period the rows were filed under, not a month invented
// here. A month with no rows is absent rather than empty: history scored before
// monthly periods existed only has the all-time row, and pretending August 2025
// was "a quiet month" would be a fabricated statement about real data.
type MonthWords struct {
	Month string
	Words []WordCount
}

// MonthlyWordFrequencies reads the accumulated token counts per month for one
// side of one model version.
//
// Ordering is the same rule the all-time word cloud uses — count descending,
// word ascending — so a regenerated ranking is stable; relying on the row order
// SQLite happens to return would let two runs disagree for no reason. The
// version filter is not optional: a month counts the tokens of the model that
// produced them, and mixing two versions' rows would double every count under a
// label that looked like one model's work.
//
// An empty result is an empty slice and a nil error, not an error: no monthly
// rows means the data predates monthly scoring, which is the caller's fact to
// report, not a failure.
func (s *Store) MonthlyWordFrequencies(ctx context.Context, talker, modelVersion string, isSelf bool, limit int) ([]MonthWords, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("monthly word frequencies: limit must be positive, got %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT period, word, cnt
  FROM word_freq_v2
 WHERE talker = ? AND is_self = ? AND model_version = ? AND period <> ?
 ORDER BY period ASC, cnt DESC, word ASC
`, talker, boolToInt(isSelf), modelVersion, AllPeriod)
	if err != nil {
		return nil, fmt.Errorf("read monthly word frequencies: %w", err)
	}
	defer rows.Close()

	// The query is ordered by period, so a new month starts exactly when the
	// period changes: no map, and therefore no re-sort that could reintroduce a
	// different tie-break than the one the query used.
	months := make([]MonthWords, 0, 8)
	for rows.Next() {
		var period, word string
		var count int
		if err := rows.Scan(&period, &word, &count); err != nil {
			return nil, fmt.Errorf("scan monthly word frequency: %w", err)
		}
		if len(months) == 0 || months[len(months)-1].Month != period {
			months = append(months, MonthWords{Month: period, Words: make([]WordCount, 0, limit)})
		}
		bucket := &months[len(months)-1]
		if len(bucket.Words) >= limit {
			// Past the limit for this month: the rows are already in rank
			// order, so the rest of the month can be dropped unread.
			continue
		}
		bucket.Words = append(bucket.Words, WordCount{Word: word, Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monthly word frequencies: %w", err)
	}
	return months, nil
}
