package store

import (
	"context"
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
	"analysis_jobs",
	"ingest_state",
}

// Migrate creates every table and index. It is idempotent: running it twice
// leaves the same schema and reports no error, so the CLI can call it
// unconditionally on startup.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	return nil
}