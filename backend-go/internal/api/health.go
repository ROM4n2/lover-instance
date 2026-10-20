package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/store"
)

const (
	// healthTimeout bounds every probe the health endpoint makes. The store
	// holds a single connection, so a health check that waited behind a long
	// transaction would report the dashboard as down for as long as the run
	// lasts — the badge exists to tell "not running" apart from "busy".
	//
	// Five seconds, not two: measured on a real scoring run, the nlp-service
	// answers /health in about 2.9s while it is chewing through a batch (the
	// request queues behind the work), so a 2s probe reported "the sentiment
	// service is not running" exactly while it was the thing doing the work.
	// The badge lied at the one moment a reader was watching it.
	healthTimeout = 5 * time.Second

	// statusDegraded is returned when some part of the system cannot do its
	// job. It is not an error status: the badge in the corner has to be able
	// to say "the sentiment service is not running", which it cannot do if the
	// request fails instead of answering.
	statusOK       = "ok"
	statusDegraded = "degraded"

	// nlpUnreachable is what the nlp block reports when the service did not
	// answer at all, as opposed to answering that it is still loading.
	nlpUnreachable = "unreachable"

	// dbOK and dbError are the two values the db field may hold.
	dbOK    = "ok"
	dbError = "error"
)

// healthResponse is what GET /api/health returns.
//
// It is a flat description of three independent parts of the system rather than
// one verdict: a dashboard can then say which part is missing instead of only
// that something is.
type healthResponse struct {
	Status  string     `json:"status"`
	DB      string     `json:"db"`
	NLP     nlpHealth  `json:"nlp"`
	Job     *jobHealth `json:"job"`
	Version string     `json:"version"`
}

// nlpHealth mirrors the service's own /health answer plus whether it answered.
//
// available_versions is always a list, never null: the client is what offers
// the menu of models to score with, and an empty menu has to render as an empty
// menu rather than as a broken field.
type nlpHealth struct {
	Status            string   `json:"status"`
	ModelVersion      string   `json:"model_version"`
	AvailableVersions []string `json:"available_versions"`
	Reachable         bool     `json:"reachable"`
}

// jobHealth is the progress of the newest active run, as the dashboard shows it.
//
// The fields are the store's own names for the same values (Job.ID, Status,
// Done, Total), lowercased for JSON. Nothing is renamed and nothing is derived:
// done and total are the absolute message counts the job row holds, not a
// percentage. A percentage goes backwards when messages are imported, and a
// reader watching a bar that moves backwards stops believing the bar.
//
// store.Job is not reused directly because it carries Talker and, later,
// ModelVersion — a struct with a talker field one `c.JSON` away from the
// response is precisely the shape this package must not have.
type jobHealth struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Done   int    `json:"done"`
	Total  int    `json:"total"`
}

// healthHandler answers GET /api/health with 200 in every case, including a
// dead database and a missing NLP service.
//
// The alternative — failing the request — would let one missing component take
// the whole page down, and the page is what tells the user which component is
// missing. So the status code is the transport's answer ("this endpoint is
// working"); the body's status field is the system's.
func healthHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), healthTimeout)
		defer cancel()

		response := healthResponse{
			Status:  statusOK,
			DB:      dbOK,
			Version: opts.Version,
		}

		// A ping rather than a query: the question is whether the file is
		// still there and still writable, and a schema read would answer a
		// different one.
		if err := opts.Store.DB().PingContext(ctx); err != nil {
			response.DB = dbError
			response.Status = statusDegraded
		}

		response.NLP = probeNLP(ctx, opts.NLPURL)
		// Ready is the only status that can score. "loading" is a service that
		// is up and cannot yet do the job, which is degraded by the badge's
		// definition.
		if !response.NLP.Reachable || response.NLP.Status != "ready" {
			response.Status = statusDegraded
		}

		job, err := activeJob(ctx, opts.Store)
		if err != nil {
			// The read failing is not reported as a job that does not exist:
			// the badge degrades so nobody reads the null as "no run is
			// happening". The error text stays out of the response.
			response.Status = statusDegraded
			job = nil
		}
		response.Job = job

		c.JSON(http.StatusOK, response)
	}
}

// probeNLP asks the service how it is doing and describes it either way.
//
// A service that is not running is the expected state on a machine where the
// dashboard was opened before the Python side, so it is described, not raised:
// the caller is told it is unreachable and gets an empty menu.
func probeNLP(ctx context.Context, url string) nlpHealth {
	unreachable := nlpHealth{
		Status:            nlpUnreachable,
		AvailableVersions: []string{},
		Reachable:         false,
	}
	client, err := nlp.NewClient(url)
	if err != nil {
		// An unusable URL is indistinguishable from a service that is not
		// there, from the dashboard's point of view.
		return unreachable
	}
	health, err := client.Health(ctx)
	if err != nil {
		return unreachable
	}
	return nlpHealth{
		Status:            health.Status,
		ModelVersion:      health.ModelVersion,
		AvailableVersions: health.AvailableVersions,
		Reachable:         true,
	}
}

// activeJob returns the newest queued or running job in this database, or nil
// when no run is active.
//
// The query lives here rather than in package jobs because jobs is organised
// around one (talker, kind) slot — Store.ActiveJob answers "is *this*
// conversation being scored" — while the dashboard asks a wider question: is
// anything running at all, so the badge and the progress bar can attach to it.
// Widening jobs.ActiveJob would change a public API that the single-flight
// contract depends on, so the read is done here instead.
//
// It is a read, one SELECT, no writes and no transactions. The store package's
// rule that SQL belongs to the store is bent for this one statement, and the
// honest note is that the right long-term home for it is a store method; the
// API package is where it must live for this task.
//
// The talker column is not selected: the caller serializes this row into an
// HTTP response, and what is not read cannot be leaked.
func activeJob(ctx context.Context, s *store.Store) (*jobHealth, error) {
	var job jobHealth
	err := s.DB().QueryRowContext(ctx, `
SELECT id, status, done, total
  FROM analysis_jobs
 WHERE status IN (?, ?)
 ORDER BY id DESC
 LIMIT 1
`, jobs.StatusQueued, jobs.StatusRunning).Scan(&job.ID, &job.Status, &job.Done, &job.Total)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}
