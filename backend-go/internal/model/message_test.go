package model

import "testing"

// wantEpochMs is one instant (epoch 1756225000 seconds) expressed at every
// digit precision the exporter emits: 10=s, 13=ms, 16=us, 19=ns strings must
// all normalize to this single epoch-ms value.
const wantEpochMs int64 = 1756225000000

// someMsgSvrID is a realistic 19-digit message server id (not a timestamp).
const someMsgSvrID = "7058190743326509315"

// seqStyleTimestamp is the seq-is-timestamp trap: a second-precision epoch
// padded to 13 digits, so its millisecond digits are all zero. It identifies
// a moment, never a message, so it must never become a primary key.
const seqStyleTimestamp = "1756225000000"

func TestNormalizeTs_MillisStringReturnsSameEpochMs(t *testing.T) {
	got, err := NormalizeTs("1756225000000")
	if err != nil {
		t.Fatalf(`NormalizeTs("1756225000000") error: %v`, err)
	}
	if got != wantEpochMs {
		t.Errorf(`NormalizeTs("1756225000000") = %d, want %d`, got, wantEpochMs)
	}
}

func TestNormalizeTs_SecondsStringNormalizesToEpochMs(t *testing.T) {
	got, err := NormalizeTs("1756225000")
	if err != nil {
		t.Fatalf(`NormalizeTs("1756225000") error: %v`, err)
	}
	if got != wantEpochMs {
		t.Errorf(`NormalizeTs("1756225000") = %d, want %d`, got, wantEpochMs)
	}
}

func TestNormalizeTs_MicrosStringNormalizesToEpochMs(t *testing.T) {
	got, err := NormalizeTs("1756225000000000")
	if err != nil {
		t.Fatalf(`NormalizeTs("1756225000000000") error: %v`, err)
	}
	if got != wantEpochMs {
		t.Errorf(`NormalizeTs("1756225000000000") = %d, want %d`, got, wantEpochMs)
	}
}

func TestNormalizeTs_NanosStringNormalizesToEpochMs(t *testing.T) {
	got, err := NormalizeTs("1756225000000000000")
	if err != nil {
		t.Fatalf(`NormalizeTs("1756225000000000000") error: %v`, err)
	}
	if got != wantEpochMs {
		t.Errorf(`NormalizeTs("1756225000000000000") = %d, want %d`, got, wantEpochMs)
	}
}

func TestNormalizeTs_RejectsAmbiguousLengthEleven(t *testing.T) {
	if _, err := NormalizeTs("17562250000"); err == nil {
		t.Fatalf(`NormalizeTs("17562250000") has length 11: want error, got nil`)
	}
}

func TestNormalizeTs_RejectsEmptyString(t *testing.T) {
	if _, err := NormalizeTs(""); err == nil {
		t.Fatalf(`NormalizeTs(""): want error for missing input, got nil`)
	}
}

func TestParseRaw_JudgesIsSelfTrue(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: someMsgSvrID, Time: "1756225000000", IsSelf: true}
	canonical, err := ParseRaw(raw)
	if err != nil {
		t.Fatalf("ParseRaw() error: %v", err)
	}
	if !canonical.IsSelf {
		t.Errorf("canonical.IsSelf = false, want true")
	}
}

func TestParseRaw_JudgesIsSelfFalseWithoutSignals(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: someMsgSvrID, Time: "1756225000000"}
	canonical, err := ParseRaw(raw)
	if err != nil {
		t.Fatalf("ParseRaw() error: %v", err)
	}
	if canonical.IsSelf {
		t.Errorf("canonical.IsSelf = true, want false without any self signal")
	}
}

func TestParseRaw_PromotesIsSelfFromTruthyExtraMarker(t *testing.T) {
	raw := RawChatlogMessage{
		MsgSvrID: someMsgSvrID,
		Time:     "1756225000000",
		Extra:    map[string]any{"is_self": "1"},
	}
	canonical, err := ParseRaw(raw)
	if err != nil {
		t.Fatalf("ParseRaw() error: %v", err)
	}
	if !canonical.IsSelf {
		t.Errorf("canonical.IsSelf = false, want true promoted from Extra is_self marker")
	}
}

func TestParseRaw_NormalizesTimestampAndKeepsFields(t *testing.T) {
	raw := RawChatlogMessage{
		Seq: 42, MsgSvrID: someMsgSvrID, Time: "1756225000",
		Talker: "wxid_room", Sender: "wxid_peer", IsSelf: true,
		Type: 1, SubType: 0, Content: "hello",
	}
	canonical, err := ParseRaw(raw)
	if err != nil {
		t.Fatalf("ParseRaw() error: %v", err)
	}
	if canonical.TimestampMs != wantEpochMs {
		t.Errorf("canonical.TimestampMs = %d, want %d", canonical.TimestampMs, wantEpochMs)
	}
	if canonical.UniqueKey != someMsgSvrID {
		t.Errorf("canonical.UniqueKey = %q, want %q", canonical.UniqueKey, someMsgSvrID)
	}
	if canonical.Seq != 42 {
		t.Errorf("canonical.Seq = %d, want 42", canonical.Seq)
	}
	if canonical.Content != "hello" {
		t.Errorf("canonical.Content = %q, want %q", canonical.Content, "hello")
	}
}

