package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Job is one analysis run's persisted state.
//
// It lives in the store because it is a row. ADR-0002 makes analysis_jobs the
// only authority on what happened to a run — an in-memory copy would go stale
// the moment a second process (the CLI and, later, the API server) looked at
// the same database, and the two copies would then disagree about whether a
// conversation was already being analyzed.
//
// The status values are enumerated here only as a vocabulary: jobStatuses is
// the set of values the column may hold, so a caller that skips package jobs
// cannot write "Done" or an empty string into it. Which of them may follow
// which is still the state machine's decision in package jobs; the store's
// contribution is the guarded UPDATEs that make an edge illegal to write.
type Job struct {
	ID           int64
	Talker       string
	Kind         string
	ModelVersion string
	Status       string
	Total        int
	Done         int
	Error        string
	CreatedAt    string
	UpdatedAt    string
}

// jobColumns is the read shape every query in this file shares. The two
// nullable columns are coalesced rather than scanned into sql.NullString: for a
// string a person may be shown there is no meaningful difference between NULL
// and ”, and a forgotten .Valid check is how NULLs become empty output later.
const jobColumns = `id, talker, kind, COALESCE(model_version, ''), status, total, done, COALESCE(error, ''), created_at, updated_at`

// jobStatuses is every value analysis_jobs.status is allowed to hold: the six
// states the state machine in package jobs names. It is a vocabulary, not an
// edge list — the edges are the from arguments the caller supplies.
//
// It exists because the WHERE guard constrains the source state only. Without a
// check on the target, a typo at a call site writes a status nothing can read
// ("Done", "cancelled", or the empty string, which SQLite accepts happily), and
// every later query silently stops matching the row.
var jobStatuses = map[string]bool{
	"queued":      true,
	"running":     true,
	"done":        true,
	"failed":      true,
	"canceled":    true,
	"interrupted": true,
}

// IsUniqueViolation reports whether err is SQLite refusing a write because a
// uniqueness constraint already holds — for this table, uq_jobs_active.
//
// The test is on the driver's result code, never on its message: modernc's
// driver enables extended result codes on every connection, so a partial-index
// conflict arrives as *sqlite.Error with Code() == SQLITE_CONSTRAINT_UNIQUE
// (2067, the extended form of SQLITE_CONSTRAINT=19), and matching text would
// break on any wording change as well as being fooled by a caller quoting it.
//
// Everything else — a full disk, an I/O error, a closed handle — is answered
// false on purpose. A single-flight check that treats every failed insert as
// "someone else is running" hides the real fault behind a job that does not
// exist.
func IsUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// jobScanner is what both *sql.Row and *sql.Rows provide, so one scan helper
// serves the single-row and the list-shaped reads.
type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.Talker, &job.Kind, &job.ModelVersion,
		&job.Status, &job.Total, &job.Done, &job.Error, &job.CreatedAt, &job.UpdatedAt)
	return job, err
}

// InsertJob records a job in the queued state and returns it as stored.
//
// It does not check for an existing active job first. uq_jobs_active is what
// decides, and it decides at insert: a check followed by an insert has a window
// between the two in which both callers see an empty slot, which is the double
// run this table exists to prevent. The caller handles the conflict by adopting
// the winner's row.
//
// The talker key never appears in an error string. The CLI prints these to
// stderr and the project's rule is that conversation keys do not travel in
// output; the kind is enough to identify the job.
func (s *Store) InsertJob(ctx context.Context, talker, kind, modelVersion string) (Job, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO analysis_jobs (talker, kind, model_version, status, total, done, error, created_at, updated_at)
VALUES (?, ?, ?, 'queued', 0, 0, NULL, datetime('now'), datetime('now'))
`, talker, kind, modelVersion)
	if err != nil {
		return Job{}, fmt.Errorf("insert job %q: %w", kind, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Job{}, fmt.Errorf("read the id of the new job %q: %w", kind, err)
	}
	// Read the row back rather than echoing what was sent: created_at and
	// updated_at are the database's clock, and a caller that compares them
	// against another table's rows has to be comparing the same clock.
	job, err := s.JobByID(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// ActiveJob returns the job currently occupying the (talker, kind) slot, which
// is exactly what uq_jobs_active covers.
func (s *Store) ActiveJob(ctx context.Context, talker, kind string) (Job, bool, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, `
SELECT `+jobColumns+`
  FROM analysis_jobs
 WHERE talker = ? AND kind = ? AND status IN ('queued','running')
 LIMIT 1
`, talker, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("read the active job %q: %w", kind, err)
	}
	return job, true, nil
}

// JobByID reads one job. A missing id is reported as an error wrapping
// sql.ErrNoRows, so a caller can tell "no such job" from "the read failed".
func (s *Store) JobByID(ctx context.Context, id int64) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM analysis_jobs WHERE id = ?`, id))
	if err != nil {
		return Job{}, fmt.Errorf("read job %d: %w", id, err)
	}
	return job, nil
}

