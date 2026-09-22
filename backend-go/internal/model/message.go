// Package model defines the canonical chatlog message: raw exporter rows are
// parsed exactly once into normalized, validated canonical messages.
package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RawChatlogMessage is one row exactly as the chatlog exporter emitted it:
// Time keeps its original digit-string form and identity fields may be empty
// or timestamp-shaped.
type RawChatlogMessage struct {
	Seq      int64
	MsgSvrID string
	Time     string
	Talker   string
	Sender   string
	IsSelf   bool
	Type     int
	SubType  int
	Content  string
	Extra    map[string]any
}

// CanonicalMessage is the normalized form: TimestampMs is always epoch
// milliseconds, UniqueKey is guaranteed non-empty, IsSelf is judged from every
// available signal. ParseRaw never returns the zero value with a nil error.
type CanonicalMessage struct {
	Seq         int64
	UniqueKey   string
	TimestampMs int64
	Talker      string
	Sender      string
	IsSelf      bool
	Type        int
	SubType     int
	Content     string
	Extra       map[string]any
}

// ParseRaw normalizes one raw row: the timestamp becomes epoch milliseconds
// and the unique key is detected. Any failed normalization surfaces as an
// error because zero-value silent success would corrupt downstream dedup.
func ParseRaw(raw RawChatlogMessage) (CanonicalMessage, error) {
	timestampMs, err := NormalizeTs(raw.Time)
	if err != nil {
		return CanonicalMessage{}, fmt.Errorf("parse message time: %w", err)
	}
	uniqueKey, err := DetectUniqueKey(raw)
	if err != nil {
		return CanonicalMessage{}, fmt.Errorf("parse message identity: %w", err)
	}
	return CanonicalMessage{
		Seq:         raw.Seq,
		UniqueKey:   uniqueKey,
		TimestampMs: timestampMs,
		Talker:      raw.Talker,
		Sender:      raw.Sender,
		IsSelf:      judgeIsSelf(raw),
		Type:        raw.Type,
		SubType:     raw.SubType,
		Content:     raw.Content,
		Extra:       raw.Extra,
	}, nil
}

// NormalizeTs converts a raw epoch timestamp to epoch milliseconds. The digit
// count identifies the unit by itself: 10=seconds, 13=milliseconds,
// 16=microseconds, 19=nanoseconds. Every other length (e.g. 11 digits) is
// ambiguous and rejected. Pure epoch arithmetic via time.Unix and friends —
// no timezone shifting happens here.
func NormalizeTs(rawTs string) (int64, error) {
	ts := strings.TrimSpace(rawTs)
	epoch, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("timestamp %q is not a base-10 integer: %w", rawTs, err)
	}
	switch len(ts) {
	case 10: // seconds
		return time.Unix(epoch, 0).UnixMilli(), nil
	case 13: // milliseconds
		return time.UnixMilli(epoch).UnixMilli(), nil
	case 16: // microseconds
		return time.UnixMicro(epoch).UnixMilli(), nil
	case 19: // nanoseconds
		return time.Unix(0, epoch).UnixMilli(), nil
	default:
		return 0, fmt.Errorf("timestamp %q has ambiguous length %d (want 10, 13, 16 or 19 digits)", ts, len(ts))
	}
}

// DetectUniqueKey returns the first usable identity in priority order:
// MsgSvrID, Extra["id"], Extra["server_id"], then the remaining non-empty
// Extra scalar values. It errors when every candidate is empty because a
// silent fallback to "" would collide distinct messages on one key.
func DetectUniqueKey(raw RawChatlogMessage) (string, error) {
	for _, candidate := range uniqueKeyCandidates(raw) {
		key := strings.TrimSpace(candidate)
		if key == "" || looksLikeSeqTimestamp(key) {
			continue
		}
		return key, nil
	}
	return "", fmt.Errorf("seq %d has no unique key: MsgSvrID and Extra id fields are all empty", raw.Seq)
}

// uniqueKeyCandidates lists identity candidates in priority order.
func uniqueKeyCandidates(raw RawChatlogMessage) []string {
	candidates := []string{
		raw.MsgSvrID,
		scalarToString(raw.Extra["id"]),
		scalarToString(raw.Extra["server_id"]),
	}
	return append(candidates, remainingExtraScalars(raw.Extra)...)
}

// remainingExtraScalars returns the other Extra scalar values, sorted by key
// so candidate order stays deterministic across runs (map order is random).
func remainingExtraScalars(extra map[string]any) []string {
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if key != "id" && key != "server_id" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, scalarToString(extra[key]))
	}
	return values
}

// scalarToString renders a JSON-style scalar as its identity string; nil and
// composite values render as "" so they fall through instead of becoming
// garbage keys.
func scalarToString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// looksLikeSeqTimestamp reports whether s is a 13-digit millisecond timestamp
// whose millisecond digits are all zero (e.g. "1756225000000"). Chatlog
// exporters routinely copy the seq column into id-shaped fields, but seq is
// itself a timestamp — the seq-is-timestamp pitfall — so such a value marks a
// moment, not a message, and must never be treated as a primary key.
func looksLikeSeqTimestamp(s string) bool {
	if len(s) != 13 {
		return false
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return false
	}
	return strings.HasSuffix(s, "000")
}

// judgeIsSelf combines every is_self signal: the exporter flag is
// authoritative, and a truthy Extra["is_self"] marker can only promote the
// judgment because some exporters serialize the flag as "1"/"true" while the
// parsed bool stays at its zero value.
func judgeIsSelf(raw RawChatlogMessage) bool {
	if raw.IsSelf {
		return true
	}
	return extraFlagTruthy(raw.Extra["is_self"])
}

// extraFlagTruthy reads a boolean carried inside Extra in any scalar encoding.
func extraFlagTruthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	default:
		return false
	}
}
