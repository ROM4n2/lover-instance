// Package analyze drives the sentiment pass: it walks the unscored messages of
// one conversation in blocks, hands each block to the NLP service, and records
// the outcome.
package analyze

import (
	"context"
	"fmt"
	"time"

	"lover-instance/backend-go/internal/nlp"
	"lover-instance/backend-go/internal/store"
)

// blockTimeout bounds one HTTP block. A service that has gone quiet must not
// hold the whole run: the block fails, the checkpoint keeps everything written
// before it, and the run can be resumed.
const blockTimeout = 5 * time.Minute

// Progress is how far a scoring run has got. It is an absolute count rather
// than a percentage because the total grows as more messages are imported.
type Progress struct {
	Scored int
	Total  int
}

// RunSentiment scores every unscored analyzable message of one conversation.
//
// The loop re-reads its work list each time rather than slicing a snapshot
// taken up front. That is what makes the run resumable for free: a block that
// has been written is no longer pending, so an interrupted run picks up where
// it stopped and a completed one does no work at all. It is also what keeps
// the writer connection free between blocks — the design forbids holding it
// while an HTTP request is in flight.
func RunSentiment(ctx context.Context, s *store.Store, c *nlp.Client, talker, modelVersion string, batchSize int) (Progress, error) {
	return RunSentimentProgress(ctx, s, c, talker, modelVersion, batchSize, nil)
}

// RunSentimentProgress is RunSentiment with a hook that runs after each block
// has been committed.
//
// onProgress receives the absolute counts, and it runs only after the block's
// transaction returned successfully. Calling it first would advertise a
// checkpoint that does not exist yet, and a job row promising progress the
// database does not hold is exactly the half-finished state ADR-0002 exists to
// prevent. It is called once per committed block, so a job watching it sees
// steps rather than a figure that only appears at the end.
//
// A nil onProgress is allowed and is what RunSentiment passes.
func RunSentimentProgress(ctx context.Context, s *store.Store, c *nlp.Client, talker, modelVersion string, batchSize int, onProgress func(scored, total int)) (Progress, error) {
	if batchSize <= 0 || batchSize > nlp.MaxBatchSize {
		return Progress{}, fmt.Errorf("analyze: batch size %d is outside 1..%d", batchSize, nlp.MaxBatchSize)
	}
	total, err := s.CountAnalyzable(ctx, talker)
	if err != nil {
		return Progress{}, fmt.Errorf("analyze: %w", err)
	}
	progress := Progress{Total: total}

	for {
		if err := ctx.Err(); err != nil {
			return progress, fmt.Errorf("analyze: stopped after scoring %d of %d: %w", progress.Scored, progress.Total, err)
		}
		pending, err := s.PendingMessages(ctx, talker, modelVersion, batchSize)
		if err != nil {
			return progress, fmt.Errorf("analyze: %w", err)
		}
		if len(pending) == 0 {
			return progress, nil
		}

		results, err := scoreBlock(ctx, c, modelVersion, pending)
		if err != nil {
			return progress, fmt.Errorf("analyze: after scoring %d of %d: %w", progress.Scored, progress.Total, err)
		}

		selfTokens, partnerTokens := splitTokens(pending, results)
		rows := make([]store.SentimentRow, 0, len(results))
		for _, result := range results {
			rows = append(rows, store.SentimentRow{
				MsgID:     result.MsgID,
				Polarity:  result.Polarity,
				Emotion:   result.Emotion,
				Intensity: result.Intensity,
			})
		}
		if err := s.SaveScoredBlock(ctx, talker, modelVersion, store.AllPeriod, rows, selfTokens, partnerTokens); err != nil {
			return progress, fmt.Errorf("analyze: %w", err)
		}
		progress.Scored += len(rows)
		if onProgress != nil {
			onProgress(progress.Scored, progress.Total)
		}
	}
}

// scoreBlock sends one block under its own deadline.
func scoreBlock(ctx context.Context, c *nlp.Client, modelVersion string, pending []store.PendingMsg) ([]nlp.Result, error) {
	items := make([]nlp.Item, 0, len(pending))
	for _, msg := range pending {
		items = append(items, nlp.Item{MsgID: msg.ID, Content: msg.Content})
	}
	blockCtx, cancel := context.WithTimeout(ctx, blockTimeout)
	defer cancel()

	results, err := c.Score(blockCtx, modelVersion, items)
	if err != nil {
		return nil, fmt.Errorf("score block of %d: %w", len(items), err)
	}
	return results, nil
}

// splitTokens buckets each message's tokens by who sent it.
//
// The service returns bare tokens and nothing about identity, which is the
// point: only this side knows is_self, so the self/partner split happens here
// rather than being pushed into a contract that would then have to carry
// sender information across a process boundary for no reason.
func splitTokens(pending []store.PendingMsg, results []nlp.Result) (self, partner map[string]int) {
	self = make(map[string]int)
	partner = make(map[string]int)
	for i, result := range results {
		if i >= len(pending) {
			break
		}
		target := partner
		if pending[i].IsSelf {
			target = self
		}
		for _, token := range result.Tokens {
			target[token]++
		}
	}
	return self, partner
}
