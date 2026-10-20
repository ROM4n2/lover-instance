package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/store"
)

const (
	// heartbeatInterval is how often a live run refreshes its job row. It is
	// deliberately independent of the blocks: one block is a single HTTP call
	// that may legitimately take the whole block timeout, so a run can go
	// minutes without committing anything, and jobs.Recover reads silence as a
	// dead process. Ten seconds against a ninety-second window is nine missed
	// beats of margin.
	heartbeatInterval = 10 * time.Second

	// finishTimeout bounds the write that closes a job. It runs on a context
	// that ignores the run's cancellation, so it is the only thing standing
	// between a wedged database and a worker that never lets go.
	finishTimeout = 5 * time.Second
)

// JobManager owns the workers this process is running.
//
// The database owns which job is active — uq_jobs_active settles that, and it
// survives a restart — but it cannot hold a context, so the two things only a
// live process knows live here: the cancel function of each run, and the
// goroutine that has to finish before the process may close the database.
//
// The map is not an authority on anything. A job row is. This map only answers
// "is there a worker in *this* process I can ask to stop", which is why a miss
// falls back to jobs.Cancel rather than reporting that the job is gone.
type JobManager struct {
	s      *store.Store
	nlpURL string

	baseCtx  context.Context
	stopBase context.CancelFunc

	// submitMu serialises the claim, not the work. Two POSTs arriving together
	// must get one 202 and one 409, and that answer has to be decided before
	// either response is written: the insert itself is already single-flight,
	// but the loser of that race needs the winner's id, which only a
	// serialised Start can hand back synchronously.
	submitMu sync.Mutex

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

// NewJobManager builds a manager for s, scoring through the service at nlpURL.
func NewJobManager(s *store.Store, nlpURL string) *JobManager {
	baseCtx, stopBase := context.WithCancel(context.Background())
	return &JobManager{
		s:        s,
		nlpURL:   nlpURL,
		baseCtx:  baseCtx,
		stopBase: stopBase,
		cancels:  make(map[int64]context.CancelFunc),
	}
}

// Shutdown stops every worker this process started and waits for them to
// release the database.
//
// Waiting is not politeness: the workers write to the one connection the server
// is about to close, and a worker still inside a transaction when Close happens
// is a crash at exit. A run that was stopped this way records itself canceled,
// which is accurate — the user did stop it. The row a *killed* process leaves
// behind stays 'running' and is what jobs.Recover cleans up on the next start.
func (m *JobManager) Shutdown() {
	m.stopBase()
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
}

// cancel stops the worker for id and reports whether this process had one.
//
// A false is not an error: the run may have been started by the CLI, or by a
// previous process that died. The caller falls back to writing the row, which
// is the answer that works whether or not anybody is watching it.
func (m *JobManager) cancel(id int64) bool {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// claim records the run in the database and returns the job this request owns.
//
// Recover comes first, as it does in cmd/analyze: a killed process cannot write
// its own obituary, so its job still holds the single-flight slot until the
// heartbeat has been silent for jobs.StaleAfter. Start then either creates the
// row or adopts a live one, and created reports which — that flag is what turns
// into 202 versus 409.
func (m *JobManager) claim(ctx context.Context, talker, version string) (jobs.Job, bool, error) {
	m.submitMu.Lock()
	defer m.submitMu.Unlock()

	if _, err := jobs.Recover(ctx, m.s, talker, jobKindSentiment); err != nil {
		return jobs.Job{}, false, err
	}
	return jobs.Start(ctx, m.s, talker, jobKindSentiment, version)
}

// launch runs the claimed job on its own goroutine and its own context.
//
// The cancel function is registered before launch returns, so a DELETE that
// arrives immediately after the 202 still finds the worker it has to stop.
func (m *JobManager) launch(job jobs.Job, talker, version string) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.mu.Lock()
	m.cancels[job.ID] = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.cancels, job.ID)
			m.mu.Unlock()
			cancel()
		}()
		m.execute(ctx, job, talker, version)
	}()
}