func TestParseRaw_RejectsAmbiguousTimestamp(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: someMsgSvrID, Time: "17562250000"}
	if _, err := ParseRaw(raw); err == nil {
		t.Fatal("ParseRaw with 11-digit time: want error, got nil")
	}
}

func TestParseRaw_RejectsZeroValueRow(t *testing.T) {
	if _, err := ParseRaw(RawChatlogMessage{}); err == nil {
		t.Fatal("ParseRaw(zero row): want error for missing time and identity, got nil")
	}
}

func TestDetectUniqueKey_PrefersMsgSvrID(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: someMsgSvrID, Extra: map[string]any{"id": "lesser-id"}}
	got, err := DetectUniqueKey(raw)
	if err != nil {
		t.Fatalf("DetectUniqueKey() error: %v", err)
	}
	if got != someMsgSvrID {
		t.Errorf("DetectUniqueKey() = %q, want %q", got, someMsgSvrID)
	}
}

func TestDetectUniqueKey_FallsBackToExtraIDWhenMsgSvrIDEmpty(t *testing.T) {
	raw := RawChatlogMessage{Extra: map[string]any{"id": "wxid_fallback"}}
	got, err := DetectUniqueKey(raw)
	if err != nil {
		t.Fatalf("DetectUniqueKey() error: %v", err)
	}
	if got != "wxid_fallback" {
		t.Errorf("DetectUniqueKey() = %q, want %q", got, "wxid_fallback")
	}
}

func TestDetectUniqueKey_FallsBackToExtraServerID(t *testing.T) {
	raw := RawChatlogMessage{Extra: map[string]any{"server_id": "srv-9"}}
	got, err := DetectUniqueKey(raw)
	if err != nil {
		t.Fatalf("DetectUniqueKey() error: %v", err)
	}
	if got != "srv-9" {
		t.Errorf("DetectUniqueKey() = %q, want %q", got, "srv-9")
	}
}

func TestDetectUniqueKey_ConvertsRemainingExtraScalars(t *testing.T) {
	raw := RawChatlogMessage{Extra: map[string]any{"id": "", "server_id": "", "nonce": 424242}}
	got, err := DetectUniqueKey(raw)
	if err != nil {
		t.Fatalf("DetectUniqueKey() error: %v", err)
	}
	if got != "424242" {
		t.Errorf("DetectUniqueKey() = %q, want %q from int scalar conversion", got, "424242")
	}
}

func TestDetectUniqueKey_ErrorsWhenAllCandidatesEmpty(t *testing.T) {
	got, err := DetectUniqueKey(RawChatlogMessage{})
	if err == nil {
		t.Fatal("DetectUniqueKey with no identity at all: want error, got nil")
	}
	if got != "" {
		t.Errorf("DetectUniqueKey() = %q on error, want empty string", got)
	}
}

func TestDetectUniqueKey_SkipsSeqStyleTimestampMsgSvrID(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: seqStyleTimestamp, Extra: map[string]any{"id": "real-msg-id"}}
	got, err := DetectUniqueKey(raw)
	if err != nil {
		t.Fatalf("DetectUniqueKey() error: %v", err)
	}
	if got != "real-msg-id" {
		t.Errorf("DetectUniqueKey() = %q, want %q: seq-style timestamp must not be a primary key", got, "real-msg-id")
	}
}

func TestDetectUniqueKey_ErrorsWhenOnlySeqStyleTimestampPresent(t *testing.T) {
	raw := RawChatlogMessage{MsgSvrID: seqStyleTimestamp}
	got, err := DetectUniqueKey(raw)
	if err == nil {
		t.Fatal("DetectUniqueKey with only a seq-style timestamp: want error, got nil")
	}
	if got != "" {
		t.Errorf("DetectUniqueKey() = %q on error, want empty string", got)
	}
}

func TestScalarToString_RendersEachScalarKind(t *testing.T) {
	if got := scalarToString("plain"); got != "plain" {
		t.Errorf("scalarToString(%q) = %q, want %q", "plain", got, "plain")
	}
	if got := scalarToString(7); got != "7" {
		t.Errorf("scalarToString(7) = %q, want %q", got, "7")
	}
	if got := scalarToString(2.5); got != "2.5" {
		t.Errorf("scalarToString(2.5) = %q, want %q", got, "2.5")
	}
	if got := scalarToString(true); got != "true" {
		t.Errorf("scalarToString(true) = %q, want %q", got, "true")
	}
	if got := scalarToString(nil); got != "" {
		t.Errorf("scalarToString(nil) = %q, want empty string for non-scalar", got)
	}
}
