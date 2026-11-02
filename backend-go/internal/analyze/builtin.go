package analyze

import (
	"context"
	"fmt"

	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/store"
)

// BehaviorAnalyzer aggregates daily stats, response patterns, and heatmaps.
type BehaviorAnalyzer struct{}

func (b *BehaviorAnalyzer) ID() string             { return "behavior" }
func (b *BehaviorAnalyzer) Name() string           { return "基础行为与热力统计" }
func (b *BehaviorAnalyzer) Dependencies() []string { return nil }
func (b *BehaviorAnalyzer) Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error {
	if opts.Talker == "" {
		return fmt.Errorf("analyze: talker is required for behavior analysis")
	}
	return s.RebuildBehaviorMetrics(ctx, opts.Talker)
}

// SentimentAnalyzer scores pending messages and extracts word frequencies.
type SentimentAnalyzer struct {
	Client *nlp.Client
}

func (sa *SentimentAnalyzer) ID() string             { return "sentiment" }
func (sa *SentimentAnalyzer) Name() string           { return "情感打分与词频聚合" }
func (sa *SentimentAnalyzer) Dependencies() []string { return []string{"behavior"} }
func (sa *SentimentAnalyzer) Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error {
	if sa.Client == nil {
		return fmt.Errorf("analyze: sentiment analyzer requires an NLP client")
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	_, err := RunSentimentProgress(ctx, s, sa.Client, opts.Talker, opts.SentimentVersion, batchSize, onProgress)
	return err
}

// DefaultRegistry creates an Analyzer registry populated with the standard pipeline stages.
func DefaultRegistry(c *nlp.Client) *Registry {
	r := NewRegistry()
	_ = r.Register(&BehaviorAnalyzer{})
	_ = r.Register(&HighlightsAnalyzer{})
	if c != nil {
		_ = r.Register(&SentimentAnalyzer{Client: c})
	}
	return r
}
