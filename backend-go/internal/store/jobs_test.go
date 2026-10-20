package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// The job tests use migratedStore from messages_test.go: an unmigrated database
// has no analysis_jobs table, and a test that raced the migration would report
// a missing table as a broken query.

func TestInsertJob_StartsQueuedWithTheDatabaseClock(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "lexicon-hownet-v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if job.ID == 0 {
		t.Error("InsertJob() returned id 0, want the rowid SQLite assigned")
	}
	if job.Status != "queued" {
		t.Errorf("status = %q, want %q: a new job has not started yet", job.Status, "queued")
	}
	if job.Total != 0 || job.Done != 0 {
		t.Errorf("total/done = %d/%d, want 0/0", job.Total, job.Done)
	}
	if job.Error != "" {
		t.Errorf("error = %q, want empty", job.Error)
	}
	if job.ModelVersion != "lexicon-hownet-v1" {
		t.Errorf("model_version = %q, want the version the caller named", job.ModelVersion)
	}
	// The timestamps must come back from the row, not from a Go clock: they are
	// written with SQL's datetime('now') everywhere else in this schema, and a
	// caller comparing them against another table's rows has to be comparing the
	// same clock.
	if !strings.HasPrefix(job.CreatedAt, "20") || !strings.HasPrefix(job.UpdatedAt, "20") {
		t.Errorf("created_at/updated_at = %q/%q, want the database's timestamps", job.CreatedAt, job.UpdatedAt)
	}
}

// TestInsertJob_RefusesASecondActiveJob is the store-level half of single
// flight: the partial unique index, not a process-local lock, is what makes a
// second run impossible.
func TestInsertJob_RefusesASecondActiveJob(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("first InsertJob() error: %v", err)
	}
	if _, err := s.InsertJob(ctx, "c", "sentiment", "v1"); err == nil {
		t.Error("second InsertJob() = nil error, want uq_jobs_active to refuse it")
	}
	// A different kind is a different slot: the index is on (talker, kind).
	if _, err := s.InsertJob(ctx, "c", "other-kind", "v1"); err != nil {
		t.Errorf("InsertJob() for another kind error: %v — the index must not span kinds", err)
	}
	// A finished job leaves the partial index, so the slot frees up.
	if _, err := s.TransitionJob(ctx, job.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{"running"}, "done", ""); err != nil {
		t.Fatalf("TransitionJob(running→done) error: %v", err)
	}
	if _, err := s.InsertJob(ctx, "c", "sentiment", "v1"); err != nil {
		t.Errorf("InsertJob() after the previous job finished error: %v — a terminal row must not hold the slot", err)
	}
}

