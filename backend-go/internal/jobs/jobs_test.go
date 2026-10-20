package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"lover-instance/backend-go/internal/store"
)

// openJobsStore opens a store on path and migrates it. Closing is registered on
// cleanup, so a test that closes early (to simulate a killed process) does not
// have to unregister anything: sql.DB.Close is idempotent.
func openJobsStore(t *testing.T, path string) *store.Store {
	t.Helper()
	dsn, err := store.DSN(path)
	if err != nil {
		t.Fatalf("store.DSN() error: %v", err)
	}
	s, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return s
}

// TestStart_ConcurrentStartsYieldOneJob is the single-flight contract. Two runs
// starting at the same instant must both be told the same job: the loser of the
// insert race adopts the winner's row instead of running a second inference
// pass over the same conversation. Asserting "both calls succeeded with the
// same id" is stronger than asserting one row, because an implementation that
// returned an error to the loser also leaves exactly one row.
func TestStart_ConcurrentStartsYieldOneJob(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	type result struct {
		job     Job
		created bool
		err     error
	}
	results := make([]result, 2)
	ready := make(chan struct{})
	var started sync.WaitGroup
	started.Add(len(results))
	for i := range results {
		go func(i int) {
			defer started.Done()
			<-ready
			job, created, err := Start(ctx, s, "c", "sentiment", "lexicon-hownet-v1")
			results[i] = result{job: job, created: created, err: err}
		}(i)
	}
	close(ready)
	started.Wait()

	created := 0
	for i, got := range results {
		if got.err != nil {
			t.Fatalf("Start() #%d error: %v", i, got.err)
		}
		if got.job.ID == 0 {
			t.Fatalf("Start() #%d returned job id 0", i)
		}
		if got.created {
			created++
		}
	}
	if results[0].job.ID != results[1].job.ID {
		t.Errorf("Start() ids = %d and %d, want one id: the second call must adopt the existing job",
			results[0].job.ID, results[1].job.ID)
	}
	if created != 1 {
		t.Errorf("created flags = %v/%v, want exactly one true", results[0].created, results[1].created)
	}

	var rows int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM analysis_jobs`).Scan(&rows); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if rows != 1 {
		t.Errorf("analysis_jobs holds %d rows, want 1", rows)
	}
}

// TestStart_ReportsAFreshJobAsRunning pins what Start means: it is called by the
// process that is about to do the work, so the row it hands back has been
// claimed. Leaving it queued would make the queued→running edge unreachable and
// would leave a killed run looking queued rather than interrupted.
func TestStart_ReportsAFreshJobAsRunning(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	job, created, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if !created {
		t.Fatal("Start() created = false, want true for the first start")
	}
	if job.Status != StatusRunning {
		t.Errorf("status = %q, want %q", job.Status, StatusRunning)
	}
	if job.ModelVersion != "v1" {
		t.Errorf("model_version = %q, want v1", job.ModelVersion)
	}
}

// TestRecover_FreesTheSlotAKilledRunLeftBehind is the restart path: the process
// is killed mid-run, its job is still 'running', and the next process has to
// turn that into 'interrupted' before it can start anything. The interrupted
// row is outside uq_jobs_active, which is exactly why the new start gets a new
// id rather than adopting a job nobody is working on.
func TestRecover_FreesTheSlotAKilledRunLeftBehind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")

	s := openJobsStore(t, path)
	killed, created, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if !created {
		t.Fatal("Start() created = false for the first start")
	}
	// The process dies here: no Finish, no Cancel, just the handle going away.
	if err := s.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	s = openJobsStore(t, path)
	interrupted, err := Recover(ctx, s, "c", "sentiment")
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if interrupted != 1 {
		t.Errorf("Recover() = %d, want 1: the killed run was still marked running", interrupted)
	}
	after, err := s.JobByID(ctx, killed.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Status != StatusInterrupted {
		t.Errorf("status = %q, want %q", after.Status, StatusInterrupted)
	}

	fresh, created, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() after recovery error: %v", err)
	}
	if !created {
		t.Error("Start() created = false, want a new job: an interrupted row is outside uq_jobs_active")
	}
	if fresh.ID == killed.ID {
		t.Errorf("Start() adopted the interrupted job %d, want a new id", fresh.ID)
	}

	// The slot is single, so the interrupted job can only be resumed once the
	// new run releases it. Resuming it while the fresh run holds the slot is
	// refused by the index rather than silently producing two running rows.
	if err := Resume(ctx, s, killed.ID); err == nil {
		t.Error("Resume() while another job holds the slot = nil error, want a refusal")
	}
	if err := Cancel(ctx, s, fresh.ID); err != nil {
		t.Fatalf("Cancel(fresh) error: %v", err)
	}
	// Now nothing is running — the fresh job is canceled and the old one is
	// interrupted — so a second recovery has nothing to mark.
	if count, err := Recover(ctx, s, "c", "sentiment"); err != nil || count != 0 {
		t.Errorf("second Recover() = %d, %v; want 0 and no error", count, err)
	}
	if err := Resume(ctx, s, killed.ID); err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	resumed, err := s.JobByID(ctx, killed.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if resumed.Status != StatusRunning {
		t.Errorf("status = %q after Resume, want %q", resumed.Status, StatusRunning)
	}
}

// TestIllegalTransitionsAreRefused covers every move the state machine does not
// allow, and requires the message to name both states: "illegal transition" on
// its own sends the reader back to the source to find out what happened.
func TestIllegalTransitionsAreRefused(t *testing.T) {
	ctx := context.Background()

	type step func(context.Context, *store.Store, int64) error
	cases := []struct {
		name    string
		from    string
		to      string
		reach   step // puts the job in the "from" state
		attempt step // the move that must be refused
	}{
		{
			name:  "done to running",
			from:  StatusDone,
			to:    StatusRunning,
			reach: func(ctx context.Context, s *store.Store, id int64) error { return Finish(ctx, s, id, StatusDone, "") },
			attempt: func(ctx context.Context, s *store.Store, id int64) error {
				return Resume(ctx, s, id)
			},
		},
		{
			name:  "canceled to done",
			from:  StatusCanceled,
			to:    StatusDone,
			reach: Cancel,
			attempt: func(ctx context.Context, s *store.Store, id int64) error {
				return Finish(ctx, s, id, StatusDone, "")
			},
		},
		{
			name: "failed to running",
			from: StatusFailed,
			to:   StatusRunning,
			reach: func(ctx context.Context, s *store.Store, id int64) error {
				return Finish(ctx, s, id, StatusFailed, "boom")
			},
			attempt: func(ctx context.Context, s *store.Store, id int64) error {
				return Resume(ctx, s, id)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))
			job, _, err := Start(ctx, s, "c", "sentiment", "v1")
			if err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			if err := tc.reach(ctx, s, job.ID); err != nil {
				t.Fatalf("reaching %q error: %v", tc.from, err)
			}
			err = tc.attempt(ctx, s, job.ID)
			if err == nil {
				t.Fatalf("%s = nil error, want a refusal", tc.name)
			}
			for _, want := range []string{tc.from, tc.to} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			after, err := s.JobByID(ctx, job.ID)
			if err != nil {
				t.Fatalf("JobByID() error: %v", err)
			}
			if after.Status != tc.from {
				t.Errorf("status = %q after a refused move, want %q", after.Status, tc.from)
			}
		})
	}
}

// TestFinish_RecordsTheFailureAndClearsItOnSuccess pins both halves of the error
// column: a failed run leaves its message behind for the next process to read,
// and a run that closes cleanly does not inherit one.
func TestFinish_RecordsTheFailureAndClearsItOnSuccess(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	failing, _, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := Finish(ctx, s, failing.ID, StatusFailed, "boom"); err != nil {
		t.Fatalf("Finish(failed) error: %v", err)
	}
	stored, err := s.JobByID(ctx, failing.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if stored.Status != StatusFailed || stored.Error != "boom" {
		t.Errorf("status/error = %q/%q, want %q/%q", stored.Status, stored.Error, StatusFailed, "boom")
	}

	ok, _, err := Start(ctx, s, "c", "other-kind", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := Finish(ctx, s, ok.ID, StatusDone, ""); err != nil {
		t.Fatalf("Finish(done) error: %v", err)
	}
	closed, err := s.JobByID(ctx, ok.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if closed.Status != StatusDone || closed.Error != "" {
		t.Errorf("status/error = %q/%q, want %q and an empty error", closed.Status, closed.Error, StatusDone)
	}

	// Finish only closes jobs: it cannot be used to fabricate a running one.
	if err := Finish(ctx, s, ok.ID, StatusRunning, ""); err == nil {
		t.Error("Finish(running) = nil error, want a refusal: running is not a final status")
	}
}

// TestHeartbeat_RecordsProgressAndRefusesNonsense keeps done and total honest.
// A heartbeat saying more messages were scored than exist would make the
// progress a reader sees exceed the work the conversation holds.
func TestHeartbeat_RecordsProgressAndRefusesNonsense(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	job, _, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := Heartbeat(ctx, s, job.ID, 2, 4); err != nil {
		t.Fatalf("Heartbeat(2, 4) error: %v", err)
	}
	after, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if after.Done != 2 || after.Total != 4 {
		t.Errorf("done/total = %d/%d, want 2/4", after.Done, after.Total)
	}

	if err := Heartbeat(ctx, s, job.ID, 5, 4); err == nil {
		t.Error("Heartbeat(done beyond total) = nil, want an error")
	}
	unchanged, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if unchanged.Done != 2 {
		t.Errorf("done = %d after a refused heartbeat, want 2", unchanged.Done)
	}

	// A heartbeat that arrives after the job closed is refused, so a worker
	// cannot rewrite the counters of a finished run.
	if err := Finish(ctx, s, job.ID, StatusDone, ""); err != nil {
		t.Fatalf("Finish(done) error: %v", err)
	}
	if err := Heartbeat(ctx, s, job.ID, 4, 4); err == nil {
		t.Error("Heartbeat() on a done job = nil, want an error")
	}
}

// TestCancel_MovesAnActiveJobAndRefusesAFinishedOne covers the cancel edge on
// both sides: an active job becomes canceled, and a job that already finished
// cannot be relabelled as canceled afterwards.
func TestCancel_MovesAnActiveJobAndRefusesAFinishedOne(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	active, _, err := Start(ctx, s, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if err := Cancel(ctx, s, active.ID); err != nil {
		t.Fatalf("Cancel() error: %v", err)
	}
	canceled, err := s.JobByID(ctx, active.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if canceled.Status != StatusCanceled {
		t.Errorf("status = %q, want %q", canceled.Status, StatusCanceled)
	}

	if err := Finish(ctx, s, active.ID, StatusDone, ""); err == nil {
		t.Error("Finish(done) on a canceled job = nil, want a refusal")
	}
	if err := Cancel(ctx, s, active.ID); err == nil {
		t.Error("Cancel() on a canceled job = nil, want a refusal")
	}
}

// TestFinish_ClosesAJobThatNeverStarted is the queued→final edge: a run that
// fails between being inserted and being claimed owns a job with no work behind
// it, and leaving it queued would block every later start on that conversation.
func TestFinish_ClosesAJobThatNeverStarted(t *testing.T) {
	ctx := context.Background()
	s := openJobsStore(t, filepath.Join(t.TempDir(), "data.db"))

	// Insert without claiming, which is the state Start leaves behind if the
	// claim fails after the row exists.
	job, err := s.InsertJob(ctx, "c", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if err := Finish(ctx, s, job.ID, StatusFailed, fmt.Sprintf("batch size %d is outside 1..%d", 0, 256)); err != nil {
		t.Fatalf("Finish(failed) on a queued job error: %v", err)
	}
	closed, err := s.JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobByID() error: %v", err)
	}
	if closed.Status != StatusFailed {
		t.Errorf("status = %q, want %q", closed.Status, StatusFailed)
	}
}
