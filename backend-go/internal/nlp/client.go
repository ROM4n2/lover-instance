// Package nlp talks to the stateless NLP microservice. It sends batches of
// message text and receives per-message scores; it never sends identity, and
// the service never touches the database.
package nlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// MaxBatchSize mirrors the service's contract. It is enforced here too, so
	// an oversized batch is a programming error caught locally rather than a
	// 413 discovered in the middle of a long run.
	MaxBatchSize = 256

	// connectTimeout bounds dialing a service that may not be running.
	connectTimeout = 3 * time.Second
	// hardTimeout is the client-level ceiling. The caller supplies the
	// per-block deadline (5 minutes); this only guarantees that a caller who
	// forgets one cannot hang forever. A zero-value http.Client never times
	// out at all, which is why the field is set in NewClient and asserted.
	hardTimeout = 10 * time.Minute

	maxAttempts    = 3 // one try plus the two retries the design allows
	initialBackoff = 250 * time.Millisecond
)

// Item is one message to score. Only the id and the text travel: the service
// has no business knowing who sent what.
type Item struct {
	MsgID   int64
	Content string
}

// Result is one message's score. Tokens is the tokenizer's output for that
// message, returned so the caller can aggregate word and expression
// frequencies on the side that actually knows is_self.
type Result struct {
	MsgID     int64
	Polarity  float64
	Emotion   string
	Intensity float64
	Tokens    []string
}

// Health is the service's readiness report. Status distinguishes a service
// still loading its lexicon from one that can serve; ModelLoaded is always
// false for the M1 dictionary baseline.
type Health struct {
	Status      string
	ModelLoaded bool
	// ModelVersion is the lexicon the service will actually score with. The
	// caller records it as the provenance of every score, so it is taken from
	// the service rather than assumed. It is empty while the lexicon loads.
	ModelVersion string
}

// Client is an HTTP client for the NLP service.
type Client struct {
	baseURL string
	http    *http.Client
	// sleep is a seam so tests can exercise the retry schedule without
	// spending the real backoff.
	sleep func(context.Context, time.Duration) error
}

// NewClient builds a client for baseURL.
func NewClient(baseURL string) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("nlp: no service URL given")
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return nil, fmt.Errorf("nlp: service URL %q must start with http:// or https://", baseURL)
	}
	return &Client{
		baseURL: trimmed,
		http: &http.Client{
			Timeout: hardTimeout,
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   connectTimeout,
				ResponseHeaderTimeout: hardTimeout,
			},
		},
		sleep: sleepCtx,
	}, nil
}

// sleepCtx waits for d, or returns early if the caller gives up first. A retry
// that cannot be interrupted would keep a cancelled job alive for the whole
// backoff.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Score sends one batch. The caller owns the per-block deadline; this method
// must not be given more than MaxBatchSize items.
func (c *Client) Score(ctx context.Context, modelVersion string, items []Item) ([]Result, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > MaxBatchSize {
		return nil, fmt.Errorf("nlp: batch of %d exceeds the contract maximum of %d", len(items), MaxBatchSize)
	}
	payload := scoreRequest{ModelVersion: modelVersion, Items: make([]scoreItem, 0, len(items))}
	for _, item := range items {
		payload.Items = append(payload.Items, scoreItem{MsgID: item.MsgID, Content: item.Content})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("nlp: encode batch: %w", err)
	}

	var response scoreResponse
	if err := c.call(ctx, http.MethodPost, "/score", body, &response); err != nil {
		return nil, err
	}
	return checkAlignment(items, response, modelVersion)
}

// Health asks whether the service can serve requests.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var payload struct {
		Status       string `json:"status"`
		ModelLoaded  bool   `json:"model_loaded"`
		ModelVersion string `json:"model_version"`
	}
	if err := c.call(ctx, http.MethodGet, "/health", nil, &payload); err != nil {
		return Health{}, err
	}
	switch payload.Status {
	case "ready", "loading":
	default:
		return Health{}, fmt.Errorf("nlp: service reported an unrecognized status %q", payload.Status)
	}
	return Health{Status: payload.Status, ModelLoaded: payload.ModelLoaded, ModelVersion: payload.ModelVersion}, nil
}