// SetJobProgress records how far a run has got.
//
// Only a queued or running job accepts progress, and a job that has already
// finished is an error rather than a silent no-op. Silently ignoring the update
// would let a worker believe its checkpoint landed while the row says the run
// got nowhere, and that is the state a reader would then be shown as truth.
//
// done and total are absolute counts, never percentages: percentages go
// backwards when more messages are imported.
func (s *Store) SetJobProgress(ctx context.Context, id int64, done, total int) error {
	if done < 0 || total < 0 {
		return fmt.Errorf("record progress for job %d: done=%d total=%d must not be negative", id, done, total)
	}
	if done > total {
		return fmt.Errorf("record progress for job %d: done=%d exceeds total=%d", id, done, total)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE analysis_jobs
   SET done = ?, total = ?, updated_at = datetime('now')
 WHERE id = ? AND status IN ('queued','running')
`, done, total, id)
	if err != nil {
		return fmt.Errorf("record progress for job %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("record progress for job %d: %w", id, err)
	}
	if affected == 0 {
		// The guard refused the write. Reading the row afterwards only explains
		// why; it does not decide anything, which is why it is allowed here.
		return fmt.Errorf("record progress for job %d: %w", id, s.jobNotOpen(ctx, id))
	}
	return nil
}

// TransitionJob moves a job from one of from to to, and reports whether a row
// actually moved.
//
// The guard is the WHERE clause. A SELECT followed by an unconditional UPDATE
// would let two callers both see a legal source state and then both write, and
// the second write would be a transition nobody checked. Here the database
// decides, and RowsAffected==0 means the move was not legal instead of meaning
// "nothing to do".
//
// The target is checked against jobStatuses before the statement is built: the
// WHERE clause cannot see it, so without that check the one thing this function
// could still write is a status that does not exist.
//
// An empty errMsg stores NULL: a run that closed cleanly must not keep a
// previous attempt's failure on record.
func (s *Store) TransitionJob(ctx context.Context, id int64, from []string, to, errMsg string) (bool, error) {
	if !jobStatuses[to] {
		return false, fmt.Errorf("move job %d to %q: %q is not a status analysis_jobs may hold", id, to, to)
	}
	if len(from) == 0 {
		return false, fmt.Errorf("move job %d to %q: no source status was given, so no row could ever match", id, to)
	}
	placeholders := make([]string, 0, len(from))
	args := make([]any, 0, len(from)+3)
	var storedError any
	if errMsg != "" {
		storedError = errMsg
	}
	args = append(args, to, storedError, id)
	for _, status := range from {
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE analysis_jobs
   SET status = ?, error = ?, updated_at = datetime('now')
 WHERE id = ? AND status IN (`+strings.Join(placeholders, ", ")+`)
`, args...)
	if err != nil {
		return false, fmt.Errorf("move job %d to %q: %w", id, to, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("move job %d to %q: %w", id, to, err)
	}
	return affected > 0, nil
}

// InterruptRunning marks the runs a dead process left behind and returns how
// many it changed.
//
// Only running jobs are touched: a queued job has no owner to lose, and a
// finished one has an outcome that must not be rewritten. The rows it produces
// are outside uq_jobs_active on purpose — that is what frees the slot for the
// next run while leaving the interrupted job available to be resumed.
func (s *Store) InterruptRunning(ctx context.Context, talker, kind string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE analysis_jobs
   SET status = 'interrupted', updated_at = datetime('now')
 WHERE talker = ? AND kind = ? AND status = 'running'
`, talker, kind)
	if err != nil {
		return 0, fmt.Errorf("interrupt the running job %q: %w", kind, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("interrupt the running job %q: %w", kind, err)
	}
	return affected, nil
}

// jobNotOpen explains an UPDATE that required a queued or running job and
// matched no row: either the job is gone, or it has already finished. Naming
// the status is the difference between a message a reader can act on and one
// that sends them back to the source.
func (s *Store) jobNotOpen(ctx context.Context, id int64) error {
	job, err := s.JobByID(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("job %d is %q, which is a final state", id, job.Status)
}