func TestActiveJob_ReportsQueuedAndRunningOnly(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	if _, ok, err := s.ActiveJob(ctx, "c", "sentiment"); ok || err != nil {
		t.Errorf("ActiveJob() on an empty table = %v, %v; want not found and no error", ok, err)
	}

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if got, ok, err := s.ActiveJob(ctx, "c", "sentiment"); !ok || err != nil || got.ID != job.ID {
		t.Errorf("ActiveJob() = %+v, %v, %v; want the queued job %d", got, ok, err, job.ID)
	}

	if _, err := s.TransitionJob(ctx, job.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if got, ok, err := s.ActiveJob(ctx, "c", "sentiment"); !ok || err != nil || got.ID != job.ID {
		t.Errorf("ActiveJob() = %+v, %v, %v; want the running job %d", got, ok, err, job.ID)
	}

	if _, err := s.TransitionJob(ctx, job.ID, []string{"running"}, "done", ""); err != nil {
		t.Fatalf("TransitionJob(running→done) error: %v", err)
	}
	if got, ok, err := s.ActiveJob(ctx, "c", "sentiment"); ok || err != nil {
		t.Errorf("ActiveJob() = %+v, %v, %v; want not found once the job is done", got, ok, err)
	}
}

// TestTransitionJob_OnlyMovesWhenTheGuardMatches pins the shape the plan
// requires: a single guarded UPDATE whose RowsAffected answers whether the
// move happened, never a SELECT followed by an unconditional UPDATE.
func TestTransitionJob_OnlyMovesWhenTheGuardMatches(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	moved, err := s.TransitionJob(ctx, job.ID, []string{"queued", "running"}, "canceled", "")
	if err != nil {
		t.Fatalf("TransitionJob() error: %v", err)
	}
	if !moved {
		t.Fatal("TransitionJob(queued→canceled) = false, want true")
	}

	// canceled → done is not an edge: the guard matches no row.
	moved, err = s.TransitionJob(ctx, job.ID, []string{"running"}, "done", "")
	if err != nil {
		t.Fatalf("TransitionJob() on an illegal move error: %v, want no error and moved=false", err)
	}
	if moved {
		t.Error("TransitionJob(canceled→done) = true, want false")
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Status != "canceled" {
		t.Errorf("status = %q after a refused move, want %q: the guarded UPDATE must not have matched", after.Status, "canceled")
	}

	moved, err = s.TransitionJob(ctx, 9999, []string{"running"}, "done", "")
	if moved || err != nil {
		t.Errorf("TransitionJob(unknown id) = %v, %v; want false and no error", moved, err)
	}

	if _, err := s.TransitionJob(ctx, job.ID, nil, "done", ""); err == nil {
		t.Error("TransitionJob() with no source statuses = nil error, want a refusal: an empty IN list cannot match")
	}
}

// TestTransitionJob_RefusesAStatusTheSchemaNeverDefined is the store's half of
// "a caller that skips package jobs still cannot write nonsense": the guard
// makes an illegal edge match no row, but nothing in the WHERE clause would
// stop a typo in the target from being written. A row reading "Done" or
// "cancelled" is unreadable to every later query, so the target is checked
// against the six values analysis_jobs may hold before the UPDATE is built.
func TestTransitionJob_RefusesAStatusTheSchemaNeverDefined(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	for _, to := range []string{"", "Done", "RUNNING", "cancelled", "finished", "running "} {
		moved, err := s.TransitionJob(ctx, job.ID, []string{"queued", "running"}, to, "")
		if err == nil {
			t.Errorf("TransitionJob(queued→%q) = %v, nil; want a refusal for a status the schema does not define", to, moved)
		}
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Status != "queued" {
		t.Errorf("status = %q after refused targets, want %q: no UPDATE should have been sent", after.Status, "queued")
	}

	// The vocabulary itself is open: these are the six values the state machine
	// in package jobs names, and the store only refuses what is outside it.
	for _, to := range []string{"queued", "running", "done", "failed", "canceled", "interrupted"} {
		each, err := s.InsertJob(ctx, "c", "kind-"+to, "v1")
		if err != nil {
			t.Fatalf("InsertJob(%q) error: %v", to, err)
		}
		if _, err := s.TransitionJob(ctx, each.ID, []string{"queued"}, to, ""); err != nil {
			t.Errorf("TransitionJob(queued→%q) error: %v — %q is a status the schema defines", to, err, to)
		}
	}
}

// TestTransitionJob_KeepsTheFailureMessageAndClearsItOnAnEmptyOne records the
// decision Finish depends on: an empty message means "no error", stored as
// NULL, so a run that finished cleanly does not carry a previous attempt's
// failure around forever.
func TestTransitionJob_KeepsTheFailureMessageAndClearsItOnAnEmptyOne(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	failed, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, failed.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, failed.ID, []string{"running"}, "failed", "boom"); err != nil {
		t.Fatalf("TransitionJob(running→failed) error: %v", err)
	}
	stored, err := s.JobByID(ctx, failed.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if stored.Error != "boom" {
		t.Errorf("error = %q, want %q", stored.Error, "boom")
	}

	cleared, err := s.InsertJob(ctx, "c", "other-kind", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, cleared.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, cleared.ID, []string{"running"}, "done", ""); err != nil {
		t.Fatalf("TransitionJob(running→done) error: %v", err)
	}
	var column sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT error FROM analysis_jobs WHERE id = ?`, cleared.ID).Scan(&column); err != nil {
		t.Fatalf("read the error column: %v", err)
	}
	if column.Valid {
		t.Errorf("error column = %q, want NULL when the job closed without a message", column.String)
	}
}

func TestSetJobProgress_RecordsQueuedAndRunningJobs(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	// A queued job may report progress: the window between the insert and the
	// claim is real, and a block can commit inside it.
	if err := s.SetJobProgress(ctx, job.ID, 0, 12); err != nil {
		t.Fatalf("SetJobProgress() on a queued job error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if err := s.SetJobProgress(ctx, job.ID, 5, 12); err != nil {
		t.Fatalf("SetJobProgress() error: %v", err)
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Done != 5 || after.Total != 12 {
		t.Errorf("done/total = %d/%d, want 5/12", after.Done, after.Total)
	}
}

// TestSetJobProgress_RefusesAFinishedJob records the decision: a heartbeat that
// arrives after the job closed is an error, not a silent no-op. A silent no-op
// would let a worker believe its progress landed while the row says the run
// never got anywhere.
func TestSetJobProgress_RefusesAFinishedJob(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	if err := s.SetJobProgress(ctx, job.ID, 5, 12); err != nil {
		t.Fatalf("SetJobProgress() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{"running"}, "done", ""); err != nil {
		t.Fatalf("TransitionJob(running→done) error: %v", err)
	}

	err = s.SetJobProgress(ctx, job.ID, 12, 12)
	if err == nil {
		t.Fatal("SetJobProgress() on a done job = nil, want an error")
	}
	if !strings.Contains(err.Error(), "done") {
		t.Errorf("error %q does not name the status that refused the update", err)
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Done != 5 {
		t.Errorf("done = %d after a refused progress update, want 5 (the last value that landed)", after.Done)
	}
}

func TestSetJobProgress_RefusesDoneBeyondTotal(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	for _, tc := range []struct {
		name        string
		done, total int
	}{
		{"done beyond total", 5, 4},
		{"negative done", -1, 4},
		{"negative total", 1, -1},
	} {
		if err := s.SetJobProgress(ctx, job.ID, tc.done, tc.total); err == nil {
			t.Errorf("SetJobProgress(%s) = nil, want an error", tc.name)
		}
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Done != 0 || after.Total != 0 {
		t.Errorf("done/total = %d/%d after refused updates, want 0/0", after.Done, after.Total)
	}
}

func TestInterruptRunning_OnlyMovesRunningJobs(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	running, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, running.ID, []string{"queued"}, "running", ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	// The process that owned it is gone, and that is what its heartbeat says:
	// ten minutes without a write. Status alone no longer means "dead" — see
	// TestInterruptRunning_OnlyInterruptsAHeartbeatThatHasGoneStale.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE analysis_jobs SET updated_at = datetime('now', '-600 seconds') WHERE id = ?`, running.ID); err != nil {
		t.Fatalf("age the running job's heartbeat: %v", err)
	}
	queued, err := s.InsertJob(ctx, "c", "other-kind", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	canceled, err := s.InsertJob(ctx, "c", "third-kind", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, canceled.ID, []string{"queued"}, "canceled", ""); err != nil {
		t.Fatalf("TransitionJob(queued→canceled) error: %v", err)
	}

	interrupted, err := s.InterruptRunning(ctx, "c", "sentiment", 90*time.Second)
	if err != nil {
		t.Fatalf("InterruptRunning() error: %v", err)
	}
	if interrupted != 1 {
		t.Errorf("InterruptRunning() = %d, want 1 (only the running job)", interrupted)
	}
	for id, want := range map[int64]string{
		running.ID:  "interrupted",
		queued.ID:   "queued",
		canceled.ID: "canceled",
	} {
		job, err := s.JobByID(ctx, id)
		if err != nil {
			t.Fatalf("JobByID(%d) error: %v", id, err)
		}
		if job.Status != want {
			t.Errorf("job %d status = %q, want %q", id, job.Status, want)
		}
	}

	// Nothing is running any more, so the second call changes nothing and says so.
	again, err := s.InterruptRunning(ctx, "c", "sentiment", 90*time.Second)
	if err != nil {
		t.Fatalf("second InterruptRunning() error: %v", err)
	}
	if again != 0 {
		t.Errorf("second InterruptRunning() = %d, want 0", again)
	}
}

// TestInterruptRunning_OnlyInterruptsAHeartbeatThatHasGoneStale is the guard
// against a second CLI stealing the single-flight slot from a run that is still
// scoring. Two processes on one conversation each write part of the blocks, and
// word_freq_v2 accumulates (cnt = cnt + excluded.cnt), so the overlapping part
// is counted twice — a silent doubling of the numbers the report shows.
//
// uq_jobs_active makes two running rows in one (talker, kind) impossible, so
// the index is dropped here: this test is about the WHERE clause, and it takes
// both shapes in front of the same UPDATE to see that the aged row alone moves.
func TestInterruptRunning_OnlyInterruptsAHeartbeatThatHasGoneStale(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	if _, err := s.db.ExecContext(ctx, `DROP INDEX uq_jobs_active`); err != nil {
		t.Fatalf("drop uq_jobs_active: %v", err)
	}
	insertRunning := func(age string) int64 {
		t.Helper()
		result, err := s.db.ExecContext(ctx, `
INSERT INTO analysis_jobs (talker, kind, model_version, status, total, done, error, created_at, updated_at)
VALUES ('c', 'sentiment', 'v1', 'running', 0, 0, NULL, datetime('now', ?), datetime('now', ?))
`, age, age)
		if err != nil {
			t.Fatalf("insert a running job aged %s: %v", age, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read the id of the inserted job: %v", err)
		}
		return id
	}
	dead := insertRunning("-600 seconds")
	alive := insertRunning("-1 seconds")

	interrupted, err := s.InterruptRunning(ctx, "c", "sentiment", 90*time.Second)
	if err != nil {
		t.Fatalf("InterruptRunning() error: %v", err)
	}
	if interrupted != 1 {
		t.Errorf("InterruptRunning() = %d, want 1: a row untouched for ten minutes is dead, one touched a second ago is a live run", interrupted)
	}
	for _, tc := range []struct {
		id   int64
		want string
	}{{dead, "interrupted"}, {alive, "running"}} {
		job, err := s.JobByID(ctx, tc.id)
		if err != nil {
			t.Fatalf("JobByID(%d) error: %v", tc.id, err)
		}
		if job.Status != tc.want {
			t.Errorf("job %d status = %q, want %q", tc.id, job.Status, tc.want)
		}
	}

	// A zero or negative window would mean "every running row is stale", which is
	// the unconditional interruption this argument replaced. It is refused rather
	// than silently degenerating into it.
	if _, err := s.InterruptRunning(ctx, "c", "sentiment", 0); err == nil {
		t.Error("InterruptRunning() with a zero window = nil error, want a refusal")
	}
	if _, err := s.InterruptRunning(ctx, "c", "sentiment", -time.Second); err == nil {
		t.Error("InterruptRunning() with a negative window = nil error, want a refusal")
	}
	stillRunning, err := s.JobByID(ctx, alive)
	if err != nil {
		t.Fatalf("JobByID(%d) error: %v", alive, err)
	}
	if stillRunning.Status != "running" {
		t.Errorf("status = %q after the refused calls, want %q", stillRunning.Status, "running")
	}
}

func TestJobByID_ReportsAMissingJob(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)

	if _, err := s.JobByID(ctx, 4242); err == nil {
		t.Fatal("JobByID() on an unknown id = nil error, want one")
	} else if !strings.Contains(err.Error(), "4242") {
		t.Errorf("error %q does not name the id that was not found", err)
	}
}
