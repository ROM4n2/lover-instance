package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/jobs"
	"lover-instance/backend-go/internal/store"
)

// fakeScorer is a sentiment service that can be made slow, or made to fail.
//
// The delays matter: a cancel test has to catch a run in the middle, and the
// only way to have a middle is a service that takes its time. The call counter
// is what proves a cancelled run stops sending blocks.
type fakeScorer struct {
	server *httptest.Server

	mu     sync.Mutex
	calls  int
	delay  time.Duration
	status int // 0 means 200; anything else is returned as an error status
}

func newFakeScorer(t *testing.T) *fakeScorer {
	t.Helper()
	scorer := &fakeScorer{delay: 10 * time.Millisecond}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, healthyNLP)
	})
	mux.HandleFunc("/score", scorer.score)
	scorer.server = httptest.NewServer(mux)
	t.Cleanup(scorer.server.Close)
	return scorer
}

func (f *fakeScorer) score(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ModelVersion string `json:"model_version"`
		Items        []struct {
			MsgID int64 `json:"msg_id"`
		} `json:"items"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&payload)

	f.mu.Lock()
	f.calls++
	delay, status := f.delay, f.status
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		// A 5xx with no body: the client reads the status code and never the
		// message, so nothing the service saw can travel back with the error.
		w.WriteHeader(status)
		return
	}

	results := make([]map[string]any, 0, len(payload.Items))
	for _, item := range payload.Items {
		results = append(results, map[string]any{
			"msg_id":    item.MsgID,
			"polarity":  0.5,
			"emotion":   "乐",
			"intensity": 0.5,
			"tokens":    []string{"你好"},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model_version": payload.ModelVersion,
		"results":       results,
	})
}

func (f *fakeScorer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeScorer) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// jobEngine wires a store, a scorer and an explicit manager, so the test can
// stop every worker before the store is closed.
func jobEngine(t *testing.T, s *store.Store, scorer *fakeScorer) (*gin.Engine, *JobManager) {
	t.Helper()
	manager := NewJobManager(s, scorer.server.URL)
	t.Cleanup(manager.Shutdown)
	return New(Options{Store: s, NLPURL: scorer.server.URL, Version: "test", Log: io.Discard, Jobs: manager}), manager
}

// waitFor polls until condition holds, and fails the test with what it was
// waiting for if it never does.
func waitFor(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// waitJobStatus polls GET /api/jobs/:id until the run reaches status.
func waitJobStatus(t *testing.T, engine *gin.Engine, id int64, status string, timeout time.Duration) jobResponse {
	t.Helper()
	var last jobResponse
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		recorder := do(t, engine, http.MethodGet, fmt.Sprintf("/api/jobs/%d", id), "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /api/jobs/%d = %d, want 200: %s", id, recorder.Code, recorder.Body)
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &last); err != nil {
			t.Fatalf("decode job body %q: %v", recorder.Body, err)
		}
		if last.Status == status {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %d stayed %q, want %q within %s (last body %+v)", id, last.Status, status, timeout, last)
	return last
}

// TestJobs_TwoStartsYieldOneRunAndOneConflict is single flight as the dashboard
// sees it.
//
// The database already makes a second concurrent run impossible, but the API
// has a second obligation: the loser must be told 409 *and* given the id of the
// run that is in the way, so the client can watch that one instead of showing a
// "start failed" box next to a progress bar it cannot attach to anything.
func TestJobs_TwoStartsYieldOneRunAndOneConflict(t *testing.T) {
	s := testStore(t)
	scorer := newFakeScorer(t)
	engine, _ := jobEngine(t, s, scorer)

	const talker = "wxid_race"
	for i := 0; i < 40; i++ {
		seedText(t, s, fmt.Sprintf("k%d", i), talker, testBaseMs+int64(i*1000), i%2 == 0, "你好")
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]string, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recorder := do(t, engine, http.MethodPost, "/api/jobs", fmt.Sprintf(`{"talker":%q}`, talker))
			codes[i], bodies[i] = recorder.Code, recorder.Body.String()
		}(i)
	}
	wg.Wait()

	accepted, conflicted := 0, 0
	for i, code := range codes {
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
			var body jobConflictResponse
			if err := json.Unmarshal([]byte(bodies[i]), &body); err != nil {
				t.Fatalf("decode conflict body %q: %v", bodies[i], err)
			}
			if body.Code != "job_active" {
				t.Errorf("conflict code = %q, want %q", body.Code, "job_active")
			}
			if body.ID == 0 {
				t.Error("conflict body carries no id: the client cannot attach to the run that is in the way")
			}
		default:
			t.Errorf("POST /api/jobs = %d, want 202 or 409: %s", code, bodies[i])
		}
	}
	if accepted != 1 || conflicted != 1 {
		t.Fatalf("codes = %v, want exactly one 202 and one 409: two concurrent starts must not both succeed", codes)
	}

	var active int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM analysis_jobs WHERE status IN ('queued','running')`).Scan(&active); err != nil {
		t.Fatalf("count active jobs: %v", err)
	}
	if active != 1 {
		t.Errorf("active jobs = %d, want 1: uq_jobs_active must leave the slot with one owner", active)
	}
}

