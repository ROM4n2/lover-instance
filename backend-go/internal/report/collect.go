package report

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"lover-instance/backend-go/internal/narrative"
	"lover-instance/backend-go/internal/store"
)

// Narrator writes a paragraph about a conversation from the aggregates in a
// narrative.Input. It is the seam that lets the report stay free of any
// concrete LLM: a test injects a deterministic fake, the CLI injects the
// cached loopback provider, and Collect itself never knows which it has.
type Narrator interface {
	Narrate(ctx context.Context, in narrative.Input) (string, error)
}

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
func Collect(ctx context.Context, s *store.Store, talker, behaviorModelVersion, sentimentModelVersion string, narrator Narrator) (View, error) {
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
	versions, err := s.SentimentVersions(ctx, talker)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	emotion, emotionSource, err := emotionBreakdown(ctx, s, talker, sentimentModelVersion, versions)
	if err != nil {
		return View{}, err
	}
	comparison, err := comparisonOf(ctx, s, talker, sentimentModelVersion, versions)
	if err != nil {
		return View{}, err
	}
	trendPoints, err := s.Trend(ctx, talker, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	selfTokens, err := s.WordFrequencies(ctx, talker, store.AllPeriod, true, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	partnerTokens, err := s.WordFrequencies(ctx, talker, store.AllPeriod, false, sentimentModelVersion)
	if err != nil {
		return View{}, fmt.Errorf("report: %w", err)
	}
	monthly, err := monthlyView(ctx, s, talker, sentimentModelVersion)
	if err != nil {
		return View{}, err
	}

	highlights, err := CollectHighlights(ctx, s, talker)
	if err != nil {
		return View{}, err
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
		Emotion:    emotion,
		Coverage:   CoverageView{Scored: coverage.Scored, Total: coverage.Total, Partial: coverage.Scored < coverage.Total, ExcludedTypes: excluded},
		Comparison: comparison,
		Trend:      trendView(trendPoints),
		// The emotion section is not always the report's own version's work,
		// and the template states whose work it is.
		EmotionSource: emotionSource,
		Disclaimer:    DisclaimerText,
		Monthly:       monthly,
		Highlights:    highlights,
	}

	// The narration runs last and its failure is never returned. The report is
	// the product; a local model being absent, slow or misconfigured writes one
	// sentence into the section and leaves the exit code at zero (M4 plan,
	// "降级不致命").
	if narrator == nil {
		view.NarrativeNote = "本次未生成叙事（未配置本机模型）"
	} else {
		text, err := narrator.Narrate(ctx, narrativeInput(view))
		switch {
		case err != nil:
			view.NarrativeNote = "未生成叙事：" + narrativeFailureReason(err)
		case strings.TrimSpace(text) == "":
			view.NarrativeNote = "未生成叙事：本机模型返回了空内容"
		default:
			view.Narrative = text
		}
	}
	return view, nil
}

// monthlyView reads the other side's per-month top words.
//
// The partner side is chosen (and the template names it) so one chart answers
// "what did they keep saying, month by month" without implying a comparison the
// data cannot support. A version with no monthly rows yields an empty slice and
// no error: that is the pre-rescan state, and the template explains it instead
// of drawing an empty chart that would read as a quiet month.
func monthlyView(ctx context.Context, s *store.Store, talker, sentimentModelVersion string) ([]MonthView, error) {
	if sentimentModelVersion == "" {
		return nil, nil
	}
	months, err := s.MonthlyWordFrequencies(ctx, talker, sentimentModelVersion, false, 12)
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	view := make([]MonthView, 0, len(months))
	for _, month := range months {
		words := make([]WordCount, 0, len(month.Words))
		for _, entry := range month.Words {
			words = append(words, WordCount{Word: entry.Word, Count: entry.Count})
		}
		view = append(view, MonthView{Month: month.Month, Words: words})
	}
	return view, nil
}

// narrativeInput copies the aggregates into the one struct the model is allowed
// to see. Every field here is a count, a share, a date or a word token; there
// is deliberately no place for a message, a sender or the conversation key, so
// the privacy boundary is enforced by the type rather than by care.
//
// Rejected on purpose: Meta.TalkerHash, the raw talker, and every message body.
// The word lists carry only .Word, never a source sentence, and the date range
// is formatted to day precision so no timestamp rides along.
// The word lists are deliberately NOT filled, and this is a privacy decision
// rather than a stylistic one.
//
// A real word cloud carries people's names: on this conversation's own export
// the third most frequent token was a personal name, 1,016 times, and the M1
// notes already record that no NER stands in the way. Fed to the narrator, those
// words come back out in a paragraph — and this file is meant to be shared, so a
// name the sender never typed in the prompt would travel with it. The model also
// turns word lists into specific-sounding claims it cannot support.
//
// So the narrator sees numbers and their shapes, not vocabulary. A duller
// paragraph is the price; the report's own charts still show the words.
func narrativeInput(view View) narrative.Input {
	input := narrative.Input{
		Days:            view.Summary.DayCount,
		Messages:        view.Summary.SelfMsgs + view.Summary.PartnerMsgs,
		SelfMessages:    view.Summary.SelfMsgs,
		PartnerMessages: view.Summary.PartnerMsgs,
		RangeStart:      dayString(view.Meta.RangeStartMs),
		RangeEnd:        dayString(view.Meta.RangeEndMs),
		EmotionShares:   view.Emotion.Shares,
		TrendPoints:     trendPointsForNarrative(view.Trend.Points),
	}
	return input
}

// wordList takes the words off a ranking, capped, so the prompt carries tokens
// and never the entries' provenance.
func wordList(entries []WordCount, limit int) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Word == "" {
			continue
		}
		out = append(out, entry.Word)
		if len(out) == limit {
			break
		}
	}
	return out
}

