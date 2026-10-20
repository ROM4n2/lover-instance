package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/report"
	"lover-instance/backend-go/internal/store"
)

const (
	// overviewTimeout bounds one aggregate read. It is generous because the
	// store holds a single connection: while a scoring run commits a block
	// this request waits behind it, and the failure worth reporting is a
	// database that stopped answering, not a run that is busy.
	overviewTimeout = 60 * time.Second

	// confidenceTimeout bounds the probe that asks the NLP service which model
	// version to report. Health is one small GET; a service that cannot answer
	// that in two seconds cannot be named as the source of the numbers either.
	modelProbeTimeout = 5 * time.Second
)

// overviewResponse is what GET /api/overview returns.
//
// View is report.View untouched — the same struct the HTML template renders —
// so the dashboard and the shareable file cannot disagree about a figure. No
// field is renamed or recomputed here; the one thing added is the job progress,
// which the report file has no room for and which the dashboard needs to gate a
// half-finished curve.
//
// There is no talker field. A conversation is named by its fingerprint, which
// View.Meta.TalkerHash already carries; the top-level copy exists so a client
// does not have to know where inside the view to look for it.
type overviewResponse struct {
	TalkerHash string       `json:"talker_hash"`
	View       report.View  `json:"view"`
	Analysis   analysisView `json:"analysis"`
}

// analysisView says whether a scoring run is in progress for this conversation,
// and how far the run has got in absolute counts.
//
// A dashboard that drew a sentiment curve while this was running would draw the
// left half of the conversation as though it were the whole of it. The gate is
// the point of the field: done and total are what the client needs to decide
// whether to render a full curve or a "still analyzing" placeholder.
type analysisView struct {
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`
}

// overviewHandler answers GET /api/overview.
func overviewHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), overviewTimeout)
		defer cancel()

		talker, failure := resolveTalker(ctx, c.Query("talker"), opts.Store)
		if failure != nil {
			failure.write(c)
			return
		}
		version, failure := sentimentVersion(ctx, opts, c.Query("model_version"))
		if failure != nil {
			failure.write(c)
			return
		}

		// One derivation, the report's own. Anything computed a second time here
		// would be a second answer to the same question, and the two would drift
		// the first time either changed.
		view, err := report.Collect(ctx, opts.Store, talker, store.BehaviorModelVersion, version, nil)
		if err != nil {
			// The error is not echoed: it wraps a SQL error, and an error string
			// is the easiest place for a database path to leave the process.
			writeError(c, http.StatusInternalServerError, "internal",
				"the aggregate view could not be assembled from this database",
				"retry the request; if it keeps failing, check /api/health and the server's log")
			return
		}

		analysis, failure := analysisOf(ctx, opts.Store, talker)
		if failure != nil {
			failure.write(c)
			return
		}

		c.JSON(http.StatusOK, overviewResponse{
			// Read from the view rather than hashed again: one derivation of the
			// fingerprint, and it is the one the report file will print.
			TalkerHash: view.Meta.TalkerHash,
			View:       view,
			Analysis:   analysis,
		})
	}
}

// resolveTalker picks the conversation an endpoint speaks about.
//
// A named talker is used as given. Without one, a database holding exactly one
// conversation answers itself — that is the common case and asking the user to
// name the only option is noise. More than one is a decision only the user can
// make, so it is refused with the candidates named by fingerprint: the response
// must not turn a wrong guess into a report about the wrong person, and it must
// not put a wxid in a body either.
func resolveTalker(ctx context.Context, requested string, s *store.Store) (string, *apiFailure) {
	if requested != "" {
		return requested, nil
	}
	talkers, err := s.ListTalkers(ctx)
	if err != nil {
		return "", fail(http.StatusInternalServerError, "internal",
			"the stored conversations could not be listed",
			"retry the request; if it keeps failing, check /api/health")
	}
	switch len(talkers) {
	case 0:
		return "", fail(http.StatusBadRequest, "talker_required",
			"no conversation has been imported into this database yet",
			"import an export with the analyze command first, then reopen the dashboard")
	case 1:
		return talkers[0], nil
	default:
		hashes := make([]string, 0, len(talkers))
		for _, talker := range talkers {
			hashes = append(hashes, report.TalkerHash(talker))
		}
		return "", fail(http.StatusBadRequest, "talker_required",
			"this database holds more than one conversation, so a talker has to be named",
			"use one of these fingerprints as ?talker=: "+strings.Join(hashes, ", "))
	}
}

// sentimentVersion decides which model version the numbers are attributed to.
//
// The requested version wins; otherwise the service's own default, which is
// what every run used before the choice existed. Either way an empty answer is
// refused rather than guessed at: the report prints the model that produced it,
// and a report that cannot name its model is not a report.
//
// A version the service does not advertise is refused before any work starts.
// Letting it through would surface as a 400 from the scoring service in the
// middle of a long pass, after a job row had already been written.
func sentimentVersion(ctx context.Context, opts Options, requested string) (string, *apiFailure) {
	probeCtx, cancel := context.WithTimeout(ctx, modelProbeTimeout)
	defer cancel()

	client, err := nlp.NewClient(opts.NLPURL)
	if err != nil {
		return "", nlpUnreachableFailure()
	}
	health, err := client.Health(probeCtx)
	if err != nil {
		return "", nlpUnreachableFailure()
	}

	if requested != "" {
		if !offersVersion(health.AvailableVersions, requested) {
			return "", fail(http.StatusBadRequest, "unknown_model_version",
				"the sentiment service does not offer the requested model version",
				"the versions it does offer are: "+strings.Join(health.AvailableVersions, ", "))
		}
		return requested, nil
	}
	if health.ModelVersion == "" {
		// The service is up but has not finished loading a lexicon. Scoring
		// would fail on the first block and the report would have no version
		// to name, so this is answered as "not available yet".
		return "", nlpUnreachableFailure()
	}
	return health.ModelVersion, nil
}

// offersVersion reports whether version is on the service's menu.
//
// A service older than version selection advertises no menu at all; that
// carries no information to check against, so the request is passed through and
// the service's own refusal reports a wrong version.
func offersVersion(available []string, version string) bool {
	if len(available) == 0 {
		return true
	}
	for _, offered := range available {
		if offered == version {
			return true
		}
	}
	return false
}

// nlpUnreachableFailure is the one shape every "the service cannot answer"
// case takes, so a client can key on one code instead of three.
func nlpUnreachableFailure() *apiFailure {
	return fail(http.StatusServiceUnavailable, "nlp_unreachable",
		"the sentiment service is not answering, and a report has to name the model that produced it",
		"start the NLP service and retry; /api/health reports whether it is up")
}

// analysisOf describes the scoring run for one conversation.
//
// The read goes through the store's own (talker, kind) slot rather than a new
// query: that slot is the same one single flight is enforced on, so "a run is
// in progress" here means exactly what it means to POST /api/jobs.
func analysisOf(ctx context.Context, s *store.Store, talker string) (analysisView, *apiFailure) {
	job, ok, err := s.ActiveJob(ctx, talker, jobKindSentiment)
	if err != nil {
		return analysisView{}, fail(http.StatusInternalServerError, "internal",
			"the progress of this conversation's analysis run could not be read",
			"retry the request; if it keeps failing, check /api/health")
	}
	if !ok {
		// Zero counts and running=false, never a null block: the client renders
		// the same fields either way and only has one branch to write.
		return analysisView{}, nil
	}
	return analysisView{Running: true, Done: job.Done, Total: job.Total}, nil
}