// call performs the request, retrying only what is safe to retry.
//
// Retries happen here, inside one block, and nowhere else. A retry at the task
// level would restart a job that may already have run for an hour.
func (c *Client) call(ctx context.Context, method, path string, body []byte, out any) error {
	backoff := initialBackoff
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := c.attempt(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		var retryable retryableError
		if !errors.As(err, &retryable) {
			return err
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		// Full jitter: the wait is drawn uniformly from [0, backoff). Two
		// blocks failing together would otherwise retry in lockstep.
		wait := time.Duration(rand.Int63n(int64(backoff) + 1))
		if err := c.sleep(ctx, wait); err != nil {
			return fmt.Errorf("nlp: %s %s: giving up while waiting to retry: %w", method, path, err)
		}
		backoff *= 2
	}
	return fmt.Errorf("nlp: %s %s failed after %d attempts: %w", method, path, maxAttempts, lastErr)
}

// attempt is one try.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("nlp: build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		// A caller that has already given up must not be retried: the block
		// is over, and the next attempt would just burn the remaining budget.
		if ctx.Err() != nil {
			return fmt.Errorf("nlp: %s %s: %w", method, path, ctx.Err())
		}
		return retryableError{fmt.Errorf("nlp: %s %s: %w", method, path, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()

	if response.StatusCode >= 500 {
		return retryableError{fmt.Errorf("nlp: %s %s returned %d", method, path, response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("nlp: %s %s returned %d%s", method, path, response.StatusCode, errorCode(response.Body))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(out); err != nil {
		return fmt.Errorf("nlp: decode %s %s response: %w", method, path, err)
	}
	return nil
}

// retryableError marks the failures worth another attempt: connection
// failures, timeouts and 5xx.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// errorCode surfaces the service's machine-readable error code. The message
// field is deliberately not read: it flows from a service that has seen the
// message text, and an error string is the easiest place for that text to
// escape into a log.
func errorCode(body io.Reader) string {
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&envelope); err != nil || envelope.Code == "" {
		return ""
	}
	return " (code " + envelope.Code + ")"
}

// checkAlignment verifies the response matches the request one for one.
//
// A short, long, or reordered response would silently attach one message's
// score to another; the database would then hold a plausible-looking but wrong
// sentiment curve, which nothing downstream could detect.
func checkAlignment(items []Item, response scoreResponse, modelVersion string) ([]Result, error) {
	// Every score is stored under the version that was asked for, and the
	// report prints it as the provenance of the numbers. If the service
	// answered with a different lexicon, the batch would be recorded under a
	// version that did not produce it — so a mismatch fails the block instead
	// of being written down. An empty echo is tolerated: the version is
	// optional in the response contract.
	if response.ModelVersion != "" && response.ModelVersion != modelVersion {
		return nil, fmt.Errorf("nlp: asked for model %q but the service scored with %q; refusing to record scores under a version that did not produce them", modelVersion, response.ModelVersion)
	}
	if len(response.Results) != len(items) {
		return nil, fmt.Errorf("nlp: sent %d messages but the service returned %d scores", len(items), len(response.Results))
	}
	results := make([]Result, 0, len(items))
	for i, got := range response.Results {
		if got.MsgID != items[i].MsgID {
			return nil, fmt.Errorf("nlp: score %d is for message %d but %d was sent: the results are out of order", i, got.MsgID, items[i].MsgID)
		}
		results = append(results, Result{
			MsgID:     got.MsgID,
			Polarity:  got.Polarity,
			Emotion:   got.Emotion,
			Intensity: got.Intensity,
			Tokens:    got.Tokens,
		})
	}
	return results, nil
}

type scoreRequest struct {
	ModelVersion string      `json:"model_version"`
	Items        []scoreItem `json:"items"`
}

type scoreItem struct {
	MsgID   int64  `json:"msg_id"`
	Content string `json:"content"`
}

type scoreResponse struct {
	ModelVersion string        `json:"model_version"`
	Results      []scoreResult `json:"results"`
}

type scoreResult struct {
	MsgID     int64    `json:"msg_id"`
	Polarity  float64  `json:"polarity"`
	Emotion   string   `json:"emotion"`
	Intensity float64  `json:"intensity"`
	Tokens    []string `json:"tokens"`
}