func trendPointsForNarrative(points []TrendPointView) []narrative.TrendPoint {
	out := make([]narrative.TrendPoint, 0, len(points))
	for _, point := range points {
		out = append(out, narrative.TrendPoint{
			Day: point.Day, Mean: point.Mean, StdDev: point.StdDev, Count: point.Count,
		})
	}
	return out
}

// dayString renders a millisecond timestamp as a calendar date in the report's
// zone. A zero timestamp (no messages) becomes an empty string rather than a
// 1970 date the model would then describe.
func dayString(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(cst).Format("2006-01-02")
}

// narrativeFailureReason maps a narration error to a short, fixed phrase.
//
// The error itself is never printed. It can wrap a filesystem path or a request
// URL, and the report is a file meant to be shared; only the shape of the
// failure is safe to carry. Every branch returns a constant.
func narrativeFailureReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"),
		strings.Contains(message, "connectex"),
		strings.Contains(message, "dial tcp"),
		strings.Contains(message, "no such host"),
		strings.Contains(message, "actively refused"):
		return "本机模型未运行"
	case strings.Contains(message, "timeout"),
		strings.Contains(message, "deadline exceeded"):
		return "本机模型超时"
	case strings.Contains(message, "status 404"):
		return "本机没有这个模型"
	case strings.Contains(message, "no model name"),
		strings.Contains(message, "loopback"),
		strings.Contains(message, "must use http"),
		strings.Contains(message, "timeout must be"),
		strings.Contains(message, "parse base url"):
		return "本机模型配置有误"
	default:
		return "无法从本机模型取得叙事"
	}
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

