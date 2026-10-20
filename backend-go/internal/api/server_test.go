package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/store"
)

// TestMain silences gin's route-registration banner. It is debug output about
// the router, not about the code under test, and it would otherwise print into
// the middle of a failing test's output.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// testStore opens a fresh database with the schema in place. Every test gets
// its own file: analysis_jobs carries a partial unique index, and a shared
// database would let one test's active job refuse another test's insert.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	dsn, err := store.DSN(filepath.Join(t.TempDir(), "data.db"))
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

// get issues one request against the engine and returns the recorder.
func get(t *testing.T, engine *gin.Engine, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

// decodeHealth decodes a health body, failing the test on a shape it cannot
// read. Decoding into the response type itself is deliberate: a hand-written
// mirror in the test would keep passing after the real field names changed.
func decodeHealth(t *testing.T, body []byte) healthResponse {
	t.Helper()
	var response healthResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode health body %q: %v", body, err)
	}
	return response
}

// unreachableURL returns a URL nothing is listening on: it binds a port, notes
// the address and closes the listener, so a connection attempt is refused
// immediately instead of waiting for the network to give up on a reserved
// address.
func unreachableURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return "http://" + addr
}

// fakeNLP serves the /health contract the real service implements.
func fakeNLP(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestHealth_DegradesWhenTheNLPServiceIsNotRunning is the whole reason this
// endpoint does not 500 on a missing dependency.
//
// The dashboard is opened before the Python service on a cold machine, and the
// badge in the corner is what tells the user "the sentiment service is not
// running". A failed request cannot say that: the page only knows the API is
// broken, and every panel that would have worked is gone with it.
func TestHealth_DegradesWhenTheNLPServiceIsNotRunning(t *testing.T) {
	engine := New(Options{Store: testStore(t), NLPURL: unreachableURL(t), Version: "test"})

	recorder := get(t, engine, "/api/health")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200: an unreachable NLP service is a degraded badge, not a failed request", recorder.Code)
	}

	response := decodeHealth(t, recorder.Body.Bytes())
	if response.Status != statusDegraded {
		t.Errorf("status = %q, want %q", response.Status, statusDegraded)
	}
	if response.DB != dbOK {
		t.Errorf("db = %q, want %q: the database is fine, so the degradation must be attributed to the service that is not", response.DB, dbOK)
	}
	if response.NLP.Reachable {
		t.Error("nlp.reachable = true, want false: nothing is listening on that port")
	}
	if response.NLP.Status != nlpUnreachable {
		t.Errorf("nlp.status = %q, want %q", response.NLP.Status, nlpUnreachable)
	}
	if response.NLP.AvailableVersions == nil {
		t.Error("nlp.available_versions = null, want an empty list: the client renders the menu, and a null is not a menu")
	}
}

// TestHealth_ReportsTheVersionsTheServiceAdvertises checks that a healthy
// service is reported as healthy and that its menu travels through untouched.
func TestHealth_ReportsTheVersionsTheServiceAdvertises(t *testing.T) {
	service := fakeNLP(t, `{"status":"ready","model_loaded":true,"model_version":"lexicon-hownet-v1","available_versions":["lexicon-hownet-v1","lexicon-v2"]}`)
	engine := New(Options{Store: testStore(t), NLPURL: service.URL, Version: "test"})

	response := decodeHealth(t, get(t, engine, "/api/health").Body.Bytes())
	if response.Status != statusOK {
		t.Fatalf("status = %q, want %q", response.Status, statusOK)
	}
	if !response.NLP.Reachable {
		t.Error("nlp.reachable = false, want true")
	}
	if response.NLP.Status != "ready" || response.NLP.ModelVersion != "lexicon-hownet-v1" {
		t.Errorf("nlp = %+v, want the service's own status and default version", response.NLP)
	}
	want := []string{"lexicon-hownet-v1", "lexicon-v2"}
	if len(response.NLP.AvailableVersions) != len(want) {
		t.Fatalf("available_versions = %v, want %v", response.NLP.AvailableVersions, want)
	}
	for i, version := range want {
		if response.NLP.AvailableVersions[i] != version {
			t.Errorf("available_versions[%d] = %q, want %q: the menu must be passed through in order", i, response.NLP.AvailableVersions[i], version)
		}
	}
	if response.Version != "test" {
		t.Errorf("version = %q, want %q: the health block names the build that answered", response.Version, "test")
	}
}

