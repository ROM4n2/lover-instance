package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// openTemp opens a store on a fresh file under dir and closes it on cleanup.
func openTemp(t *testing.T, dir, name string) *Store {
	t.Helper()
	dsn, err := DSN(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dsn, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mainDBFile reads back the file SQLite actually opened. Asserting on this,
// rather than on "Open did not return an error", is what makes the DSN tests
// meaningful: a DSN that truncates the path at a URI delimiter opens *a*
// database quite happily, just not the one that was asked for.
func mainDBFile(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query("PRAGMA database_list")
	if err != nil {
		t.Fatalf("PRAGMA database_list: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			t.Fatalf("scan database_list: %v", err)
		}
		if name == "main" {
			return file
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate database_list: %v", err)
	}
	t.Fatal("PRAGMA database_list reported no \"main\" database")
	return ""
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// TestDSN_ResolvesToTheIntendedFile covers the measured failure modes of DSN
// construction on Windows. Building the DSN through url.URL's Path field makes
// the drive letter a URI authority and the driver rejects it outright; and
// every raw concatenation of the path silently opens a different file once the
// path contains '#'. Both are caught here, because the second one reports
// success.
func TestDSN_ResolvesToTheIntendedFile(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name string
		dir  string
	}{
		{"plain", ""},
		{"with space", "a b"},
		{"with chinese", "聊天记录"},
		{"with space and chinese", "聊 天"},
		{"with hash", "a#b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := base
			if tc.dir != "" {
				dir = filepath.Join(base, tc.dir)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %q: %v", dir, err)
				}
			}
			want := filepath.Join(dir, "data.db")

			s := openTemp(t, dir, "data.db")
			if got := mainDBFile(t, s); !samePath(got, want) {
				t.Errorf("opened %q, want %q — the DSN resolved to a different file", got, want)
			}
		})
	}
}

func TestOpen_AppliesRequiredPragmas(t *testing.T) {
	s := openTemp(t, t.TempDir(), "data.db")

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}

	var busyTimeout int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout < 10000 {
		t.Errorf("busy_timeout = %d, want >= 10000", busyTimeout)
	}

	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}

// TestOpen_PinsSingleWriterConnection guards the choice that makes SQLITE_BUSY
// structurally impossible. If Open stopped pinning it, a caller could open a
// second connection, and the per-connection pragmas above would not follow it.
func TestOpen_PinsSingleWriterConnection(t *testing.T) {
	s := openTemp(t, t.TempDir(), "data.db")
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestMigrate_CreatesEveryTableAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir(), "data.db")

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error: %v — migration must be idempotent", err)
	}

	for _, table := range tables {
		var name string
		err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q is missing after Migrate", table)
			continue
		}
		if err != nil {
			t.Fatalf("query sqlite_master for %q: %v", table, err)
		}
	}

	for _, index := range []string{"uq_msg_source", "uq_msg_natural", "uq_jobs_active", "idx_msg_talker_ts"} {
		var name string
		err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("index %q is missing after Migrate", index)
			continue
		}
		if err != nil {
			t.Fatalf("query sqlite_master for index %q: %v", index, err)
		}
	}
}

// TestMigrate_DailyStatsCarriesFirstAndLastSpeaker pins the one deliberate
// addition to the spec's DDL: the M1 indicator list asks for who opened and who
// closed each day, and the spec's daily_stats had no column for it.
func TestMigrate_DailyStatsCarriesFirstAndLastSpeaker(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir(), "data.db")
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	for _, column := range []string{"first_is_self", "last_is_self"} {
		rows, err := s.db.QueryContext(ctx, `SELECT `+column+` FROM daily_stats LIMIT 1`)
		if err != nil {
			t.Errorf("daily_stats has no %q column: %v", column, err)
			continue
		}
		_ = rows.Close()
	}
}

func TestRejectSyncedPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"local code directory", `D:\Code\lover-instance\data.db`, false},
		{"appdata local", `C:\Users\someone\AppData\Local\lover-instance\data.db`, false},
		{"onedrive redirect", `C:\Users\someone\OneDrive\Documents\data.db`, true},
		{"onedrive business suffix", `C:\Users\someone\OneDrive - Contoso\data.db`, true},
		{"nutstore actual folder name", `C:\Users\someone\Nutstore Files\data.db`, true},
		{"baidu actual folder name", `C:\Users\someone\BaiduNetdiskDownload\data.db`, true},
		{"nutstore chinese name", `D:\坚果云\data.db`, true},
		{"baidu chinese name", `D:\百度网盘\data.db`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectSyncedPath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("RejectSyncedPath(%q) = nil, want an error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("RejectSyncedPath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// TestRejectSyncedPath_HonoursSyncRootEnvVar covers the case a path match
// cannot: a client installed under a directory named in neither language but
// still published through the environment.
func TestRejectSyncedPath_HonoursSyncRootEnvVar(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OneDrive", root)

	if err := RejectSyncedPath(filepath.Join(root, "docs", "data.db")); err == nil {
		t.Error("RejectSyncedPath() = nil for a path under the OneDrive env root, want an error")
	}
	if err := RejectSyncedPath(filepath.Join(t.TempDir(), "data.db")); err != nil {
		t.Errorf("RejectSyncedPath() = %v for an unrelated temp path, want nil", err)
	}
}