// emotionBreakdown builds the seven-emotion section and names the version it
// came from.
//
// Only the lexicon produces DUTIR labels. A model version records an empty
// emotion for every message (Task 3 of the M2 plan), which the store folds into
// the neutral bucket, so a report pinned to the model would draw seven zero
// spokes — which reads as "this conversation had no emotion" rather than "this
// version does not measure emotions". Showing the version that did produce
// labels and naming it is the honest alternative; fabricating seven categories
// out of the polarity sign is the dishonest one, and ADR-0004 forbids it.
//
// The polarity split printed beside the radar is read from the same version as
// the labels, so that whole section describes one run rather than two.
func emotionBreakdown(ctx context.Context, s *store.Store, talker, current string, versions []string) (EmotionView, string, error) {
	distribution, err := s.EmotionDistribution(ctx, talker, current)
	if err != nil {
		return EmotionView{}, "", fmt.Errorf("report: %w", err)
	}
	source := current
	if !hasEmotionLabels(distribution) {
		source, distribution = "", nil
		for _, version := range versions {
			if version == current {
				continue
			}
			candidate, err := s.EmotionDistribution(ctx, talker, version)
			if err != nil {
				return EmotionView{}, "", fmt.Errorf("report: %w", err)
			}
			// The first candidate in the store's sorted order wins, so the
			// fallback is the same on every render.
			if hasEmotionLabels(candidate) {
				source, distribution = version, candidate
				break
			}
		}
	}

	positive, negative := 0, 0
	if source != "" {
		// The polarity rollup is read for its two judged buckets only. Its
		// third bucket counts a different thing — polarity of zero rather than
		// an unmatched emotion word — and emotionView takes that count from the
		// distribution instead, so this one is deliberately discarded rather
		// than passed along.
		positive, negative, _, err = s.SentimentTotals(ctx, talker, source)
		if err != nil {
			return EmotionView{}, "", fmt.Errorf("report: %w", err)
		}
	}
	return emotionView(distribution, positive, negative), source, nil
}

// hasEmotionLabels reports whether a distribution holds at least one DUTIR
// label. The neutral bucket is the failure to match an emotion word, so a
// distribution holding nothing but neutral carries no emotion information.
func hasEmotionLabels(distribution map[string]int) bool {
	for _, label := range Emotions {
		if distribution[label] > 0 {
			return true
		}
	}
	return false
}

// comparisonOf builds the side-by-side section.
//
// It exists only when two versions scored the conversation. With one version
// there is nothing to compare against, and a section comparing it with itself
// would show a 0% disagreement rate — a claim, not an absence.
func comparisonOf(ctx context.Context, s *store.Store, talker, current string, versions []string) (ComparisonView, error) {
	if len(versions) < 2 {
		return ComparisonView{}, nil
	}
	other, ok, err := otherVersion(ctx, s, talker, current, versions)
	if err != nil {
		return ComparisonView{}, err
	}
	if !ok {
		return ComparisonView{}, nil
	}
	currentSplit, err := splitOfVersion(ctx, s, talker, current)
	if err != nil {
		return ComparisonView{}, err
	}
	otherSplit, err := splitOfVersion(ctx, s, talker, other)
	if err != nil {
		return ComparisonView{}, err
	}
	disagreement, err := s.Disagreement(ctx, talker, current, other)
	if err != nil {
		return ComparisonView{}, fmt.Errorf("report: %w", err)
	}

	view := ComparisonView{
		Present:    true,
		Current:    currentSplit,
		Other:      otherSplit,
		BothScored: disagreement.Both,
		Differing:  disagreement.Differing,
	}
	// BothScored can be zero when the two versions scored disjoint messages,
	// and 0/0 is not a rate: the section then shows the counts and no
	// percentage rather than a confident 0%.
	if disagreement.Both > 0 {
		view.DisagreementRate = float64(disagreement.Differing) / float64(disagreement.Both)
	}
	return view, nil
}

// otherVersion picks the version the comparison column shows beside the current
// one.
//
// Candidates are every version that scored this conversation except the
// report's own. Preference goes to a candidate whose name does not contain the
// current version's name and that produced DUTIR labels: a version whose name
// embeds another's is a derivative of it — a re-run at a different threshold,
// say — not an independent second opinion, and comparing the two would report
// an agreement that is partly definitional. If no candidate meets both
// conditions, the first one in the store's sorted order is used, so the choice
// does not change between renders.
func otherVersion(ctx context.Context, s *store.Store, talker, current string, versions []string) (string, bool, error) {
	fallback := ""
	haveFallback := false
	for _, version := range versions {
		if version == current {
			continue
		}
		if !haveFallback {
			fallback, haveFallback = version, true
		}
		if strings.Contains(version, current) {
			continue
		}
		distribution, err := s.EmotionDistribution(ctx, talker, version)
		if err != nil {
			return "", false, fmt.Errorf("report: %w", err)
		}
		if hasEmotionLabels(distribution) {
			return version, true, nil
		}
	}
	return fallback, haveFallback, nil
}