// TestJobs_CancelStopsTheRunAndClosesTheRow is the cancel path end to end.
//
// Two things have to be true afterwards, and they are different things: the row
// says canceled, and no block is sent again. A cancel that only wrote the row
// would leave the worker scoring, and a cancel that only stopped the worker
// would leave a 'running' row holding the slot forever.
func TestJobs_CancelStopsTheRunAndClosesTheRow(t *testing.T) {
	s := testStore(t)
	scorer := newFakeScorer(t)
	scorer.delay = 30 * time.Millisecond
	engine, _ := jobEngine(t, s, scorer)

	const talker = "wxid_to_cancel"
	// Enough messages for several blocks at nlp.MaxBatchSize, so the run is
	// still going when the DELETE arrives.
	for i := 0; i < 900; i++ {
		seedText(t, s, fmt.Sprintf("k%d", i), talker, testBaseMs+int64(i*1000), i%2 == 0, "你好")
	}

	recorder := do(t, engine, http.MethodPost, "/api/jobs", fmt.Sprintf(`{"talker":%q}`, talker))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST /api/jobs = %d, want 202: %s", recorder.Code, recorder.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created job: %v", err)
	}

	waitFor(t, "the first scoring block to be sent", 3*time.Second, func() bool { return scorer.count() >= 1 })

	recorder = do(t, engine, http.MethodDelete, fmt.Sprintf("/api/jobs/%d", created.ID), "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("DELETE /api/jobs/%d = %d, want 202: %s", created.ID, recorder.Code, recorder.Body)
	}

	closed := waitJobStatus(t, engine, created.ID, jobs.StatusCanceled, 5*time.Second)
	if closed.Error == "" {
		t.Error("job error is empty, want the reason it stopped: a canceled row that says why is what the dashboard shows")
	}
	if strings.Contains(closed.Error, talker) {
		t.Errorf("job error = %q, want no talker anywhere in a job row", closed.Error)
	}

	// No new block may be sent after the run has closed. The allowance for one
	// is the block that was already in flight when the DELETE landed; anything
	// beyond that is a worker that did not notice.
	settled := scorer.count()
	time.Sleep(300 * time.Millisecond)
	if got := scorer.count(); got != settled {
		t.Errorf("scoring calls went from %d to %d after the run was cancelled, want no further blocks", settled, got)
	}
}

// TestJobs_FailureClosesTheRowWithoutTheMessageText covers the failure path.
//
// The error column is shown in a dashboard and ends up in screenshots, so it
// must describe the fault without carrying anything the fault was processing.
func TestJobs_FailureClosesTheRowWithoutTheMessageText(t *testing.T) {
	s := testStore(t)
	scorer := newFakeScorer(t)
	scorer.setStatus(http.StatusInternalServerError)
	engine, _ := jobEngine(t, s, scorer)

	const talker = "wxid_failing"
	const body = "supersecret message body"
	for i := 0; i < 3; i++ {
		seedText(t, s, fmt.Sprintf("k%d", i), talker, testBaseMs+int64(i*1000), true, body)
	}

	recorder := do(t, engine, http.MethodPost, "/api/jobs", fmt.Sprintf(`{"talker":%q}`, talker))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST /api/jobs = %d, want 202: %s", recorder.Code, recorder.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created job: %v", err)
	}

	failed := waitJobStatus(t, engine, created.ID, jobs.StatusFailed, 10*time.Second)
	if failed.Error == "" {
		t.Fatal("job error is empty, want the failure that stopped the run")
	}
	if strings.Contains(failed.Error, body) {
		t.Errorf("job error = %q, want no message text: the error column is shown to a person", failed.Error)
	}
	if strings.Contains(failed.Error, talker) {
		t.Errorf("job error = %q, want no talker", failed.Error)
	}
}

// TestPanicIsAnsweredAsInternalAndDoesNotLogTheURL is the recovery guard.
//
// gin.Recovery() would satisfy the first half and fail the second: its dump
// carries the request line, query string included, and this project's URLs
// carry ?talker=<wxid>. The panic value here is a plain string, which is the
// easy case — a panic value can be anything, so what is logged is its type.
func TestPanicIsAnsweredAsInternalAndDoesNotLogTheURL(t *testing.T) {
	var logged bytes.Buffer

	// gin's own writers are redirected as well: an implementation that fell
	// back to gin.Recovery() writes there, and this test must catch that rather
	// than let it escape into stdout.
	originalWriter, originalErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logged, &logged
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = originalWriter, originalErrorWriter })

	engine := New(Options{Store: testStore(t), NLPURL: "", Version: "test", Log: &logged})
	// Registered on the engine New returned, not in New: the production route
	// table stays free of an endpoint that exists to panic.
	engine.GET("/test/panic", func(_ *gin.Context) {
		panic("boom")
	})

	recorder := do(t, engine, http.MethodGet, "/test/panic?talker=wxid_secret", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("panic route = %d, want 500: %s", recorder.Code, recorder.Body)
	}
	// The body is checked without a fatal decode so the log assertions below
	// still run: an implementation whose log leaks the talker must fail on that
	// fact, not on an unrelated missing field.
	var envelope errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Errorf("body = %q, want the %d-field error envelope", recorder.Body, 3)
	} else if envelope.Code != "internal" {
		t.Errorf("code = %q, want %q", envelope.Code, "internal")
	}

	output := logged.String()
	if output == "" {
		t.Fatal("nothing was logged, so this test would pass on a recovery middleware that logged nothing at all")
	}
	if !strings.Contains(output, "panic") {
		t.Errorf("log = %q, want a line saying a panic happened", output)
	}
	if strings.Contains(output, "wxid_secret") {
		t.Errorf("log = %q, want no query string: the talker travels in the URL and must never reach a log", output)
	}
	if strings.Contains(output, "?") {
		t.Errorf("log = %q, want no URL at all: a query string is what gin.Recovery() dumps", output)
	}
}
