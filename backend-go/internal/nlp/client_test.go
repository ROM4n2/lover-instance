package nlp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a client pointed at srv with the retry backoff stubbed
// out, so the schedule can be exercised without spending it.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// scoreHandler replies with one result per request item, echoing the ids back.
func scoreHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var request scoreRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := scoreResponse{ModelVersion: request.ModelVersion}
		for _, item := range request.Items {
			response.Results = append(response.Results, scoreResult{
				MsgID: item.MsgID, Polarity: 0.5, Emotion: "乐", Intensity: 0.4,
				Tokens: []string{"你好", "[捂脸]"},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

// TestNewClient_SetsANonZeroTimeout guards the rule that a zero-value
// http.Client, which never times out, must not appear in this codebase.
func TestNewClient_SetsANonZeroTimeout(t *testing.T) {
	c, err := NewClient("http://127.0.0.1:8000")
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}
	if c.http.Timeout <= 0 {
		t.Errorf("client timeout = %v, want a positive value: a zero-value client never times out", c.http.Timeout)
	}
	if c.http.Timeout != hardTimeout {
		t.Errorf("client timeout = %v, want the %v hard ceiling", c.http.Timeout, hardTimeout)
	}
}

func TestNewClient_RejectsAURLLessInput(t *testing.T) {
	if _, err := NewClient("   "); err == nil {
		t.Error("NewClient(\"   \") = nil error, want an error")
	}
	if _, err := NewClient("127.0.0.1:8000"); err == nil {
		t.Error("NewClient(schemeless) = nil error, want an error")
	}
}

func TestScore_ReturnsOneResultPerItemInOrder(t *testing.T) {
	srv := httptest.NewServer(scoreHandler(t))
	defer srv.Close()

	items := []Item{{MsgID: 11, Content: "a"}, {MsgID: 22, Content: "b"}, {MsgID: 33, Content: "c"}}
	results, err := newTestClient(t, srv).Score(context.Background(), "lexicon-hownet-v1", items)
	if err != nil {
		t.Fatalf("Score() error: %v", err)
	}
	if len(results) != len(items) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(items))
	}
	for i, got := range results {
		if got.MsgID != items[i].MsgID {
			t.Errorf("results[%d].MsgID = %d, want %d", i, got.MsgID, items[i].MsgID)
		}
		if len(got.Tokens) == 0 {
			t.Errorf("results[%d].Tokens is empty: word-frequency aggregation depends on it", i)
		}
	}
}

// TestScore_RejectsAMisalignedResponse is the guard against silently attaching
// one message's score to another, which would produce a plausible but wrong
// sentiment curve that nothing downstream could detect.
func TestScore_RejectsAMisalignedResponse(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"too few results", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(scoreResponse{Results: []scoreResult{{MsgID: 11}}})
		}},
		{"too many results", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(scoreResponse{Results: []scoreResult{{MsgID: 11}, {MsgID: 22}, {MsgID: 33}}})
		}},
		{"same count but out of order", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(scoreResponse{Results: []scoreResult{{MsgID: 22}, {MsgID: 11}}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			items := []Item{{MsgID: 11, Content: "a"}, {MsgID: 22, Content: "b"}}
			results, err := newTestClient(t, srv).Score(context.Background(), "v", items)
			if err == nil {
				t.Fatalf("Score() = %v, nil error; want an error", results)
			}
		})
	}
}

// TestScore_RetriesServerErrorsExactlyTwice pins the retry budget: one attempt
// plus the two the design allows, and no more. Retrying is bounded here
// because the alternative -- a task-level retry -- would restart work that may
// already have run for an hour.
func TestScore_RetriesServerErrorsExactlyTwice(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Score(context.Background(), "v", []Item{{MsgID: 1, Content: "a"}})
	if err == nil {
		t.Fatal("Score() against a always-500 service = nil error, want an error")
	}
	if got := atomic.LoadInt32(&attempts); got != maxAttempts {
		t.Errorf("server saw %d attempts, want %d (one try plus two retries)", got, maxAttempts)
	}
}

func TestScore_SucceedsAfterATransientServerError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		scoreHandler(t)(w, r)
	}))
	defer srv.Close()

	results, err := newTestClient(t, srv).Score(context.Background(), "v", []Item{{MsgID: 1, Content: "a"}})
	if err != nil {
		t.Fatalf("Score() error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

// TestScore_DoesNotRetryAClientError keeps a 4xx from being repeated: the
// request is wrong and will stay wrong.
func TestScore_DoesNotRetryAClientError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"code":"BATCH_TOO_LARGE"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Score(context.Background(), "v", []Item{{MsgID: 1, Content: "a"}})
	if err == nil {
		t.Fatal("Score() = nil error for a 413, want an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("server saw %d attempts, want 1: a 413 must not be retried", got)
	}
	if !strings.Contains(err.Error(), "BATCH_TOO_LARGE") {
		t.Errorf("error = %v, want it to carry the service's error code", err)
	}
}

// TestScore_NeverEchoesMessageText is the privacy guard: the service has seen
// the message text, and an error string is the easiest place for that text to
// leak into a log.
func TestScore_NeverEchoesMessageText(t *testing.T) {
	const secret = "SENTINEL_MESSAGE_BODY_9f3a"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"code":"BOOM","message":"failed on %s"}`, secret)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Score(context.Background(), "v", []Item{{MsgID: 1, Content: secret}})
	if err == nil {
		t.Fatal("Score() = nil error, want an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error string contains the message body: %v", err)
	}
}

func TestScore_RejectsAnOversizedBatchLocally(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		scoreHandler(t)(w, r)
	}))
	defer srv.Close()

	items := make([]Item, MaxBatchSize+1)
	for i := range items {
		items[i] = Item{MsgID: int64(i), Content: "x"}
	}
	if _, err := newTestClient(t, srv).Score(context.Background(), "v", items); err == nil {
		t.Fatal("Score(257 items) = nil error, want an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 0 {
		t.Errorf("server saw %d requests, want 0: an oversized batch must be refused before it is sent", got)
	}
}

func TestScore_EmptyBatchMakesNoRequest(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
	}))
	defer srv.Close()

	results, err := newTestClient(t, srv).Score(context.Background(), "v", nil)
	if err != nil {
		t.Fatalf("Score(nil) error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
	if got := atomic.LoadInt32(&attempts); got != 0 {
		t.Errorf("server saw %d requests for an empty batch, want 0", got)
	}
}

// TestCall_DoesNotRetryOnAnExpiredContext stops a cancelled block from being
// retried against a service that is not going to answer.
func TestCall_DoesNotRetryOnAnExpiredContext(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := c.Score(ctx, "v", []Item{{MsgID: 1, Content: "a"}}); err == nil {
		t.Fatal("Score() = nil error under an expired context, want an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("server saw %d attempts, want 1: an expired context must stop the retry loop", got)
	}
}

func TestHealth_DistinguishesLoadingFromReady(t *testing.T) {
	for _, status := range []string{"loading", "ready"} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "model_loaded": false})
			}))
			defer srv.Close()

			got, err := newTestClient(t, srv).Health(context.Background())
			if err != nil {
				t.Fatalf("Health() error: %v", err)
			}
			if got.Status != status {
				t.Errorf("Status = %q, want %q", got.Status, status)
			}
		})
	}
}

func TestHealth_RejectsAnUnrecognizedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "banana"})
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).Health(context.Background()); err == nil {
		t.Error("Health() = nil error for an unrecognized status, want an error")
	}
}
