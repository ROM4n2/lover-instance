// Package web serves the built Vue dashboard from the Go binary.
//
// The dashboard is a single-page application: one index.html, a bundle of
// hashed assets, and client-side routing driven by ?view=. That shape has two
// consequences this package has to handle and the /api routes must not:
//
//   - A deep link such as /anything is not a file on disk, so an unknown path
//     has to fall back to index.html instead of a 404. The client then reads
//     the path and the query string and draws the right view.
//   - A missing build is the normal state on a fresh checkout (design decision
//     D3 keeps dist out of the repository), so it must produce an honest,
//     readable page that says how to build one — never a panic and never a
//     500, because a white screen is the failure this project keeps designing
//     against.
//
// The handler here never sees /api traffic: the router installs it behind a
// NoRoute that answers /api/* with the API package's JSON error envelope. That
// separation is deliberate — an API typo must not be answered with index.html.
package web

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// missingPage is what a browser gets before the dashboard has been built. It is
// plain text on purpose: no CSS, no script, nothing that could itself fail to
// load. It names both ways to produce the dist directory so a reader can fix it
// without going to the source.
const missingPage = "dashboard assets are not installed\n\n" +
	"The Go server is running, but no built front-end was found next to it.\n" +
	"请先 npm run build，或设置 LOVER_FRONTEND_DIST 指向一个包含 index.html 的目录。\n\n" +
	"Build the dashboard with:\n" +
	"    cd frontend && npm install && npm run build\n\n" +
	"Or point the server at an existing build:\n" +
	"    LOVER_FRONTEND_DIST=/path/to/dist ./server\n\n" +
	"The API under /api is unaffected and still answers normally.\n"

// Handler returns the http.Handler that serves the built dashboard. It is wired
// into the router's NoRoute; non-/api paths arrive here and unknown paths fall
// back to index.html so the client-side router can take over.
//
// When no build is present every request gets the explanatory page with a 200:
// the server is up, and telling the browser "not found" would hide the one
// message the user actually needs.
func Handler() http.Handler {
	fs, ok := distFS()
	if !ok {
		return http.HandlerFunc(serveMissing)
	}
	return &assetHandler{fs: fs}
}

// assetHandler serves files from the dist filesystem and falls back to
// index.html for paths that do not name a file.
type assetHandler struct {
	fs http.FileSystem
}

func (h *assetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// The dashboard makes no mutating calls against static paths; anything
		// else is a client that took a wrong turn and should be told so.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.serveFile(w, r, assetName(r.URL.Path)) {
		return
	}
	// The SPA fallback: an unknown path is a client-side route, so hand back
	// index.html and let it render the view the URL names.
	if !h.serveIndex(w, r) {
		// A build with no index.html is broken rather than a deep link; say so
		// instead of returning an empty 200.
		serveMissing(w, r)
	}
}

// serveFile serves the named file and reports whether it existed. Directories
// are treated as misses so the caller falls through to index.html.
func (h *assetHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := h.fs.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	w.Header().Set("Content-Type", contentType(name))
	// Hashed asset names change with their content, so they are safe to cache
	// for a long time; index.html is served fresh (see serveIndex) so a rebuild
	// is picked up on the next load instead of after a cache expiry.
	if name != "index.html" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
	return true
}

// serveIndex serves index.html with caching disabled. It is the deep-link and
// root response, and it must never be answered from a stale cache: the hashed
// filenames it references are the only thing tying a reload to a new build.
func (h *assetHandler) serveIndex(w http.ResponseWriter, r *http.Request) bool {
	file, err := h.fs.Open("index.html")
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", info.ModTime(), file)
	return true
}

// serveMissing writes the explanatory page with a 200. The status is genuine:
// the server answered this request; what is missing is a build artifact, and
// the body says exactly that.
func serveMissing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, missingPage)
}

// assetName turns a URL path into the name a file on disk would have. The root
// maps to index.html; everything else keeps its relative shape.
func assetName(urlPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		return "index.html"
	}
	return name
}

// contentType names the type explicitly for the extensions a dashboard ships,
// because the platform MIME table is not identical everywhere (a Windows
// registry without a .js mapping served the bundle as octet-stream, which some
// browsers refuse to execute under a module script tag).
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	}
	if detected := mime.TypeByExtension(path.Ext(name)); detected != "" {
		return detected
	}
	return "application/octet-stream"
}
