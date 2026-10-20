package narrative

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseTokens bounds what the model may write. It is generous for a
// 150-250 character answer on purpose: a reasoning model spends tokens before
// it answers, and a cap that only covers the answer truncates the thinking and
// returns an empty content field. 1,200 is still eight times the answer.
const maxResponseTokens = 1200

// maxOutputBytes caps how much narration is kept from one model reply. A model
// that rambles should not be able to bloat the report; 4000 bytes is several
// times the requested length.
const maxOutputBytes = 4000

// ollamaProvider is the loopback-only, OpenAI-compatible client.
type ollamaProvider struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewOllama builds a provider for an OpenAI-compatible endpoint at baseURL.
//
// Only loopback hosts are accepted (ADR-0004, deny-by-default): 127.0.0.0/8,
// ::1 or localhost. There is no opt-out switch. The timeout must be positive —
// a zero-value http.Client never times out, so it is refused here rather than
// allowed to hang a report.
func NewOllama(baseURL, model string, timeout time.Duration) (Provider, error) {
	if model == "" {
		return nil, fmt.Errorf("narrative: no model name given")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("narrative: timeout must be positive, got %v: a zero timeout never expires", timeout)
	}
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("narrative: parse base URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("narrative: base URL %q must use http or https", baseURL)
	}
	if !isLoopback(parsed.Hostname()) {
		return nil, fmt.Errorf("narrative: base URL %q is not a loopback address; only 127.0.0.0/8, ::1 and localhost are allowed (cloud LLM is denied by default)", baseURL)
	}
	return &ollamaProvider{
		baseURL: trimmed,
		model:   model,
		http:    &http.Client{Timeout: timeout},
	}, nil
}

// isLoopback reports whether host names this machine. net.IP.IsLoopback covers
// all of 127.0.0.0/8 and ::1; localhost is the one name we accept, and we do not
// resolve arbitrary names to their addresses — a hostname could point anywhere.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Generate sends one prompt and returns the trimmed model reply, truncated to
// maxOutputBytes.
func (p *ollamaProvider) Generate(ctx context.Context, prompt string) (string, error) {
	payload := chatRequest{
		Model:    p.model,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
		Stream:   false,
		// The answer is 150-250 characters, so a few hundred tokens is the
		// ceiling, not the target. Without it a chatty model spends the whole
		// timeout on tokens nobody reads: measured on qwen3.5:4b, one request
		// produced 4,311 tokens for a one-paragraph answer and blew past two
		// minutes. A cap turns "hangs" into "long", and truncation is already
		// handled on the way out.
		MaxTokens: maxResponseTokens,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("narrative: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return "", fmt.Errorf("narrative: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := p.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("narrative: call ollama: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	// The body is not read into the error. It is model output derived from the
	// prompt, and an error string is the easiest place for that text to escape
	// into a log; only the status code is safe to carry.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("narrative: ollama returned status %d", response.StatusCode)
	}

	var parsed chatResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&parsed); err != nil {
		return "", fmt.Errorf("narrative: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("narrative: ollama returned no choices")
	}
	text := cleanOutput(parsed.Choices[0].Message.Content)
	if text == "" {
		// A reasoning model can spend the entire max_tokens budget thinking and
		// hand back an empty content field. That is a failed request, not a
		// short answer: cached as a success it would be served forever.
		return "", fmt.Errorf("narrative: the model returned no text (it may have spent the whole token budget before answering)")
	}
	return text, nil
}

// cleanOutput trims surrounding whitespace and caps the length at
// maxOutputBytes without splitting a UTF-8 rune.
func cleanOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxOutputBytes {
		s = s[:maxOutputBytes]
		s = strings.ToValidUTF8(s, "")
	}
	return s
}

type chatRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []chatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}