// splitOfVersion reads one version's three polarity buckets.
func splitOfVersion(ctx context.Context, s *store.Store, talker, version string) (VersionSplitView, error) {
	positive, negative, neutral, err := s.SentimentTotals(ctx, talker, version)
	if err != nil {
		return VersionSplitView{}, fmt.Errorf("report: %w", err)
	}
	view := VersionSplitView{
		Version:  version,
		Positive: positive,
		Negative: negative,
		Neutral:  neutral,
	}
	if polarity := positive + negative; polarity > 0 {
		view.PositiveRatio = float64(positive) / float64(polarity)
	}
	return view, nil
}

// trendView maps the store's points into the payload the chart reads.
//
// No point is dropped. A point that cannot be drawn is still a point — it is
// the evidence that samples were too thin — so the view keeps it and lets the
// chart mark it separately; dropping it here would make "not enough samples"
// indistinguishable from "no messages", which the M2 plan calls out.
//
// MeanLow and MeanHigh bound the drawable points only, because they describe
// the curve that was drawn. They stay zero when nothing is drawable.
func trendView(points []store.TrendPoint) TrendView {
	view := TrendView{Points: make([]TrendPointView, 0, len(points))}
	reach := 0.0
	for _, point := range points {
		drawable := point.Drawable()
		view.Points = append(view.Points, TrendPointView{
			Day:      point.Day,
			Mean:     point.Mean,
			StdDev:   point.StdDev,
			Count:    point.Count,
			Drawable: drawable,
		})
		if !drawable {
			view.Thin++
			continue
		}
		if view.Drawable == 0 {
			view.MeanLow, view.MeanHigh = point.Mean, point.Mean
		} else {
			view.MeanLow = math.Min(view.MeanLow, point.Mean)
			view.MeanHigh = math.Max(view.MeanHigh, point.Mean)
		}
		// The band, not just the line, has to fit: a ±1 SD edge below -axis
		// would be cut off at the plot border and read as "no spread".
		reach = math.Max(reach, math.Abs(point.Mean)+point.StdDev)
		view.Drawable++
	}
	view.Axis = math.Ceil(math.Max(reach, minAxisSpan)*100) / 100
	return view
}

// CollectHighlights reads the stored highlights for talker and maps them into a HighlightsView.
func CollectHighlights(ctx context.Context, s *store.Store, talker string) (HighlightsView, error) {
	events, err := s.GetHighlights(ctx, talker)
	if err != nil {
		return HighlightsView{}, fmt.Errorf("report highlights: %w", err)
	}
	if len(events) == 0 {
		return HighlightsView{Present: false}, nil
	}

	view := HighlightsView{
		Present: true,
	}

	for _, e := range events {
		mv := MilestoneView{
			Kind:        e.Kind,
			Date:        e.EventDate,
			Title:       e.Title,
			Description: e.Description,
			MetricValue: e.MetricValue,
		}
		view.Events = append(view.Events, mv)

		switch e.Kind {
		case "origin":
			view.StartDate = e.EventDate
			view.JourneyDays = int(e.MetricValue)
		case "streak":
			view.MaxStreakDays = int(e.MetricValue)
			view.StreakPeriod = e.EventDate
		case "peak_day":
			mv.Metric = fmt.Sprintf("%d 条", int(e.MetricValue))
			view.PeakDays = append(view.PeakDays, mv)
		case "late_night":
			mv.Metric = fmt.Sprintf("凌晨 %d 条", int(e.MetricValue))
			view.LateNights = append(view.LateNights, mv)
		}
	}
	return view, nil
}
