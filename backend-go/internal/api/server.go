package api

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/store"
	"lover-instance/backend-go/internal/web"
)

// jobKindSentiment is the analysis_jobs.kind this package produces, watches and
// cancels. It lives here because it is shared: GET /api/overview watches the
// slot, POST /api/jobs fills it and DELETE /api/jobs/:id empties it, so all
// three have to name the same thing. cmd/analyze writes under the same string,
// which is what makes the CLI and the dashboard one single-flight slot rather
// than two.
const jobKindSentiment = "sentiment"

// Options is everything the API server needs from its caller. New does not
// reach for a global to find any of it.
type Options struct {
	// Store is the one connection to the chat database. The API reads through
	// it and never opens a second one: the pragmas are per connection, and a
	// second writer would race the first.
	Store *store.Store

	// NLPURL is the base URL of the sentiment service, probed by /api/health.
	NLPURL string

	// Version is this binary's version string, echoed by /api/health so a
	// dashboard left open in a browser can say which server is behind it.
	Version string

	// Log receives the request log; nil means os.Stderr.
	//
	// It is a field rather than a direct write to os.Stderr so a test can
	// capture the log and assert on what is *not* in it — the privacy rule is
	// only worth having if something checks it.
	Log io.Writer

	// Jobs is the manager the analysis workers belong to. A nil means New
	// builds one from Store and NLPURL, which is what the tests use;
	// cmd/server builds it explicitly so it can cancel every worker on the way
	// out instead of leaving goroutines writing to a database it is closing.
	Jobs *JobManager

	// ExportsDir is where POST /api/export writes. Empty means the location the
	// design fixes, under LOCALAPPDATA.
	ExportsDir string

	// manager is the job manager the handlers actually use: Jobs when it was
	// given, otherwise the one New built. It is resolved once, in New, so no
	// handler has to ask twice and no handler can disagree with another.
	manager *JobManager
}

// New assembles the router. It does not listen: which interface and port to
// bind is the command's decision, and a test gets an engine it can drive with
// httptest without a socket.
func New(opts Options) *gin.Engine {
	out := opts.Log
	if out == nil {
		out = os.Stderr
	}
	logger := log.New(out, "", 0)

	// Resolved once, here, so the handlers below share one manager. A nil
	// Store never reaches this: cmd/server opens the database before it builds
	// the router, and a test that has no database has no business starting
	// analysis runs either.
	opts.manager = opts.Jobs
	if opts.manager == nil && opts.Store != nil {
		opts.manager = NewJobManager(opts.Store, opts.NLPURL)
	}

	// gin.New(), never gin.Default(), and the reason is not style.
	//
	// gin.Default() installs gin.Logger(), whose default formatter prints the
	// request path together with its raw query string. This project's URLs
	// carry ?talker=<wxid> (design decision D4), so that one line would put a
	// conversation key into stdout — in a terminal, a scrollback buffer, or a
	// bug report pasted from it. The repository's rule is that a talker, a wxid
	// and message text never reach a log, so the logger has to be one that
	// cannot print them.
	//
	// For the same reason this router installs recovery() rather than
	// gin.Recovery(): on a panic, gin's own dumps the request headers and the
	// full URL, query string included. See recovery() for what replaces it.
	// Recovery is installed after the logger so the 500 it writes is the status
	// the log line reports.
	r := gin.New()
	r.Use(requestLog(logger), recovery(logger))

	// The group is created once here so every endpoint hangs off the same
	// prefix instead of each spelling "/api" out.
	apiGroup := r.Group("/api")
	apiGroup.GET("/health", healthHandler(opts))
	apiGroup.GET("/overview", overviewHandler(opts))
	apiGroup.POST("/jobs", jobsCreateHandler(opts))
	apiGroup.GET("/jobs/:id", jobStatusHandler(opts))
	apiGroup.DELETE("/jobs/:id", jobCancelHandler(opts))
	apiGroup.POST("/export", exportHandler(opts))
	apiGroup.POST("/demo", demoHandler(opts))
	apiGroup.POST("/import", importHandler(opts))
	apiGroup.POST("/scan-export", scanExportHandler(opts))
	apiGroup.POST("/sync-sidecar", syncSidecarHandler(opts))
	apiGroup.GET("/highlights", highlightsHandler(opts))

	// Everything the API did not match is offered to the static dashboard,
	// except /api itself. The check comes first and is the important part: an
	// unknown /api path is a client mistake that must be told so in the JSON
	// envelope it already parses, never answered with index.html. A SPA
	// fallback that swallowed /api typos would turn "job_not_found" into a
	// page of HTML the caller cannot read.
	static := web.Handler()
	r.NoRoute(func(c *gin.Context) {
		if isAPIPath(c.Request.URL.Path) {
			writeError(c, http.StatusNotFound, "not_found",
				"no API endpoint exists at this path",
				"check the path; the dashboard's endpoints are listed under /api/")
			return
		}
		static.ServeHTTP(c.Writer, c.Request)
	})
	return r
}

// isAPIPath reports whether p names the API namespace, including the bare
// "/api" and anything beneath it. Everything else is the dashboard's.
func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// requestLog prints one line per request carrying four fields and nothing else:
// the method, the matched route template, the status and how long it took.
//
// The route template is c.FullPath() — "/api/health", "/api/jobs/:id" — which
// is the pattern the handler was registered under rather than the URL the
// client sent. Path parameters and the query string are therefore both
// structurally absent: there is no value to leak because no value is read.
// A request that matched no route has an empty template and is logged as "-";
// printing the requested path instead would defeat the point for exactly the
// requests an attacker or a broken client controls.
func requestLog(logger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		// log.Logger writes a whole line under its own mutex, so concurrent
		// requests cannot interleave halfway through one.
		logger.Printf("api: %s %s %d %s", c.Request.Method, routeTemplate(c), c.Writer.Status(),
			time.Since(started).Round(time.Microsecond))
	}
}

// routeTemplate is the pattern a handler was registered under, or "-" when the
// request matched no route at all.
//
// It is a function because two middlewares need it and both must be unable to
// print a path parameter or a query value: c.FullPath() is "/api/jobs/:id", not
// the id that was asked for.
func routeTemplate(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return "-"
}

// recovery turns a panic into this package's error envelope and one log line
// that names nobody.
//
// gin.Recovery() is not used and cannot be. On a panic it dumps the request
// headers and the full URL — query string included — and this project's URLs
// carry ?talker=<wxid> (design decision D4). That dump is exactly the leak the
// rest of this package is built to prevent, in the one code path that runs when
// something has already gone wrong and nobody is watching the shape of the
// output.
//
// What is logged is the method, the route template and the panic value's *type*.
// The type and nothing more: a panic value can be any value at all, including a
// string built from the request, and printing it would reintroduce the leak one
// line lower down.
func recovery(logger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			logger.Printf("api: panic %s %s %T", c.Request.Method, routeTemplate(c), recovered)
			if c.Writer.Written() {
				// Something has already been sent; a JSON body appended to it
				// would be a second, meaningless response. Abort so gin stops
				// walking the chain.
				c.Abort()
				return
			}
			// The 500 is written through writeError like every other failure, so
			// the client sees the envelope it already knows how to render.
			writeError(c, http.StatusInternalServerError, "internal",
				"the server hit an unexpected error while handling this request",
				"retry the request; if it keeps failing, restart the server")
		}()
		c.Next()
	}
}
