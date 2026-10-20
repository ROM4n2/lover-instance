package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaSQL is the full M1 schema from the design spec's data model, with one
// addition: daily_stats carries first_is_self / last_is_self because the M1
// indicator list asks for "the daily first and last message's speaker" and the
// spec's DDL had nowhere to put it. Every statement is IF NOT EXISTS, so
// Migrate is idempotent and safe to run on every startup; there is no
// migration history table to keep, because this database is created fresh by
// the CLI and its contents are fully re-derivable from the export.
//
// Timestamps are epoch milliseconds. Day and hour buckets are NOT computed
// here: bucketing is fixed to UTC+8 and lives in the metrics layer, where the
// 'unixepoch','+8 hours' modifiers are applied.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL,
  source_msg_id TEXT,
  ts INTEGER NOT NULL,
  talker TEXT NOT NULL,
  sender TEXT NOT NULL,
  is_self INTEGER NOT NULL,
  is_chatroom INTEGER NOT NULL DEFAULT 0,
  msg_type INTEGER NOT NULL,
  sub_type INTEGER NOT NULL DEFAULT 0,
  content TEXT,
  media_ref TEXT,
  content_sha1 TEXT,
  raw_json TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_msg_source ON messages(source, source_msg_id) WHERE source_msg_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_msg_natural ON messages(talker, sender, ts, msg_type, content_sha1) WHERE source_msg_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_msg_talker_ts ON messages(talker, ts, id);

