package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file under dir, failing the test on any error.
func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// distDir builds a minimal but complete dist in a temp directory and points the
// lookup at it with the environment variable the design fixes (D3).
func distDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "index.html", "<!doctype html><div id=app></div>")
	writeFile(t, dir, "assets/app.js", "console.log('dashboard')")
	t.Setenv("LOVER_FRONTEND_DIST", dir)
	return dir
}

// TestHandler_MissingDistSaysHowToBuildIt is the honest-fallback guarantee: a
// checkout with no dist must still answer, with a page that tells the reader
// what to run. A panic or a 500 here is the white screen this whole design is
// arranged to avoid.
func TestHandler_MissingDistSaysHowToBuildIt(t *testing.T) {
	// An empty directory is a build that is not there: no index.html, so the
	// lookup reports missing even though the path exists.
	t.Setenv("LOVER_FRONTEND_DIST", t.TempDir())

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a missing build is an explanation, not an error", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "npm run build") {
		t.Errorf("body = %q, want it to name `npm run build`", body)
	}
	if !strings.Contains(body, "LOVER_FRONTEND_DIST") {
		t.Errorf("body = %q, want it to name LOVER_FRONTEND_DIST", body)
	}
}

// TestHandler_ServesIndexAndAssets checks the two files a page actually needs:
// index.html at the root and a hashed asset by its real path.
func TestHandler_ServesIndexAndAssets(t *testing.T) {
	distDir(t)

	root := httptest.NewRecorder()
	Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", root.Code)
	}
	if got := root.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", got)
	}
	if !strings.Contains(root.Body.String(), "id=app") {
		t.Errorf("GET / body = %q, want the built index.html", root.Body.String())
	}

	asset := httptest.NewRecorder()
	Handler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("GET /assets/app.js = %d, want 200", asset.Code)
	}
	if got := asset.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("asset Content-Type = %q, want text/javascript", got)
	}
	if !strings.Contains(asset.Body.String(), "dashboard") {
		t.Errorf("asset body = %q, want the real file's contents", asset.Body.String())
	}
}

// TestHandler_FallsBackToIndexForDeepLinks pins the SPA behaviour: a client
// route is not a file, so it has to return index.html and a 200, not a 404.
func TestHandler_FallsBackToIndexForDeepLinks(t *testing.T) {
	distDir(t)

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/some/deep/link", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /some/deep/link = %d, want 200 (index.html fallback)", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if !strings.Contains(recorder.Body.String(), "id=app") {
		t.Errorf("body = %q, want index.html for a client-side route", recorder.Body.String())
	}
}

// TestHandler_HeadIsAllowed makes sure the asset route is usable by clients that
// probe before downloading; a 405 here would break a cache or a crawler.
func TestHandler_HeadIsAllowed(t *testing.T) {
	distDir(t)

	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/assets/app.js", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD /assets/app.js = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("HEAD body length = %d, want 0", recorder.Body.Len())
	}
}
