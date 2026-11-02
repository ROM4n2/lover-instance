// Package report turns stored analysis into the aggregate-only view a
// template renders into the shareable HTML file.
//
// Nothing here carries a message. The report describes a conversation in
// counts, shares and buckets, and that is a privacy boundary rather than a
// formatting preference: the file is meant to be sent to someone, and a
// per-message payload would put the conversation itself in their hands.
package report

import (
	"crypto/sha256"
	"encoding/hex"

	"lover-instance/backend-go/internal/store"
)

// WordCount is one entry of a word cloud or an expression tally.
type WordCount struct {
	Word  string
	Count int
}

// WordsView is the two word clouds, already trimmed to the top entries.
type WordsView struct {
	Self    []WordCount
	Partner []WordCount
}

// EmojiView is the expression tally: the tokens the tokenizer kept wrapped in
// square brackets, counted per side.
type EmojiView struct {
	Self    []WordCount
	Partner []WordCount
}

// ReplyView is the reply-latency summary.
type ReplyView struct {
	MedianMs    int64
	P90Ms       int64
	FastRatio   float64
	SampleCount int
}

// EmotionView is the seven-emotion breakdown and the radar drawn from it.
//
// Counts is keyed by emotion label; Shares are those counts over the scored
// total; Radar holds one share per label in Emotions order, so a chart can
// plot it without re-deriving the order. PositiveRatio is the polarity split.
type EmotionView struct {
	Counts        map[string]int
	Shares        map[string]float64
	Radar         []float64
	PositiveRatio float64
	NeutralCount  int
	ScoredTotal   int
}

// VersionSplitView is one model version's reading of the same conversation.
//
// PositiveRatio has the same denominator as EmotionView.PositiveRatio — the
// messages with a polarity at all, neutral excluded — so the two figures in the
// report mean the same thing and can be read side by side.
type VersionSplitView struct {
	Version       string
	Positive      int
	Negative      int
	Neutral       int
	PositiveRatio float64
}

// ComparisonView is the side-by-side reading of two versions.
//
// Present is false when the conversation was scored by a single version. The
// template hides the whole section on it rather than rendering a comparison of
// a version with itself, which would read as perfect agreement.
//
// DisagreementRate is Differing over BothScored, and BothScored is not the
// number of messages in the conversation: it is the number both versions
// looked at. A rate over the wrong denominator would understate the gap
// whenever one version covered less of the conversation than the other.
type ComparisonView struct {
	Present          bool
	Current          VersionSplitView
	Other            VersionSplitView
	BothScored       int
	Differing        int
	DisagreementRate float64
}

// TrendPointView is one day of the rolling estimate, already judged.
//
// Drawable carries the store's verdict rather than a threshold re-derived here:
// there is one definition of "enough samples" and it lives with the numbers.
type TrendPointView struct {
	Day      string
	Mean     float64
	StdDev   float64
	Count    int
	Drawable bool
}

// TrendView is the rolling curve plus the counts the chart and its caveats
// need.
//
// Drawable and Thin partition the points: every point is one or the other, and
// both are counted because a chart that silently dropped the thin ones would
// leave a reader unable to tell "no messages" from "too few to draw". MeanLow
// and MeanHigh bound the drawable points only, and are zero when there are
// none — a range over points that were not drawn would describe nothing.
//
// Axis is half the height of the y range, rounded up to a readable two
// decimals and never below minAxisSpan. It is computed here rather than in the
// chart code because ECharts prints its min and max labels verbatim: handing it
// -0.3796374984823631 as a bound produced an axis whose top label was the full
// float, clipped by the canvas edge into "823631". A number a reader cannot
// hold is not a label.
type TrendView struct {
	Points   []TrendPointView
	Drawable int
	Thin     int
	MeanLow  float64
	MeanHigh float64
	Axis     float64
}

// minAxisSpan is the narrowest half-height the trend axis may take. Real
// conversations sit in a band about 0.07 wide, so a purely fitted axis would
// magnify noise into drama; a fixed [-1, 1] would flatten the same band into a
// straight line. ±0.2 caps the magnification at roughly five.
const minAxisSpan = 0.2

// CoverageView states how much of the conversation the sentiment pass covered.
type CoverageView struct {
	Scored        int
	Total         int
	Partial       bool
	ExcludedTypes []store.TypeCount
}