// execute is the body of the worker: the same order cmd/analyze runs after its
// claim, with one difference — the error is written to the job row and nowhere
// else, because there is no caller to return it to.
func (m *JobManager) execute(ctx context.Context, job jobs.Job, talker, version string) {
	client, err := nlp.NewClient(m.nlpURL)
	if err != nil {
		m.closeJob(ctx, job.ID, err)
		return
	}

	// The beat goroutine and the per-block callback write the same row from two
	// goroutines, so the first failure is kept behind a mutex: under -race a
	// plain field here is a data race, and the reported failure would be
	// whichever write the scheduler let land last.
	var heartbeatMu sync.Mutex
	var heartbeatErr error
	note := func(err error) {
		if err == nil {
			return
		}
		heartbeatMu.Lock()
		defer heartbeatMu.Unlock()
		if heartbeatErr == nil {
			heartbeatErr = err
		}
	}
	heartbeatFailure := func() error {
		heartbeatMu.Lock()
		defer heartbeatMu.Unlock()
		return heartbeatErr
	}

	beat := func(ctx context.Context) error {
		return recordJobProgress(ctx, m.s, job.ID, talker, version)
	}
	stopHeartbeat := keepFresh(ctx, heartbeatInterval, beat, note)
	defer stopHeartbeat()

	// The per-block beats are kept even though they only run after a commit:
	// they are what makes the bar move during a fast pass, and the coverage
	// figure they write is right for a resumed run too.
	_, runErr := analyze.RunSentimentProgress(ctx, m.s, client, talker, version, nlp.MaxBatchSize,
		func(_, _ int) {
			note(recordJobProgress(ctx, m.s, job.ID, talker, version))
		})
	// Before anything that closes the run, and it waits: a beat landing after
	// the closing write is an error against a finished row.
	stopHeartbeat()

	if runErr == nil {
		// A run over an already-scored conversation sends no blocks, so without
		// a closing beat a finished job would be recorded as 0/0.
		runErr = heartbeatFailure()
	}
	if runErr == nil {
		runErr = m.s.RebuildSentimentMetrics(ctx, talker, version)
	}
	if runErr == nil {
		runErr = recordJobProgress(ctx, m.s, job.ID, talker, version)
	}
	m.closeJob(ctx, job.ID, runErr)
}

// closeJob writes the job's final state.
//
// status and the error message are derived from the same error the work
// produced, so the row cannot say "done" about a pass that failed. The error
// text never carries message content: the NLP client reports status codes, and
// the pipeline's own wrapping counts blocks.
//
// The write runs on a context that ignores the run's cancellation. That is the
// M2 lesson: database/sql checks the context before it will hand out the
// connection, so a cancelled context means the UPDATE is never sent and the row
// stays 'running' for as long as the slot matters. WithoutCancel keeps a
// Ctrl-C or a DELETE from producing the one outcome a canceled job must not
// produce, and the deadline keeps that from becoming a hang.
func (m *JobManager) closeJob(ctx context.Context, id int64, runErr error) {
	status := jobs.StatusDone
	message := ""
	if runErr != nil {
		status = jobs.StatusFailed
		message = runErr.Error()
		if ctx.Err() != nil {
			status = jobs.StatusCanceled
			message = ctx.Err().Error()
		}
	}

	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	// A failure here has nowhere to go and is not silenced for convenience: the
	// only realistic one is a row DELETE already closed, and the row that is
	// there is then the accurate record. Anything else leaves the run 'running'
	// and jobs.Recover picks it up on the next start.
	_ = jobs.Finish(finishCtx, m.s, id, status, message)
}

// recordJobProgress writes how much of the conversation is scored, not how much
// this run scored.
//
// A resumed run handles only the missing blocks, so "12 scored" on a
// conversation of 22,093 would show a finished conversation as barely started.
// Coverage answers "how far along is this?" for both cases, and it is the same
// figure the report's coverage section prints.
func recordJobProgress(ctx context.Context, s *store.Store, jobID int64, talker, modelVersion string) error {
	coverage, err := s.SentimentCoverage(ctx, talker, modelVersion)
	if err != nil {
		return err
	}
	return jobs.Heartbeat(ctx, s, jobID, coverage.Scored, coverage.Total)
}

// keepFresh runs beat on its own clock until the returned stop is called, and
// stop waits for the goroutine to exit.
//
// It is the same shape cmd/analyze uses, for the same reasons: the beat cannot
// be tied to committed blocks, a cancelled context must not leave stop waiting
// for a tick a minute away, and a beat that overlapped the closing write would
// be two writers on one row.
func keepFresh(ctx context.Context, every time.Duration, beat func(context.Context) error, onErr func(error)) (stop func()) {
	quit := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-ticker.C:
				if err := beat(ctx); err != nil {
					onErr(err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-exited
	}
}

// jobCreateRequest is the body of POST /api/jobs.
type jobCreateRequest struct {
	Talker       string `json:"talker"`
	ModelVersion string `json:"model_version"`
}

// jobResponse is what GET /api/jobs/:id returns.
//
// The fields are the store's own values lowercased. Talker and ModelVersion are
// absent for the reason jobHealth gives: a struct with a talker field one
// c.JSON away from the response is the shape this package must not have.
type jobResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Error  string `json:"error"`
}