CREATE TABLE IF NOT EXISTS contacts (
  wxid TEXT PRIMARY KEY, nickname TEXT, remark TEXT, is_self INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sentiment (
  msg_id INTEGER NOT NULL,
  model_version TEXT NOT NULL,
  polarity REAL, emotion TEXT, intensity REAL,
  scored_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY(msg_id, model_version)
);

CREATE TABLE IF NOT EXISTS daily_stats (
  talker TEXT NOT NULL, day TEXT NOT NULL, model_version TEXT NOT NULL,
  self_msgs INTEGER, partner_msgs INTEGER, self_chars INTEGER, partner_chars INTEGER,
  first_is_self INTEGER, last_is_self INTEGER,
  avg_polarity REAL, pos_count INTEGER, neg_count INTEGER, updated_at TEXT NOT NULL,
  PRIMARY KEY(talker, day, model_version)
);

CREATE TABLE IF NOT EXISTS activity_heatmap (
  talker TEXT NOT NULL, weekday INTEGER NOT NULL, hour INTEGER NOT NULL,
  self_count INTEGER, partner_count INTEGER,
  PRIMARY KEY(talker, weekday, hour)
);

CREATE TABLE IF NOT EXISTS conversations (
  id INTEGER PRIMARY KEY AUTOINCREMENT, talker TEXT NOT NULL,
  start_ts INTEGER NOT NULL, end_ts INTEGER NOT NULL,
  initiator TEXT, msg_count INTEGER, UNIQUE(talker, start_ts)
);

CREATE TABLE IF NOT EXISTS word_freq (
  talker TEXT NOT NULL, period TEXT NOT NULL, is_self INTEGER NOT NULL,
  word TEXT NOT NULL, cnt INTEGER NOT NULL,
  PRIMARY KEY(talker, period, is_self, word)
);

-- word_freq_v2 carries the model version the way sentiment and daily_stats do.
-- The version is part of the identity of a count, not decoration: the counts
-- are accumulated with "cnt = cnt + excluded.cnt", so a rescan under a second
-- model version would add its tokens onto the first version's rows and double
-- every word cloud figure. Keyed by version, each model accumulates its own.
--
-- The M1 table above is kept and never dropped or rewritten: it is read-only
-- legacy, and the only copy of counts a migration could not attribute.
CREATE TABLE IF NOT EXISTS word_freq_v2 (
  talker TEXT NOT NULL, period TEXT NOT NULL, is_self INTEGER NOT NULL,
  model_version TEXT NOT NULL, word TEXT NOT NULL, cnt INTEGER NOT NULL,
  PRIMARY KEY(talker, period, is_self, model_version, word)
);

CREATE TABLE IF NOT EXISTS analysis_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  talker TEXT NOT NULL, kind TEXT NOT NULL,
  model_version TEXT,
  status TEXT NOT NULL DEFAULT 'queued',
  total INTEGER NOT NULL DEFAULT 0, done INTEGER NOT NULL DEFAULT 0,
  error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_jobs_active ON analysis_jobs(talker, kind)
  WHERE status IN ('queued','running');

CREATE TABLE IF NOT EXISTS ingest_state (
  source TEXT NOT NULL, talker TEXT NOT NULL,
  last_ts INTEGER, last_msg_id TEXT, updated_at TEXT,
  PRIMARY KEY(source, talker)
);
`

// tables is the set Migrate is expected to create. It exists so a test can
// assert the schema actually landed rather than that the script merely ran.
var tables = []string{
	"messages",
	"contacts",
	"sentiment",
	"daily_stats",
	"activity_heatmap",
	"conversations",
	"word_freq",
	"word_freq_v2",
	"analysis_jobs",
	"ingest_state",
}

// Migrate creates every table and index, then carries the legacy word counts
// forward into word_freq_v2. It is idempotent: running it twice leaves the same
// schema and the same row contents and reports no error, so the CLI can call it
// unconditionally on startup.
//
// The schema and the copy run in one transaction so an interrupted upgrade
// cannot leave v2 half-populated under a schema that says the work is done.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	if err := migrateWordFreqToV2(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrate: %w", err)
	}
	return nil
}

// migrateWordFreqToV2 copies the M1 word counts into the versioned table.
//
// The legacy table has no model_version column, so its rows can only be
// attributed when the sentiment table names exactly one version: that is the
// version whose scores produced those tokens. The copy is skipped when the
// sentiment table holds no rows (nothing was ever scored, so there is no
// version to name) and when it holds two or more distinct versions (the counts
// cannot be split, and attributing them to one of the candidates would print a
// word cloud for a model that never produced it — the very mislabelling this
// table exists to prevent). An empty word_freq_v2 for those databases is the
// honest outcome; the legacy table stays readable and untouched.
//
// Idempotence comes from the WHERE NOT EXISTS: Migrate runs on every startup,
// and a row already carried over is not inserted again. Copying without it
// would duplicate rows on the first rerun and, because the counts are meant to
// be accumulated elsewhere, would be indistinguishable from the doubling this
// migration removes.
func migrateWordFreqToV2(ctx context.Context, tx *sql.Tx) error {
	versions, err := distinctSentimentVersions(ctx, tx)
	if err != nil {
		return err
	}
	if len(versions) != 1 {
		return nil
	}
	version := versions[0]

	_, err = tx.ExecContext(ctx, `
INSERT INTO word_freq_v2 (talker, period, is_self, model_version, word, cnt)
SELECT f.talker, f.period, f.is_self, ?, f.word, f.cnt
  FROM word_freq f
 WHERE NOT EXISTS (
       SELECT 1 FROM word_freq_v2 v
        WHERE v.talker = f.talker AND v.period = f.period
          AND v.is_self = f.is_self AND v.model_version = ? AND v.word = f.word
      )
`, version, version)
	if err != nil {
		return fmt.Errorf("copy word_freq into word_freq_v2: %w", err)
	}
	return nil
}

// distinctSentimentVersions lists the model versions the sentiment table
// actually holds, which is what decides whether the legacy counts can be
// attributed to one of them.
func distinctSentimentVersions(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT model_version FROM sentiment`)
	if err != nil {
		return nil, fmt.Errorf("read sentiment model versions: %w", err)
	}
	defer rows.Close()

	var versions []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan sentiment model version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sentiment model versions: %w", err)
	}
	return versions, nil
}
