// Package jobs owns the lifecycle of a long analysis run: which run is allowed
// to exist for a conversation, how it reports progress, and how it ends.
//
// The state machine is the contract ADR-0002 fixed:
//
//	queued    → running                          Start claims the job it inserted
//	queued    → failed | canceled                Finish, for a run that died before its claim
//	running   → done | failed | canceled | interrupted
//	interrupted → running                        Resume, after Recover
//
// Everything else is refused. The edges are enforced twice on purpose: here
// (each transition names the statuses it accepts) and in the store (a guarded
// UPDATE whose RowsAffected says whether the row moved). A caller that skips
// this package still cannot write an illegal edge.
//
// Single flight is the database's job, not this package's. uq_jobs_active is a
// partial index on (talker, kind) over queued and running, so there is no
// process-local lock to forget to hold and no window between a check and an
// insert. Two runs that start at the same instant both try to insert; the index
// refuses the second, and the loser adopts the winner's job.
package jobs

import (
	"context"
	"fmt"
	"time"

	"lover-instance/backend-go/internal/store"
)

// The status values analysis_jobs.status may hold. They are exported so a
// caller reporting on a job does not spell them out from memory and drift.
const (
	// StatusQueued is a job that exists but has not been claimed. It occupies
	// the single-flight slot: a second start would be a second run.
	StatusQueued = "queued"
	// StatusRunning is a job its owner is working on right now.
	StatusRunning = "running"
	// StatusDone is a completed run: every message the job covered is scored.
	StatusDone = "done"
	// StatusFailed is a run that stopped because something went wrong. The
	// message in the error column says what, and it never carries message text.
	StatusFailed = "failed"
	// StatusCanceled is a run the user asked to stop. The work it committed
	// stands; the anti-join makes the remainder cheap to pick up later.
	StatusCanceled = "canceled"
	// StatusInterrupted is a run whose process disappeared. It is outside
	// uq_jobs_active, which is what frees the slot for the next run.
	StatusInterrupted = "interrupted"
)

// Job is the stored row. It is aliased rather than redefined so a caller of
// this package never has to import the store to name what it receives.
type Job = store.Job

// Start returns the job this run owns, creating it if the slot is free.
//
// created reports whether this call made the job. A false means another run
// already owns the (talker, kind) slot and the returned job is that one: the
// caller must not start a second pass over the conversation.
//
// The returned job is running in both cases. A job adopted at queued is claimed
// on the way out, because a row still reading queued while a worker scores that
// conversation is the state nobody is in.
//
// The insert-then-look order is the whole mechanism. Looking first and
// inserting afterwards has a window in which two callers both see an empty
// slot, and the cost of losing that race is two inference passes writing the
// same rows — the OOM the ADR opens with. Here the index settles it, and only
// the loser pays for the extra lookup.
func Start(ctx context.Context, s *store.Store, talker, kind, modelVersion string) (Job, bool, error) {
	if talker == "" || kind == "" {
		return Job{}, false, fmt.Errorf("jobs: start %q: talker and kind must both be named", kind)
	}
	job, err := s.InsertJob(ctx, talker, kind, modelVersion)
	if err != nil {
		// Only uq_jobs_active means "someone else is running". The insert fails
		// the same way for a full disk, a read-only file or a closed handle, and
		// adopting a job on those would report a run that does not exist while
		// the actual fault goes unreported.
		if !store.IsUniqueViolation(err) {
			return Job{}, false, err
		}
		existing, ok, lookupErr := s.ActiveJob(ctx, talker, kind)
		if lookupErr != nil {
			return Job{}, false, lookupErr
		}
		if !ok {
			// The constraint fired but nothing covers the slot any more: the
			// row it collided with is gone, and the insert error is the only
			// truthful thing left to report.
			return Job{}, false, err
		}
		if existing.Status == StatusRunning {
			// A live run owns the slot and the row already says so. Writing
			// again would be a second claim, not a second owner.
			return existing, false, nil
		}
		adopted, claimErr := claimQueued(ctx, s, existing.ID, kind)
		if claimErr != nil {
			return Job{}, false, claimErr
		}
		return adopted, false, nil
	}
	claimedJob, err := claimQueued(ctx, s, job.ID, kind)
	if err != nil {
		return Job{}, false, err
	}
	return claimedJob, true, nil
}

