package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"lover-instance/backend-go/internal/model"
)

// ContentSHA1 is the normalized hash of a message body: surrounding
// whitespace is trimmed first, so a re-export that pads a line does not read
// as a content change. It is stored for every row, though the natural-key
// index that would consume it stays dormant for this data source (see D3).
func ContentSHA1(content string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}

// InsertResult accounts for every message handed to UpsertMessages. The three
// counters always sum to the input length: a row that was neither inserted,
// refreshed, nor recognized as unchanged would mean data went missing, which
// is the failure this pipeline exists to prevent.
type InsertResult struct {
	Inserted int
	Skipped  int
	Updated  int
}

// insertMessageSQL adds a row only when its identity is new. The conflict
// target repeats the partial index's WHERE clause because SQLite requires a
// conflict target to match the index definition exactly, partial predicate
// included.
const insertMessageSQL = `
INSERT INTO messages
  (source, source_msg_id, ts, talker, sender, is_self, is_chatroom, msg_type, sub_type, content, media_ref, content_sha1, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL)
ON CONFLICT (source, source_msg_id) WHERE source_msg_id IS NOT NULL DO NOTHING
`

// refreshMessageSQL rewrites the body of an existing row, but only when the
// body actually differs. WeChat replaces a message's text when it is revoked,
// so an incremental import must be able to carry that change; limiting the
// UPDATE to changed rows is what keeps a repeat import byte-for-byte inert.
// `IS NOT` is used rather than `<>` because the latter is NULL-unsafe.
const refreshMessageSQL = `
UPDATE messages
   SET content = ?, content_sha1 = ?
 WHERE source = ? AND source_msg_id = ?
   AND content IS NOT ?
`

// UpsertMessages writes a batch in one transaction.
//
// The identity is msg.UniqueKey, which model.ParseRaw guarantees non-empty: a
// message whose key were empty would collide with every other empty-keyed
// message on the unique index and silently collapse distinct rows into one.
// There is deliberately no second branch keying on a content hash — the
// natural-key index exists as a schema provision for a future source with no
// native message id, and this source always has one.
func (s *Store) UpsertMessages(ctx context.Context, source string, msgs []model.CanonicalMessage) (InsertResult, error) {
	var result InsertResult
	if len(msgs) == 0 {
		return result, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InsertResult{}, fmt.Errorf("begin messages batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	insert, err := tx.PrepareContext(ctx, insertMessageSQL)
	if err != nil {
		return InsertResult{}, fmt.Errorf("prepare insert: %w", err)
	}
	defer insert.Close()

	refresh, err := tx.PrepareContext(ctx, refreshMessageSQL)
	if err != nil {
		return InsertResult{}, fmt.Errorf("prepare refresh: %w", err)
	}
	defer refresh.Close()

	// Rows that collided with an existing identity, to be checked for a body
	// change once the insert pass has run.
	var collided []model.CanonicalMessage
	for i, msg := range msgs {
		if strings.TrimSpace(msg.UniqueKey) == "" {
			return InsertResult{}, fmt.Errorf("message %d in source %q has an empty unique key: refusing to write a row without identity", i, source)
		}
		affected, err := execCount(insert, ctx,
			source, msg.UniqueKey, msg.TimestampMs, msg.Talker, msg.Sender,
			boolToInt(msg.IsSelf), boolToInt(groupFlag(msg.Extra)), msg.Type, msg.SubType,
			msg.Content, ContentSHA1(msg.Content))
		if err != nil {
			return InsertResult{}, fmt.Errorf("insert message %d (key %s): %w", i, msg.UniqueKey, err)
		}
		if affected == 1 {
			result.Inserted++
			continue
		}
		collided = append(collided, msg)
	}

	for _, msg := range collided {
		hash := ContentSHA1(msg.Content)
		affected, err := execCount(refresh, ctx, msg.Content, hash, source, msg.UniqueKey, msg.Content)
		if err != nil {
			return InsertResult{}, fmt.Errorf("refresh message (key %s): %w", msg.UniqueKey, err)
		}
		if affected == 1 {
			result.Updated++
			continue
		}
		result.Skipped++
	}

	if total := result.Inserted + result.Skipped + result.Updated; total != len(msgs) {
		return InsertResult{}, fmt.Errorf("batch accounting is off: inserted %d + skipped %d + updated %d = %d, but %d messages were submitted", result.Inserted, result.Skipped, result.Updated, total, len(msgs))
	}
	if err := tx.Commit(); err != nil {
		return InsertResult{}, fmt.Errorf("commit messages batch: %w", err)
	}
	return result, nil
}

// CountMessages reports how many messages this talker already has stored. The
// ingest guard uses it to tell "this export added nothing new" apart from
// "this talker was never imported at all".
func (s *Store) CountMessages(ctx context.Context, talker string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE talker = ?`, talker).Scan(&count); err != nil {
		return 0, fmt.Errorf("count messages for talker: %w", err)
	}
	return count, nil
}

// ListTalkers returns the distinct conversation keys present in the database,
// in a stable order. It is how the CLI learns what it could analyze when the
// user did not name a conversation.
func (s *Store) ListTalkers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT talker FROM messages ORDER BY talker`)
	if err != nil {
		return nil, fmt.Errorf("list talkers: %w", err)
	}
	defer rows.Close()
	var talkers []string
	for rows.Next() {
		var talker string
		if err := rows.Scan(&talker); err != nil {
			return nil, fmt.Errorf("scan talker: %w", err)
		}
		talkers = append(talkers, talker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate talkers: %w", err)
	}
	return talkers, nil
}

// execCount runs one prepared statement and reports how many rows it touched.
func execCount(stmt *sql.Stmt, ctx context.Context, args ...any) (int64, error) {
	result, err := stmt.ExecContext(ctx, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// boolToInt renders a Go flag for SQLite, which has no boolean type.
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// groupFlag reads the exporter's group-chat marker out of Extra. It is a
// per-conversation constant, so it is carried for display only and never for
// identity.
func groupFlag(extra map[string]any) bool {
	flag, ok := extra["is_group_chat"].(bool)
	return ok && flag
}
