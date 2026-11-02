// Package analyze drives the analysis passes: behavior, sentiment, word frequency,
// highlights, and narrative. It provides a modular Analyzer interface and a DAG
// topological registry to coordinate them.
package analyze

import (
	"context"

	"lover-instance/backend-go/internal/store"
)

// Options holds parameters shared across analysis passes.
type Options struct {
	Talker           string
	SentimentVersion string
	BatchSize        int
	RescoreWords     bool
	NoNarrative      bool
	OllamaURL        string
	OllamaModel      string
}

// Analyzer is the common contract for all batch/offline analysis modules.
type Analyzer interface {
	// ID returns the unique machine identifier (e.g. "behavior", "word_freq", "sentiment", "highlights").
	ID() string
	// Name returns a human-readable title (e.g. "高光里程碑分析器").
	Name() string
	// Dependencies lists the IDs of Analyzers that must run before this one.
	Dependencies() []string
	// Run performs the analysis pass and commits its results to the Store.
	Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error
}
