// Package narrative writes a paragraph about a conversation from aggregates
// alone.
//
// It is a privacy boundary expressed as a type: the narrator is handed counts,
// shares and word tallies, never a message. There is deliberately no field on
// Input for a message body, a sender or a conversation id, so a caller cannot
// pass one in even by accident. See the global constraint in the M4 plan and
// ADR-0004 (cloud LLM deny-by-default): the only endpoint this package will
// talk to is a loopback OpenAI-compatible one.
package narrative

import "context"

// Provider writes a paragraph about the aggregates it is given.
//
// The interface exists so a test can inject a deterministic fake instead of
// starting an LLM. The only production implementation is the loopback Ollama
// provider.
type Provider interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// promptVersion is part of the cache key. Change one character of the template
// in prompt.go and you MUST bump this, otherwise an old cache entry would be
// served as if it had been written from the new prompt.
const promptVersion = "1"
