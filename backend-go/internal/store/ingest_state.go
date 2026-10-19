package store

import (
	"context"
	"database/sql"
	"fmt"
)

// IngestState reports the watermark for one source's conversation: the newest
// message timestamp already imported, or ok=false when nothing has been.
//
// The watermark is an epoch-millisecond timestamp, never a sequence number.
// The data source has no seq column at all, and a timestamp used as a
// "resume here" pointer is what the project forbids.
func (s *Store) IngestState(ctx context.Context, source, talker string) (int64, bool, error) {
	var lastTs sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_ts FROM ingest_state WHERE source = ? AND talker = ?`, source, talker).Scan(&lastTs)
	if err == sql.ErrNoRows || !lastTs.Valid {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read ingest state for source %q: %w", source, err)
	}
	return lastTs.Int64, true, nil
}

// SaveIngestState records the watermark after an import.
//
// It refuses to move a watermark backwards: a later run that only sees older
// messages (a re-import of an old export, or a window that happens to be
// entirely behind the current one) would otherwise rewind the pointer, and the
// next 60-second rescan would then re-read a stretch of history that has
// already been ingested.
func (s *Store) SaveIngestState(ctx context.Context, source, talker string, lastTs int64, lastMsgID string) error {
	current, ok, err := s.IngestState(ctx, source, talker)
	if err != nil {
		return err
	}
	if ok && current > lastTs {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO ingest_state (source, talker, last_ts, last_msg_id, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT (source, talker) DO UPDATE SET
  last_ts = excluded.last_ts,
  last_msg_id = excluded.last_msg_id,
  updated_at = excluded.updated_at
`, source, talker, lastTs, lastMsgID)
	if err != nil {
		return fmt.Errorf("save ingest state for source %q: %w", source, err)
	}
	return nil
}
