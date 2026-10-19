// Package store owns the SQLite database: it opens the file with the pragmas
// the design fixes, owns the single writer connection, and creates the schema.
// No package above this one writes SQL against the database directly.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store is the single-connection handle to the chat database. Exactly one
// writer connection exists for the whole process: the pragmas below are
// applied per connection, and a second connection would both race the writer
// and lose its own copy of busy_timeout.
type Store struct {
	db *sql.DB
}

// DSN builds the SQLite URI for path, which is made absolute first.
//
// The path is percent-escaped rather than concatenated raw, and that is a
// correctness requirement rather than a style choice. Measured against
// modernc.org/sqlite v1.58.0 on Windows:
//
//   - url.URL{Scheme: "file", Path: p} fails outright: the drive letter is
//     parsed as a URI authority ("invalid uri authority: C:").
//   - Every raw concatenation ("file:" + p + "?…") opens successfully on an
//     ordinary, spaced, or Chinese path, but for a path containing '#' it
//     silently resolves to a DIFFERENT file — the URI parser treats the '#' as
//     a fragment delimiter and truncates. Opening the wrong database without
//     an error is far worse than failing to open one.
//   - Only the escaped form resolves to the intended file on all of those.
//
// store_test.go asserts the resolved file through PRAGMA database_list, so a
// regression to any of the above shapes fails loudly.
func DSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path %q: %w", path, err)
	}
	query := url.Values{}
	for _, pragma := range []string{
		"journal_mode(WAL)",
		"busy_timeout(10000)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
	} {
		query.Add("_pragma", pragma)
	}
	escaped := (&url.URL{Path: filepath.ToSlash(abs)}).EscapedPath()
	return "file:" + escaped + "?" + query.Encode(), nil
}

// Open opens dsn and pins the writer connection. The serialization of writes
// is set here rather than left to callers: one connection and no more is what
// makes SQLITE_BUSY structurally impossible instead of merely unlikely.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dsn, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite %q: %w", dsn, err)
	}
	return &Store{db: db}, nil
}

// DB exposes the handle for the tests and for packages in this one. It is not
// a general escape hatch: SQL outside this package still belongs here.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the writer connection.
func (s *Store) Close() error { return s.db.Close() }

// syncedDirMarkers are the on-disk directory names of the cloud sync clients a
// plaintext chat database must never live under. Matching the product names
// alone would miss the actual folders: Nutstore creates "Nutstore Files" and
// Baidu creates "BaiduNetdiskDownload".
var syncedDirMarkers = []string{
	"onedrive",
	"nutstore",
	"baidunetdisk",
	"坚果云",
	"百度网盘",
}

// syncedEnvVars are the environment variables that locate a sync root, which
// catches a client installed under a directory named in neither language.
var syncedEnvVars = []string{
	"OneDrive",
	"OneDriveCommercial",
	"OneDriveConsumer",
	"Nutstore",
}

// RejectSyncedPath refuses a database path that sits in a cloud-synced folder.
//
// Windows 11's Known Folder Move silently redirects Documents into OneDrive,
// so a database created at a plausible-looking path can end up uploaded
// without the user ever choosing that. The chat log is the most sensitive
// thing this project touches, so the path is checked before the file is
// created rather than reported after.
func RejectSyncedPath(path string) error {
	normalized := strings.ToLower(filepath.ToSlash(path))
	for _, marker := range syncedDirMarkers {
		if !strings.Contains(normalized, marker) {
			continue
		}
		return fmt.Errorf("database path %q is inside a cloud-synced folder (%q): the plaintext chat database must not be uploaded by a sync client — move it under %s and retry", path, marker, defaultPathHint())
	}
	for _, name := range syncedEnvVars {
		root := os.Getenv(name)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		prefix := strings.ToLower(filepath.ToSlash(absRoot)) + "/"
		if strings.HasPrefix(normalized, prefix) {
			return fmt.Errorf("database path %q is inside the sync root %s=%q: the plaintext chat database must not be uploaded by a sync client — move it under %s and retry", path, name, root, defaultPathHint())
		}
	}
	return nil
}

// defaultPathHint names the local-only directory the CLI defaults to, so the
// rejection message tells the user where to go instead of only what not to do.
func defaultPathHint() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "lover-instance")
}
