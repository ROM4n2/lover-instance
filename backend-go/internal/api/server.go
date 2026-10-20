package api

import (
	"io"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/store"
)

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
	// gin.Recovery() is deliberately absent for the same reason: on a panic it
	// dumps the request headers and the full URL, query string included. A
	// recovery middleware that respects the rule is a later task's decision;
	// nothing registered here can panic.
	r := gin.New()
	r.Use(requestLog(logger))

	// The group is created once here so the endpoints that follow in later
	// tasks hang off the same prefix instead of each spelling "/api" out.
	apiGroup := r.Group("/api")
	apiGroup.GET("/health", healthHandler(opts))
	return r
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
		route := c.FullPath()
		if route == "" {
			route = "-"
		}
		// log.Logger writes a whole line under its own mutex, so concurrent
		// requests cannot interleave halfway through one.
		logger.Printf("api: %s %s %d %s", c.Request.Method, route, c.Writer.Status(),
			time.Since(started).Round(time.Microsecond))
	}
}