// TestHealth_ReportsTheActiveJobAsAbsoluteCounts covers both halves of the job
// field: the newest active run while one exists, and null when none does.
func TestHealth_ReportsTheActiveJobAsAbsoluteCounts(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	engine := New(Options{Store: s, NLPURL: fakeNLP(t, `{"status":"ready","model_version":"v1"}`).URL, Version: "test"})

	// Nothing has run yet: the field must be null rather than a zero-valued
	// job, which would render as "0 / 0 done" on a dashboard that has never
	// started anything.
	recorder := get(t, engine, "/api/health")
	if !strings.Contains(recorder.Body.String(), `"job":null`) {
		t.Fatalf("body = %s, want \"job\":null when no run is active", recorder.Body)
	}

	job, err := s.InsertJob(ctx, "wxid_talker", "sentiment", "v1")
	if err != nil {
		t.Fatalf("InsertJob() error: %v", err)
	}
	if _, err := s.TransitionJob(ctx, job.ID, []string{jobs.StatusQueued}, jobs.StatusRunning, ""); err != nil {
		t.Fatalf("TransitionJob(queued→running) error: %v", err)
	}
	// Absolute counts, not a percentage: these are the numbers a resumed run
	// and a fresh run both report.
	if err := s.SetJobProgress(ctx, job.ID, 1200, 22093); err != nil {
		t.Fatalf("SetJobProgress() error: %v", err)
	}

	recorder = get(t, engine, "/api/health")
	response := decodeHealth(t, recorder.Body.Bytes())
	if response.Job == nil {
		t.Fatal("job = null, want the running job")
	}
	if response.Job.ID != job.ID {
		t.Errorf("job.id = %d, want %d", response.Job.ID, job.ID)
	}
	if response.Job.Status != jobs.StatusRunning {
		t.Errorf("job.status = %q, want %q", response.Job.Status, jobs.StatusRunning)
	}
	if response.Job.Done != 1200 || response.Job.Total != 22093 {
		t.Errorf("job done/total = %d/%d, want 1200/22093: absolute message counts, never a percentage", response.Job.Done, response.Job.Total)
	}
	// The conversation key is in the row and must not be in the body.
	if strings.Contains(recorder.Body.String(), "wxid_talker") {
		t.Errorf("body = %s, want no talker: the health block names a job by its id only", recorder.Body)
	}
}

// TestWriteError_HasAllThreeFields pins the error envelope. The three fields
// are asserted as JSON keys rather than as a decoded struct so a field that
// gains `omitempty` later fails here instead of silently disappearing from the
// dashboard's error box.
func TestWriteError_HasAllThreeFields(t *testing.T) {
	// A router built inside the test, not in New: the production route table
	// stays free of endpoints that exist only to be tested.
	engine := gin.New()
	engine.GET("/test/error", func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "job_not_found", "that job does not exist", "pick one from the job list")
	})

	recorder := get(t, engine, "/test/error")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
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
		t.Errorf("body = %v, want exactly code, message and hint: any extra field is something that can leak", body)
	}
	if body["code"] != "job_not_found" || body["message"] != "that job does not exist" || body["hint"] != "pick one from the job list" {
		t.Errorf("body = %v, want the three values the caller supplied", body)
	}
}

// TestRequestLog_NeverPrintsTheQueryString is this task's privacy guard.
//
// The talker lives in the URL (design decision D4), so the logger is the one
// component that can put a wxid into a terminal, a scrollback buffer or a pasted
// bug report. This test is what makes that impossible to regress: gin's own
// logger prints the raw request URI and would fail here, which is exactly why
// New builds its router from gin.New() and installs its own.
func TestRequestLog_NeverPrintsTheQueryString(t *testing.T) {
	service := fakeNLP(t, `{"status":"ready","model_version":"v1"}`)

	var logged bytes.Buffer
	// gin.Logger writes to gin.DefaultWriter, which it reads when the handler
	// is installed. Pointing it at the same buffer means a regression to
	// gin.Default() is caught by this test rather than escaping into stdout.
	originalWriter, originalErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logged, &logged
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = originalWriter, originalErrorWriter })

	engine := New(Options{Store: testStore(t), NLPURL: service.URL, Version: "test", Log: &logged})

	recorder := get(t, engine, "/api/health?talker=wxid_secret")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if logged.Len() == 0 {
		t.Fatal("no request log line was written, so this test would pass on a logging regression that printed nothing at all")
	}
	output := logged.String()
	if strings.Contains(output, "wxid_secret") {
		t.Errorf("log output = %q, want no query string: the talker travels in the URL and must never reach a log", output)
	}
	// The line still has to be useful, or the guard would be satisfied by
	// logging nothing.
	if !strings.Contains(output, "GET") || !strings.Contains(output, "/api/health") {
		t.Errorf("log output = %q, want the method and the route template", output)
	}
}
