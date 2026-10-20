package narrative

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewOllama_LoopbackOnly pins ADR-0004: only loopback endpoints construct,
// everything else is refused at build time with an error that says "loopback".
func TestNewOllama_LoopbackOnly(t *testing.T) {
	allowed := []string{
		"http://127.0.0.1:11434",
		"http://localhost:11434",
		"http://[::1]:11434",
	}
	for _, u := range allowed {
		if _, err := NewOllama(u, "llama3", time.Second); err != nil {
			t.Errorf("NewOllama(%q) error = %v, want success", u, err)
		}
	}

	refused := []string{
		"https://api.openai.com",
		"http://10.0.0.5:11434",
		"http://192.168.1.2",
		"http://example.com",
	}
	for _, u := range refused {
		_, err := NewOllama(u, "llama3", time.Second)
		if err == nil {
			t.Errorf("NewOllama(%q) succeeded, want a loopback refusal", u)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("NewOllama(%q) error = %v, want it to mention loopback", u, err)
		}
	}
}

func TestNewOllama_RejectsZeroTimeout(t *testing.T) {
	if _, err := NewOllama("http://127.0.0.1:11434", "llama3", 0); err == nil {
		t.Fatal("NewOllama with a zero timeout succeeded; a zero-value client never times out")
	}
	if _, err := NewOllama("http://127.0.0.1:11434", "llama3", -time.Second); err == nil {
		t.Fatal("NewOllama with a negative timeout succeeded")
	}
}

func TestNewOllama_RejectsEmptyModel(t *testing.T) {
	if _, err := NewOllama("http://127.0.0.1:11434", "", time.Second); err == nil {
		t.Fatal("NewOllama with an empty model succeeded")
	}
}

// TestOllama_TimeoutApplies proves the caller's timeout actually cuts the
// request short.
func TestOllama_TimeoutApplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: "late"}}}})
	}))
	defer srv.Close()

	provider, err := NewOllama(srv.URL, "llama3", 30*time.Millisecond)
	if err != nil {
		t.Fatalf("NewOllama() error: %v", err)
	}
	start := time.Now()
	if _, err := provider.Generate(context.Background(), "hi"); err == nil {
		t.Fatal("Generate() against a slow server succeeded, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("Generate took %v; the 30ms timeout did not cut it short", elapsed)
	}
}

// TestOllama_RequestShape pins the OpenAI-compatible request: method, path,
// stream:false, model and the prompt as the single user message.
func TestOllama_RequestShape(t *testing.T) {
	const prompt = "给我讲个故事"
	var (
		gotMethod string
		gotPath   string
		gotRaw    []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotRaw, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: "ok"}}}})
	}))
	defer srv.Close()

	provider, err := NewOllama(srv.URL, "llama3", time.Second)
	if err != nil {
		t.Fatalf("NewOllama() error: %v", err)
	}
	out, err := provider.Generate(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if out != "ok" {
		t.Errorf("Generate() = %q, want %q", out, "ok")
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if !strings.Contains(string(gotRaw), `"stream":false`) {
		t.Errorf("request body missing \"stream\":false: %s", gotRaw)
	}
	var sent chatRequest
	if err := json.Unmarshal(gotRaw, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Model != "llama3" {
		t.Errorf("model = %q, want llama3", sent.Model)
	}
	// A 150-250 character answer does not need thousands of tokens, and a model
	// that thinks out loud will spend the whole timeout doing it: measured on
	// qwen3.5:4b, one request emitted 4,311 tokens for a one-paragraph answer.
	if sent.MaxTokens != maxResponseTokens {
		t.Errorf("max_tokens = %d, want %d: the request has to cap what the model may write", sent.MaxTokens, maxResponseTokens)
	}
	if len(sent.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(sent.Messages))
	}
	if sent.Messages[0].Role != "user" {
		t.Errorf("message role = %q, want user", sent.Messages[0].Role)
	}
	if sent.Messages[0].Content != prompt {
		t.Errorf("message content = %q, want %q", sent.Messages[0].Content, prompt)
	}
}

// TestOllama_Non2xxHidesBody proves the error carries the status code but not
// the response body, so model output cannot escape into a log.
func TestOllama_Non2xxHidesBody(t *testing.T) {
	const marker = "SECRET-RESPONSE-BODY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(marker))
	}))
	defer srv.Close()

	provider, err := NewOllama(srv.URL, "llama3", time.Second)
	if err != nil {
		t.Fatalf("NewOllama() error: %v", err)
	}
	_, err = provider.Generate(context.Background(), "p")
	if err == nil {
		t.Fatal("Generate() against a 500 succeeded, want an error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error leaks the response body: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should carry the status code: %v", err)
	}
}

// TestOllama_TruncatesLongOutput proves a rambling model is capped at
// maxOutputBytes.
func TestOllama_TruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("a", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: long}}}})
	}))
	defer srv.Close()

	provider, err := NewOllama(srv.URL, "llama3", time.Second)
	if err != nil {
		t.Fatalf("NewOllama() error: %v", err)
	}
	out, err := provider.Generate(context.Background(), "p")
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(out) != maxOutputBytes {
		t.Errorf("output length = %d, want it truncated to %d", len(out), maxOutputBytes)
	}
}