// claimQueued moves a queued job to running and returns it as stored.
//
// Both the run that inserted the row and a run adopting it end up here, so the
// guarded UPDATE decides which of them moves it: whoever wins, the other reads
// the row back, finds it running, and gets the same answer. That is why losing
// the claim is not an error — the question is whether the job is running, not
// who wrote the status. It also keeps the row honest for the whole pass: a job
// that stays 'queued' while a worker is scoring it documents a state nobody is
// in, and Recover cannot repair that afterwards, since Recover only moves
// running jobs.
func claimQueued(ctx context.Context, s *store.Store, id int64, kind string) (Job, error) {
	moved, err := s.TransitionJob(ctx, id, []string{StatusQueued}, StatusRunning, "")
	if err != nil {
		return Job{}, fmt.Errorf("jobs: start %q: %w", kind, err)
	}
	claimed, err := s.JobByID(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !moved && claimed.Status != StatusRunning {
		// Neither the claim nor the read finds a job this run may work on:
		// something closed or interrupted the row between the insert and here,
		// and carrying on would score a conversation nobody asked about.
		return Job{}, fmt.Errorf("jobs: start %q: job %d is %q when it should be queued or running", kind, id, claimed.Status)
	}
	return claimed, nil
}

// Heartbeat records a committed block's progress.
//
// It is called after the block's transaction commits, never before: a job row
// that advertises work the database does not hold is the half-finished state
// ADR-0002 exists to prevent.
//
// The store refuses done > total and refuses a job that has already finished,
// so a late heartbeat cannot rewrite a closed run's counters.
func Heartbeat(ctx context.Context, s *store.Store, id int64, done, total int) error {
	if err := s.SetJobProgress(ctx, id, done, total); err != nil {
		return fmt.Errorf("jobs: heartbeat: %w", err)
	}
	return nil
}

// Finish closes a job with one of the final statuses.
//
// The source states are not the same for every outcome:
//
//   - done only follows running. A job that was never claimed did no work, and
//     accepting queued→done would let a bug that never ran the pass — a batch
//     size refused at the first check, a claim that silently failed — report a
//     finished conversation.
//   - failed and canceled also follow queued, deliberately: a run can die
//     between being inserted and being claimed, and leaving that row open would
//     hold the single-flight slot for a conversation nobody is working on,
//     blocking every later start.
//
// A job that has already finished is refused, so a duplicate Finish cannot
// rewrite a recorded outcome — that is how "done" would quietly become
// "failed".
//
// errMsg becomes the job's error column; an empty message clears it, because a
// run that closed cleanly must not inherit a previous attempt's failure.
func Finish(ctx context.Context, s *store.Store, id int64, status, errMsg string) error {
	var from []string
	switch status {
	case StatusDone:
		from = []string{StatusRunning}
	case StatusFailed, StatusCanceled:
		from = []string{StatusQueued, StatusRunning}
	default:
		return fmt.Errorf("jobs: finish job %d: %q is not a final status", id, status)
	}
	if err := move(ctx, s, id, from, status, errMsg); err != nil {
		return fmt.Errorf("jobs: finish job %d: %w", id, err)
	}
	return nil
}

// Cancel asks a job to stop.
//
// It only writes the state: the worker notices between blocks, when it checks
// ctx.Err(), and the work already committed stays committed. A queued job is
// cancellable too — nothing has started, so there is nothing to tell.
func Cancel(ctx context.Context, s *store.Store, id int64) error {
	if err := move(ctx, s, id, []string{StatusQueued, StatusRunning}, StatusCanceled, ""); err != nil {
		return fmt.Errorf("jobs: cancel job %d: %w", id, err)
	}
	return nil
}

// Resume puts an interrupted job back to running.
//
// It is the explicit way to continue a run that a dead process left behind, and
// it is separate from Start because the choice is the user's: adopting an old
// job keeps its id and history, while starting a new one leaves the
// interrupted row as a record. Resuming while another job holds the slot is
// refused by uq_jobs_active, which is the correct answer — two runs over one
// conversation is what the index exists to prevent.
func Resume(ctx context.Context, s *store.Store, id int64) error {
	if err := move(ctx, s, id, []string{StatusInterrupted}, StatusRunning, ""); err != nil {
		return fmt.Errorf("jobs: resume job %d: %w", id, err)
	}
	return nil
}

// StaleAfter is how long a running job may go without a heartbeat before it is
// treated as dead. It must exceed the heartbeat cadence by a wide margin: a live
// run beats every 10s, so 90s means nine missed beats — long enough that a slow
// block, a stalled write or a busy machine cannot produce a false death, short
// enough that a killed run stops blocking the next start within seconds.
const StaleAfter = 90 * time.Second

// Recover marks the runs a previous process left behind and reports how many it
// changed.
//
// It runs before Start, not after: a killed process cannot write its own
// obituary, so its job is still running and still occupies the single-flight
// slot. Marking it interrupted is what lets the slot go — uq_jobs_active covers
// only queued and running, so an interrupted row is outside the index by design
// and a fresh start can take the slot while the old job stays available to
// Resume. Returning the count lets the CLI say that it happened instead of
// silently changing state behind the user's back.
//
// "Left behind" means the heartbeat stopped, not merely that the row says
// running: a second CLI started while the first is still scoring must leave it
// alone. Interrupting a live run frees its slot for the newcomer, and the two
// passes then score overlapping blocks of one conversation — which word_freq_v2
// accumulates into double counts. StaleAfter is the window that separates the
// two cases.
func Recover(ctx context.Context, s *store.Store, talker, kind string) (int64, error) {
	interrupted, err := s.InterruptRunning(ctx, talker, kind, StaleAfter)
	if err != nil {
		return 0, fmt.Errorf("jobs: recover %q: %w", kind, err)
	}
	return interrupted, nil
}

// move performs a guarded transition and turns "the guard matched nothing" into
// an error that names both states.
func move(ctx context.Context, s *store.Store, id int64, from []string, to, errMsg string) error {
	moved, err := s.TransitionJob(ctx, id, from, to, errMsg)
	if err != nil {
		return err
	}
	if moved {
		return nil
	}
	current, err := s.JobByID(ctx, id)
	if err != nil {
		return fmt.Errorf("the current status could not be read: %w", err)
	}
	return fmt.Errorf("status %q has no edge to %q", current.Status, to)
}
