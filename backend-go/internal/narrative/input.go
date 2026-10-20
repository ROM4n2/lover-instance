package narrative

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// TrendPoint is one day of the rolling estimate, dimensioned exactly like the
// report's trend so a caller can copy it across without reinvention. It carries
// a date string and three numbers: no message, no time of day.
type TrendPoint struct {
	Day    string
	Mean   float64
	StdDev float64
	Count  int
}

// Input is everything the narrator is allowed to know.
//
// Every field is an aggregate: counts, shares, date strings and word tokens.
// There is no Content, Talker, Sender, Wxid or Chat field, and there must never
// be one — the whitelist test pins the field set so that adding a body field
// fails the build the moment it appears. Message text cannot reach the model
// because there is nowhere to put it.
type Input struct {
	Days            int
	Messages        int
	SelfMessages    int
	PartnerMessages int

	RangeStart string // "2026-08-15", day precision only
	RangeEnd   string

	TopWordsSelf    []string // words only, never a sentence
	TopWordsPartner []string
	TopExpressions  []string // bracketed expression markers, just tokens

	EmotionShares   map[string]float64  // seven emotions + neutral -> share of scored
	TrendPoints     []TrendPoint        // one point per day
	MonthlyTop      map[string][]string // "2026-08" -> top words
	SentimentShares map[string]float64  // positive/negative/neutral -> share
}

// Digest is the cache key for one rendering: sha256 over a canonical encoding
// of the input plus the model and prompt version.
//
// The maps are serialised in key order rather than map order. Go randomises
// map iteration, so without that step two identical inputs could hash
// differently and the cache would never hit; the ordering is what makes "same
// input, same bytes" true.
func (i Input) Digest(model, version string) string {
	payload, err := json.Marshal(i.canonical())
	if err != nil {
		// canonical() holds only strings, ints and floats, so marshalling
		// cannot fail. A panic here means the type grew a field json cannot
		// encode, which is a programming error worth surfacing loudly.
		panic("narrative: encode input digest: " + err.Error())
	}
	hasher := sha256.New()
	hasher.Write(payload)
	hasher.Write([]byte{0})
	hasher.Write([]byte(model))
	hasher.Write([]byte{0})
	hasher.Write([]byte(version))
	return hex.EncodeToString(hasher.Sum(nil))
}

// canonicalInput mirrors Input, but each map becomes a slice sorted by key so
// the JSON encoding is independent of map iteration order. Struct fields encode
// in declaration order, which gives the fixed field order the digest relies on.
type canonicalInput struct {
	Days            int
	Messages        int
	SelfMessages    int
	PartnerMessages int
	RangeStart      string
	RangeEnd        string
	TopWordsSelf    []string
	TopWordsPartner []string
	TopExpressions  []string
	EmotionShares   []digestNumber
	TrendPoints     []TrendPoint
	MonthlyTop      []digestWords
	SentimentShares []digestNumber
}

type digestNumber struct {
	Key   string
	Value float64
}

type digestWords struct {
	Key   string
	Words []string
}

// canonical returns a copy of i with every map flattened into a key-sorted
// slice, so the digest does not depend on insertion order.
func (i Input) canonical() canonicalInput {
	return canonicalInput{
		Days:            i.Days,
		Messages:        i.Messages,
		SelfMessages:    i.SelfMessages,
		PartnerMessages: i.PartnerMessages,
		RangeStart:      i.RangeStart,
		RangeEnd:        i.RangeEnd,
		TopWordsSelf:    i.TopWordsSelf,
		TopWordsPartner: i.TopWordsPartner,
		TopExpressions:  i.TopExpressions,
		EmotionShares:   sortedNumbers(i.EmotionShares),
		TrendPoints:     i.TrendPoints,
		MonthlyTop:      sortedWordLists(i.MonthlyTop),
		SentimentShares: sortedNumbers(i.SentimentShares),
	}
}

// sortedKeys returns the keys of m in ascending order. This is the one place
// map ordering is imposed; remove it and every caller silently stops being
// deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedNumbers(m map[string]float64) []digestNumber {
	keys := sortedKeys(m)
	out := make([]digestNumber, 0, len(keys))
	for _, k := range keys {
		out = append(out, digestNumber{Key: k, Value: m[k]})
	}
	return out
}

func sortedWordLists(m map[string][]string) []digestWords {
	keys := sortedKeys(m)
	out := make([]digestWords, 0, len(keys))
	for _, k := range keys {
		out = append(out, digestWords{Key: k, Words: m[k]})
	}
	return out
}
