package report

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"lover-instance/backend-go/internal/store"
)

// TopWords is how many entries each word cloud carries.
const TopWords = 200

// emojiToken matches the expression tokens the tokenizer preserves verbatim,
// e.g. "[捂脸]". The same shape is what decides whether a counted token is a
// word or an expression, so the two are separated here rather than in a second
// storage column.
var emojiToken = regexp.MustCompile(`^\[.+\]$`)

// Collect assembles the report view for one conversation.
//
// The behavioral side comes from Store.BehaviorMetrics, which derives from the
// message stream, and the sentiment side from the tables the scoring pass
// wrote. That split is deliberate: the late-night share, the reply percentiles
// and the type histogram have no column in the design's schema, so reading
// some figures from daily_stats and computing others would create two
// derivations of the same day that could quietly disagree.
func Collect(ctx context.Context, s *store.Store, talker, behaviorModelVersion, sentimentModelVersion string) (View, error) {
	behavior, err := s.BehaviorMetrics(ctx, talker)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	coverage, err := s.SentimentCoverage(ctx, talker, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	excluded, err := s.ExcludedTypeCounts(ctx, talker)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	distribution, err := s.EmotionDistribution(ctx, talker, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	// The polarity rollup is read for its two judged buckets only. Its third
	// bucket counts a different thing — polarity of zero rather than an unmatched
	// emotion word — and emotionView takes that count from the distribution
	// instead, so this one is deliberately discarded rather than passed along.
	positive, negative, _, err := s.SentimentTotals(ctx, talker, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	selfTokens, err := s.WordFrequencies(ctx, talker, store.AllPeriod, true)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	partnerTokens, err := s.WordFrequencies(ctx, talker, store.AllPeriod, false)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}

	view := View{
		Meta: MetaView{
			TalkerHash:            TalkerHash(talker),
			RangeStartMs:          behavior.RangeStartMs,
			RangeEndMs:            behavior.RangeEndMs,
			BehaviorModelVersion:  behaviorModelVersion,
			SentimentModelVersion: sentimentModelVersion,
		},
		Summary:    summarize(behavior),
		Heatmap:    heatmapOf(behavior),
		Words:      WordsView{Self: topWords(selfTokens, false), Partner: topWords(partnerTokens, false)},
		Emoji:      EmojiView{Self: topWords(selfTokens, true), Partner: topWords(partnerTokens, true)},
		Emotion:    emotionView(distribution, positive, negative),
		Coverage:   CoverageView{Scored: coverage.Scored, Total: coverage.Total, Partial: coverage.Scored < coverage.Total, ExcludedTypes: excluded},
		Disclaimer: DisclaimerText,
	}
	return view, nil
}

func summarize(behavior store.BehaviorMetrics) SummaryView {
	var firstIsSelf, lastIsSelf int
	for _, day := range behavior.Daily {
		if day.FirstIsSelf {
			firstIsSelf++
		}
		if day.LastIsSelf {
			lastIsSelf++
		}
	}
	var selfMsgs, partnerMsgs, selfChars, partnerChars int
	for _, day := range behavior.Daily {
		selfMsgs += day.SelfMsgs
		partnerMsgs += day.PartnerMsgs
		selfChars += day.SelfChars
		partnerChars += day.PartnerChars
	}
	return SummaryView{
		SelfMsgs:              selfMsgs,
		PartnerMsgs:           partnerMsgs,
		SelfChars:             selfChars,
		PartnerChars:          partnerChars,
		AvgSelfLen:            behavior.AvgSelfLen,
		AvgPartnerLen:         behavior.AvgPartnerLen,
		LateNightRatio:        behavior.LateNightRatio,
		InitiatorRatio:        behavior.InitiatorRatio,
		TurnBalanceCharRatio:  behavior.TurnBalanceCharRatio,
		TurnBalanceMsgRatio:   behavior.TurnBalanceMsgRatio,
		DailyFirstIsSelfCount: firstIsSelf,
		DailyLastIsSelfCount:  lastIsSelf,
		DayCount:              len(behavior.Daily),
		ConversationCount:     len(behavior.Conversations),
		Reply: ReplyView{
			MedianMs:    behavior.Reply.MedianMs,
			P90Ms:       behavior.Reply.P90Ms,
			FastRatio:   behavior.Reply.FastRatio,
			SampleCount: behavior.Reply.SampleCount,
		},
		Types: behavior.Types,
	}
}

func heatmapOf(behavior store.BehaviorMetrics) []HeatCellView {
	cells := make([]HeatCellView, 0, len(behavior.Heatmap))
	for _, cell := range behavior.Heatmap {
		cells = append(cells, HeatCellView{
			Weekday: cell.Weekday, Hour: cell.Hour,
			SelfCount: cell.SelfCount, PartnerCount: cell.PartnerCount,
		})
	}
	return cells
}

// topWords ranks one side's tokens.
//
// Expressions and words are separated by shape rather than by a second column,
// and both are ranked with the same deterministic tie-break so a regenerated
// report is byte-identical. Relying on map iteration order would make the
// output differ between runs for no reason.
func topWords(tokens map[string]int, wantEmoji bool) []WordCount {
	ranked := make([]WordCount, 0, len(tokens))
	for token, count := range tokens {
		if emojiToken.MatchString(token) != wantEmoji {
			continue
		}
		if strings.TrimSpace(token) == "" {
			continue
		}
		ranked = append(ranked, WordCount{Word: token, Count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Word < ranked[j].Word
	})
	if len(ranked) > TopWords {
		ranked = ranked[:TopWords]
	}
	return ranked
}

// emotionView builds the seven-emotion breakdown, its radar and the polarity
// split.
//
// The radar excludes the neutral bucket: neutral is the absence of a matched
// emotion word, and plotting it as an eighth spoke would read as an emotional
// state rather than as a failure to classify.
//
// Every count here, neutral included, comes from the emotion distribution.
// There is a second way to count a neutral message — polarity of exactly zero —
// and the two do not agree: the acceptance run over a real 32,624-message
// export found three messages carrying a matched emotion word yet a polarity of
// exactly zero, so the polarity bucket held 17,258 where the emotion bucket
// held 17,255. Taking the neutral bucket from one query and the other seven
// from another made the shares divide by a total that matched no count
// anywhere, while the coverage figure beside it said something else. "No
// emotion word matched" is what this label means, and that is the distribution.
func emotionView(distribution map[string]int, positive, negative int) EmotionView {
	counts := make(map[string]int, len(Emotions)+1)
	scoredTotal := 0
	for _, emotion := range Emotions {
		counts[emotion] = distribution[emotion]
		scoredTotal += distribution[emotion]
	}
	neutral := distribution[NeutralLabel]
	counts[NeutralLabel] = neutral
	scoredTotal += neutral

	shares := make(map[string]float64, len(counts))
	radar := make([]float64, 0, len(Emotions))
	if scoredTotal > 0 {
		for label, count := range counts {
			shares[label] = float64(count) / float64(scoredTotal)
		}
		for _, emotion := range Emotions {
			radar = append(radar, shares[emotion])
		}
	} else {
		radar = make([]float64, len(Emotions))
	}

	view := EmotionView{
		Counts:       counts,
		Shares:       shares,
		Radar:        radar,
		NeutralCount: neutral,
		ScoredTotal:  scoredTotal,
	}
	if polarity := positive + negative; polarity > 0 {
		view.PositiveRatio = float64(positive) / float64(polarity)
	}
	return view
}