// SummaryView is the headline numbers.
type SummaryView struct {
	SelfMsgs     int
	PartnerMsgs  int
	SelfChars    int
	PartnerChars int

	AvgSelfLen    float64
	AvgPartnerLen float64

	LateNightRatio float64
	InitiatorRatio float64

	// TurnBalanceCharRatio and TurnBalanceMsgRatio are ratios, not shares:
	// 1.27 means this side wrote 1.27 characters for every one of the other
	// side's, and the bar beside them is drawn from ratio/(1+ratio) so the two
	// sides still meet at a whole. Passing a ratio straight to the bar helper
	// produced a 126.8% width and a negative one beside it on the first run
	// over real data.
	TurnBalanceCharRatio float64
	TurnBalanceMsgRatio  float64

	DailyFirstIsSelfCount int
	DailyLastIsSelfCount  int
	DayCount              int
	ConversationCount     int

	Reply ReplyView
	Types []store.TypeCount
}

// MonthView is one month's top words, already ranked by the store.
//
// The words are a token ranking, not a sentence: a month is a slice of the same
// word cloud over time, and it carries nothing a message body could ride in on.
type MonthView struct {
	Month string
	Words []WordCount
}

// HeatCellView is one square of the 24x7 grid.
type HeatCellView struct {
	Weekday      int
	Hour         int
	SelfCount    int
	PartnerCount int
}

// MetaView is what a reader needs to place the report: which conversation, over
// what period, and which model versions produced it.
type MetaView struct {
	// TalkerHash identifies the conversation without naming it. The report is
	// meant to be shared, and a wxid is an account identifier.
	TalkerHash            string
	RangeStartMs          int64
	RangeEndMs            int64
	BehaviorModelVersion  string
	SentimentModelVersion string
}

// View is the complete report payload.
type View struct {
	Meta       MetaView
	Summary    SummaryView
	Heatmap    []HeatCellView
	Words      WordsView
	Emoji      EmojiView
	Emotion    EmotionView
	Coverage   CoverageView
	Comparison ComparisonView
	Trend      TrendView
	// EmotionSource names the model version the seven emotion labels came
	// from. It is not always Meta.SentimentModelVersion: only the lexicon
	// produces DUTIR labels, so a model-version report shows the lexicon's
	// radar and has to say whose it is. Empty means no version produced any,
	// and the template then draws no radar at all rather than faking seven
	// categories out of a polarity sign (ADR-0004).
	EmotionSource string
	Disclaimer    string

	// Narrative is the paragraph a local model wrote from the aggregates alone.
	// It is empty when no narrator was configured or the model could not be
	// reached, and NarrativeNote then says why in one short, path-free phrase.
	// The two are mutually exclusive: a section that showed both a paragraph
	// and an apology would read as if two runs had been merged.
	Narrative     string
	NarrativeNote string

	// Monthly is the per-month top words for the other side, oldest month
	// first. It is empty on data scored before monthly periods existed, and the
	// template then explains that a rescan is needed rather than drawing a
	// blank chart that reads as "no words that month".
	Monthly []MonthView

	// Highlights contains relationship milestones: journey start, streaks, peak days, and late nights.
	Highlights HighlightsView
}

// MilestoneView represents one key milestone or highlight moment.
type MilestoneView struct {
	Kind        string  `json:"kind"`
	Date        string  `json:"date"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Metric      string  `json:"metric"`
	MetricValue float64 `json:"metric_value"`
}

// HighlightsView groups milestone moments discovered in the conversation.
type HighlightsView struct {
	Present       bool            `json:"present"`
	JourneyDays   int             `json:"journey_days"`
	StartDate     string          `json:"start_date"`
	EndDate       string          `json:"end_date"`
	MaxStreakDays int             `json:"max_streak_days"`
	StreakPeriod  string          `json:"streak_period"`
	PeakDays      []MilestoneView `json:"peak_days"`
	LateNights    []MilestoneView `json:"late_nights"`
	Events        []MilestoneView `json:"events"`
}

// DisclaimerText is fixed. It is a constant rather than a field a caller fills
// in, because a disclaimer that can be omitted is one that eventually will be.
const DisclaimerText = "玩具级统计，非关系诊断。情绪判定基于词典规则，会系统性地误判反话、爱称与玩笑（例如把“呵呵”当成中性）；" +
	"这里的任何数字都不代表你们的关系好坏。报告含对方的聊天记录，分享前请先征得对方同意。"

// Emotions is the fixed label order the radar and the shares are aligned to.
var Emotions = []string{"好", "乐", "哀", "怒", "惧", "恶", "惊"}

// NeutralLabel is the label a scored message carries when no emotion word was
// matched.
const NeutralLabel = "neutral"

// TalkerHash renders a conversation key as a short, stable, non-reversible
// identifier. Sixteen hex characters are plenty to tell two conversations
// apart while revealing nothing about the account behind them.
func TalkerHash(talker string) string {
	sum := sha256.Sum256([]byte(talker))
	return hex.EncodeToString(sum[:8])
}
