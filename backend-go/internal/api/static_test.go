package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointAtDist builds a minimal dist in a temp directory and points the web
// package's lookup at it for the duration of the test.
func pointAtDist(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id=app></div>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("create assets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	t.Setenv("LOVER_FRONTEND_DIST", dir)
}

// TestRouter_ServesDashboardAtRoot proves the wiring: the router, not just the
// web package, hands the root to the built index.html.
func TestRouter_ServesDashboardAtRoot(t *testing.T) {
	pointAtDist(t)
	engine := New(Options{Store: testStore(t), NLPURL: unreachableURL(t), Version: "test"})

	recorder := get(t, engine, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}

// TestRouter_DeepLinkFallsBackToIndex checks the SPA fallback through the real
// router, where the /api guard also lives.
func TestRouter_DeepLinkFallsBackToIndex(t *testing.T) {
	pointAtDist(t)
	engine := New(Options{Store: testStore(t), NLPURL: unreachableURL(t), Version: "test"})

	recorder := get(t, engine, "/views/sentiment")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /views/sentiment = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "id=app") {
		t.Errorf("body = %q, want index.html", recorder.Body.String())
	}
}

// TestRouter_UnknownAPIPathIsJSON is the boundary this task must not blur: a
// typo'd API path has to keep returning the error envelope, not the SPA's
// index.html. A client parsing JSON cannot do anything with a page of HTML.
func TestRouter_UnknownAPIPathIsJSON(t *testing.T) {
	pointAtDist(t)
	engine := New(Options{Store: testStore(t), NLPURL: unreachableURL(t), Version: "test"})

	recorder := get(t, engine, "/api/nope")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /api/nope = %d, want 404", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json: /api must never fall back to HTML", got)
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body, err)
	}
	for _, field := range []string{"code", "message", "hint"} {
		if _, ok := body[field]; !ok {
			t.Errorf("body = %v, want the field %q", body, field)
		}
	}
	if len(body) != 3 {
		t.Errorf("body = %v, want exactly code, message and hint", body)
	}
}

// TestRouter_BareAPIPathIsJSON covers "/api" with no trailing slash, which a
// naive prefix check would send to the SPA.
func TestRouter_BareAPIPathIsJSON(t *testing.T) {
	pointAtDist(t)
	engine := New(Options{Store: testStore(t), NLPURL: unreachableURL(t), Version: "test"})

	recorder := get(t, engine, "/api")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /api = %d, want 404", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
