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

	LateNightRatio       float64
	InitiatorRatio       float64
	TurnBalanceCharRatio float64

	DailyFirstIsSelfCount int
	DailyLastIsSelfCount  int
	DayCount              int
	ConversationCount     int

	Reply ReplyView
	Types []store.TypeCount
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
	Disclaimer string
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
