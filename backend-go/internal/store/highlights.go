package store

import (
	"context"
	"fmt"
)

// HighlightEvent represents a key milestone or peak moment discovered in a conversation.
type HighlightEvent struct {
	ID          int64
	Talker      string
	Kind        string  // e.g. "origin", "streak", "peak_day", "late_night", "special_day"
	EventDate   string  // e.g. "2024-05-20" or "2024-01-01~2024-02-15"
	Title       string
	Description string
	MetricValue float64
	ExtraJSON   string
	CreatedAt   string
}

// PeakDayRow captures message distribution on a high-activity day.
type PeakDayRow struct {
	Day         string
	TotalMsgs   int
	SelfMsgs    int
	PartnerMsgs int
}

// LateNightRow captures midnight-to-dawn conversation volume.
type LateNightRow struct {
	Day      string
	LateMsgs int
}

// SaveHighlights atomically replaces the stored highlights for a talker.
func (s *Store) SaveHighlights(ctx context.Context, talker string, events []HighlightEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save highlights: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM highlights WHERE talker = ?", talker); err != nil {
		return fmt.Errorf("delete old highlights: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO highlights (talker, kind, event_date, title, description, metric_value, extra_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare insert highlight: %w", err)
	}
	defer stmt.Close()

	for _, e := range events {
		if _, err := stmt.ExecContext(ctx, talker, e.Kind, e.EventDate, e.Title, e.Description, e.MetricValue, e.ExtraJSON); err != nil {
			return fmt.Errorf("insert highlight (%s/%s): %w", e.Kind, e.EventDate, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save highlights: %w", err)
	}
	return nil
}

// GetHighlights returns all stored highlights for a talker.
func (s *Store) GetHighlights(ctx context.Context, talker string) ([]HighlightEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, talker, kind, event_date, title, description, metric_value, COALESCE(extra_json, ''), created_at
		FROM highlights
		WHERE talker = ?
		ORDER BY id ASC
	`, talker)
	if err != nil {
		return nil, fmt.Errorf("query highlights: %w", err)
	}
	defer rows.Close()

	var events []HighlightEvent
	for rows.Next() {
		var e HighlightEvent
		if err := rows.Scan(&e.ID, &e.Talker, &e.Kind, &e.EventDate, &e.Title, &e.Description, &e.MetricValue, &e.ExtraJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan highlight: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate highlights: %w", err)
	}
	return events, nil
}

// FindOrigin returns the timestamps of the earliest and latest message, along with the total count.
func (s *Store) FindOrigin(ctx context.Context, talker string) (minTs, maxTs int64, totalCount int, err error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MIN(ts), 0), COALESCE(MAX(ts), 0), COUNT(*)
		FROM messages
		WHERE talker = ?
	`, talker)
	if err := row.Scan(&minTs, &maxTs, &totalCount); err != nil {
		return 0, 0, 0, fmt.Errorf("find origin: %w", err)
	}
	return minTs, maxTs, totalCount, nil
}

// FindDistinctDays returns all distinct days (YYYY-MM-DD in UTC+8) with messages in ascending order.
func (s *Store) FindDistinctDays(ctx context.Context, talker string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT strftime('%Y-%m-%d', ts/1000, 'unixepoch', '+8 hours') as day
		FROM messages
		WHERE talker = ?
		ORDER BY day ASC
	`, talker)
	if err != nil {
		return nil, fmt.Errorf("find distinct days: %w", err)
	}
	defer rows.Close()

	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("scan distinct day: %w", err)
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate distinct days: %w", err)
	}
	return days, nil
}

// FindPeakDays returns the top N days with the highest message volume.
func (s *Store) FindPeakDays(ctx context.Context, talker string, limit int) ([]PeakDayRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', ts/1000, 'unixepoch', '+8 hours') as day,
		       COUNT(*) as total_msgs,
		       COALESCE(SUM(CASE WHEN is_self = 1 THEN 1 ELSE 0 END), 0) as self_msgs,
		       COALESCE(SUM(CASE WHEN is_self = 0 THEN 1 ELSE 0 END), 0) as partner_msgs
		FROM messages
		WHERE talker = ?
		GROUP BY day
		ORDER BY total_msgs DESC
		LIMIT ?
	`, talker, limit)
	if err != nil {
		return nil, fmt.Errorf("find peak days: %w", err)
	}
	defer rows.Close()

	var peaks []PeakDayRow
	for rows.Next() {
		var p PeakDayRow
		if err := rows.Scan(&p.Day, &p.TotalMsgs, &p.SelfMsgs, &p.PartnerMsgs); err != nil {
			return nil, fmt.Errorf("scan peak day: %w", err)
		}
		peaks = append(peaks, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peak days: %w", err)
	}
	return peaks, nil
}

// FindLateNights returns the top N days with late-night (00:00-05:00 CST) messages.
func (s *Store) FindLateNights(ctx context.Context, talker string, limit int) ([]LateNightRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', ts/1000, 'unixepoch', '+8 hours') as day,
		       COUNT(*) as late_msgs
		FROM messages
		WHERE talker = ?
		  AND CAST(strftime('%H', ts/1000, 'unixepoch', '+8 hours') AS INTEGER) < 5
		GROUP BY day
		HAVING late_msgs >= 5
		ORDER BY late_msgs DESC
		LIMIT ?
	`, talker, limit)
	if err != nil {
		return nil, fmt.Errorf("find late nights: %w", err)
	}
	defer rows.Close()

	var nights []LateNightRow
	for rows.Next() {
		var n LateNightRow
		if err := rows.Scan(&n.Day, &n.LateMsgs); err != nil {
			return nil, fmt.Errorf("scan late night: %w", err)
		}
		nights = append(nights, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate late nights: %w", err)
	}
	return nights, nil
}