// jobConflictResponse is the body of the two "you cannot start/cancel this"
// answers. It keeps the error envelope's three fields and adds the job id, so
// the dashboard can attach its progress bar to the run that blocked it instead
// of guessing which one is in the way.
type jobConflictResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	ID      int64  `json:"id"`
}

// jobsCreateHandler answers POST /api/jobs with 202 and the new job's id.
func jobsCreateHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request jobCreateRequest
		// An empty body means "the only conversation, the default model", which
		// is the same shorthand GET /api/overview allows. Anything else has to
		// parse: a body silently ignored would start a run nobody asked for.
		if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
			writeError(c, http.StatusBadRequest, "invalid_body",
				"the request body is not the JSON object this endpoint expects",
				`send {"talker":"<fingerprint or conversation key>","model_version":"<optional>"}`)
			return
		}
		ctx := c.Request.Context()

		talker, failure := resolveTalker(ctx, request.Talker, opts.Store)
		if failure != nil {
			failure.write(c)
			return
		}
		version, failure := sentimentVersion(ctx, opts, request.ModelVersion)
		if failure != nil {
			failure.write(c)
			return
		}
		if opts.manager == nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"this server has no analysis worker configured",
				"restart the server; if it keeps failing, check the server's log")
			return
		}

		job, created, err := opts.manager.claim(ctx, talker, version)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the analysis run could not be recorded",
				"retry the request; if it keeps failing, check /api/health and the server's log")
			return
		}
		if !created {
			// Not an error: the conversation already has a run, and the client
			// gets its id so it can watch that one instead of starting a second
			// pass over the same messages.
			c.JSON(http.StatusConflict, jobConflictResponse{
				Code:    "job_active",
				Message: "this conversation already has an analysis run in progress",
				Hint:    "watch it with GET /api/jobs/{id}, or cancel it before starting another",
				ID:      job.ID,
			})
			return
		}

		opts.manager.launch(job, talker, version)
		c.JSON(http.StatusAccepted, gin.H{"id": job.ID})
	}
}

// jobStatusHandler answers GET /api/jobs/:id.
func jobStatusHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		job, ok := jobFromPath(c, opts)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, jobResponse{
			ID:     job.ID,
			Status: job.Status,
			Done:   job.Done,
			Total:  job.Total,
			Error:  job.Error,
		})
	}
}

// jobCancelHandler answers DELETE /api/jobs/:id.
//
// A worker in this process is asked to stop, and it is the worker that writes
// the final state — so a cancelled run records why it stopped, and the row is
// closed by the same code that would have closed a finished one. When no worker
// is here to stop (the run was started by the CLI, or by a process that has
// since exited) the row is written directly, which is the only answer that
// frees the slot without a restart.
func jobCancelHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		job, ok := jobFromPath(c, opts)
		if !ok {
			return
		}
		ctx := c.Request.Context()

		if opts.manager != nil && opts.manager.cancel(job.ID) {
			c.JSON(http.StatusAccepted, gin.H{"id": job.ID})
			return
		}
		if err := jobs.Cancel(ctx, opts.Store, job.ID); err != nil {
			c.JSON(http.StatusConflict, jobConflictResponse{
				Code:    "job_not_cancelable",
				Message: "this analysis run is " + job.Status + " and cannot be canceled",
				Hint:    "only a queued or running run can be stopped; a finished one is a record",
				ID:      job.ID,
			})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"id": job.ID})
	}
}

// jobFromPath reads the job named in the URL, writing 400 or 404 itself.
//
// The row carries a talker and the response never does, so the caller is handed
// the row and must copy only the fields it named.
func jobFromPath(c *gin.Context, opts Options) (jobs.Job, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid_job_id",
			"the job id in this URL is not a positive number",
			"use the id returned by POST /api/jobs")
		return jobs.Job{}, false
	}
	job, err := opts.Store.JobByID(c.Request.Context(), id)
	if err != nil {
		// Missing and unreadable are answered the same way on purpose: telling
		// them apart would confirm which ids exist, and the difference is not
		// something the dashboard can act on.
		writeError(c, http.StatusNotFound, "job_not_found",
			"no analysis run has that id in this database",
			"list the runs from /api/health, or start one with POST /api/jobs")
		return jobs.Job{}, false
	}
	return job, true
}
