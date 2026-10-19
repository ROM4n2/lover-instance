package chatlogkeeper

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleExport mirrors the shape M0 measured on a real chatlog-keeper export:
// a JSON array, ts as epoch-seconds float, server_id present on most rows and
// null on some, account_id and thread_id constant across every row.
const sampleExport = `[
  {"ts":1792166715.0,"sender":"Peer","sender_wxid":"wxid_peer","chat_room":"wxid_peer",
   "conversation_id":"wxid_peer","content":"hi","msg_type":1,"is_self":false,
   "account_id":"wxid_self","thread_id":"thread-one","server_id":"7058190743326509315",
   "source_offset":"wechat_export:1","conversation_type":"direct","is_group_chat":false},
  {"ts":1792166715.0,"sender":"Me","sender_wxid":"wxid_self","chat_room":"wxid_peer",
   "conversation_id":"wxid_peer","content":"hey","msg_type":47,"is_self":true,
   "account_id":"wxid_self","thread_id":"thread-one","server_id":null,
   "source_offset":"wechat_export:2","conversation_type":"direct","is_group_chat":false}
]`

func decodeSample(t *testing.T) []Message {
	t.Helper()
	rows, err := DecodeMessages(strings.NewReader(sampleExport))
	if err != nil {
		t.Fatalf("DecodeMessages(sample) error: %v", err)
	}
	return rows
}

// TestTimestamp_RendersEpochSecondsWithoutScientificNotation guards the defect
// this adapter exists to prevent: ts arrives as a float64, and rendering it via
// fmt.Sprint produces "1.792166715e+09", which model.NormalizeTs rejects.
func TestTimestamp_RendersEpochSecondsWithoutScientificNotation(t *testing.T) {
	got, err := Message{Ts: 1792166715.0}.Timestamp()
	if err != nil {
		t.Fatalf("Timestamp() error: %v", err)
	}
	if got != "1792166715" {
		t.Errorf("Timestamp() = %q, want %q", got, "1792166715")
	}
	if strings.ContainsAny(got, "eE.") {
		t.Errorf("Timestamp() = %q: must be a plain digit string, never scientific or decimal notation", got)
	}
	if len(got) != 10 {
		t.Errorf("len(Timestamp()) = %d, want 10 so NormalizeTs reads it as seconds", len(got))
	}
}

func TestTimestamp_WidensSubSecondPrecisionToMillis(t *testing.T) {
	got, err := Message{Ts: 1792166715.25}.Timestamp()
	if err != nil {
		t.Fatalf("Timestamp() error: %v", err)
	}
	if got != "1792166715250" {
		t.Errorf("Timestamp() = %q, want %q (13 digits: precision widened, not truncated)", got, "1792166715250")
	}
}

func TestTimestamp_RejectsNonPositiveAndNonFiniteValues(t *testing.T) {
	cases := []struct {
		name string
		ts   float64
	}{
		{"zero", 0},
		{"negative", -1},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Message{Ts: tc.ts}.Timestamp()
			if err == nil {
				t.Fatalf("Timestamp(ts=%v) = %q, want an error", tc.ts, got)
			}
			if got != "" {
				t.Errorf("Timestamp(ts=%v) = %q on error, want empty string", tc.ts, got)
			}
		})
	}
}

func TestToRaw_MapsEveryCanonicalColumn(t *testing.T) {
	rows := decodeSample(t)
	raw, err := rows[0].ToRaw()
	if err != nil {
		t.Fatalf("ToRaw() error: %v", err)
	}
	if raw.MsgSvrID != "7058190743326509315" {
		t.Errorf("MsgSvrID = %q, want the row's server_id", raw.MsgSvrID)
	}
	if raw.Time != "1792166715" {
		t.Errorf("Time = %q, want %q", raw.Time, "1792166715")
	}
	if raw.Talker != "wxid_peer" {
		t.Errorf("Talker = %q, want conversation_id", raw.Talker)
	}
	if raw.Sender != "wxid_peer" {
		t.Errorf("Sender = %q, want sender_wxid", raw.Sender)
	}
	if raw.Type != 1 {
		t.Errorf("Type = %d, want msg_type 1", raw.Type)
	}
	if raw.IsSelf {
		t.Error("IsSelf = true, want false for a peer message")
	}
	if raw.Content != "hi" {
		t.Errorf("Content = %q, want %q", raw.Content, "hi")
	}
	if raw.Extra["source_offset"] != "wechat_export:1" {
		t.Errorf(`Extra["source_offset"] = %v, want the row's source_offset`, raw.Extra["source_offset"])
	}
}

// TestToRaw_SeparatesTalkerFromSender uses a row whose conversation_id and
// sender_wxid differ, which the shared sampleExport cannot: in a one-to-one
// conversation the sample's row 0 carries the same wxid in both fields, so a
// Talker/Sender swap in ToRaw is invisible to every other test in this repo.
// This row makes the swap fail loudly, since a wrong Talker corrupts every
// per-conversation grouping downstream.
func TestToRaw_SeparatesTalkerFromSender(t *testing.T) {
	const distinctRow = `[{"ts":1792166715.0,"sender":"Peer Display","sender_wxid":"wxid_the_sender",
	  "chat_room":"wxid_the_talker","conversation_id":"wxid_the_talker","content":"hi",
	  "msg_type":1,"is_self":false,"account_id":"wxid_self","thread_id":"thread-one",
	  "server_id":"7058190743326509315","source_offset":"wechat_export:1",
	  "conversation_type":"direct","is_group_chat":false}]`
	rows, err := DecodeMessages(strings.NewReader(distinctRow))
	if err != nil {
		t.Fatalf("DecodeMessages(distinct row) error: %v", err)
	}
	raw, err := rows[0].ToRaw()
	if err != nil {
		t.Fatalf("ToRaw() error: %v", err)
	}
	if raw.Talker != "wxid_the_talker" {
		t.Errorf("Talker = %q, want conversation_id %q", raw.Talker, "wxid_the_talker")
	}
	if raw.Sender != "wxid_the_sender" {
		t.Errorf("Sender = %q, want sender_wxid %q", raw.Sender, "wxid_the_sender")
	}
	if raw.Talker == raw.Sender {
		t.Error("Talker and Sender are equal: the two fields were sourced from one another")
	}
}

// TestToRaw_LeavesSeqZero pins the seq-is-a-timestamp decision: the exporter has
// no seq column, and copying ts into Seq would recreate the trap the project
// forbids.
func TestToRaw_LeavesSeqZero(t *testing.T) {
	rows := decodeSample(t)
	raw, err := rows[0].ToRaw()
	if err != nil {
		t.Fatalf("ToRaw() error: %v", err)
	}
	if raw.Seq != 0 {
		t.Errorf("Seq = %d, want 0: this exporter has no seq column", raw.Seq)
	}
	// The timestamp must not leak into any identity field. The keys are rendered
	// to strings before comparing: a bare `==` between an `any` holding a string
	// and an int64 compares dynamic types first and is therefore unconditionally
	// false, which is how this assertion used to pass without asserting anything.
	for _, identity := range []struct{ name, key string }{
		{"MsgSvrID", raw.MsgSvrID},
		{`Extra["source_offset"]`, fmt.Sprint(raw.Extra["source_offset"])},
	} {
		if identity.key == raw.Time {
			t.Errorf("%s = %q equals the row's time: an identity must never be the timestamp", identity.name, identity.key)
		}
		if identity.key == fmt.Sprint(raw.Seq) {
			t.Errorf("%s = %q equals Seq: an identity must never be the seq column", identity.name, identity.key)
		}
	}
}

func TestParseMessages_NormalizesEveryRow(t *testing.T) {
	messages, err := ParseMessages(strings.NewReader(sampleExport))
	if err != nil {
		t.Fatalf("ParseMessages() error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	if messages[0].UniqueKey != "7058190743326509315" {
		t.Errorf("UniqueKey = %q, want the row's server_id", messages[0].UniqueKey)
	}
	if messages[0].TimestampMs != 1792166715000 {
		t.Errorf("TimestampMs = %d, want 1792166715000 (epoch ms from epoch seconds)", messages[0].TimestampMs)
	}
	if messages[1].IsSelf != true {
		t.Error("messages[1].IsSelf = false, want true from the row's is_self flag")
	}
}

// TestParseMessages_KeysNullServerIDRowOnSourceOffset is the M0 contract: 3 of
// 32624 measured rows carry a null server_id, and the parse must key those rows
// on source_offset instead of failing or colliding them together.
func TestParseMessages_KeysNullServerIDRowOnSourceOffset(t *testing.T) {
	messages, err := ParseMessages(strings.NewReader(sampleExport))
	if err != nil {
		t.Fatalf("ParseMessages() error: %v", err)
	}
	if messages[1].UniqueKey != "wechat_export:2" {
		t.Errorf("UniqueKey = %q, want %q: a null server_id must fall back to source_offset", messages[1].UniqueKey, "wechat_export:2")
	}
	if messages[0].UniqueKey == messages[1].UniqueKey {
		t.Fatalf("both rows keyed on %q: distinct messages collapsed onto one key", messages[0].UniqueKey)
	}
}

// TestParseMessages_NeverKeysOnAccountLevelConstants is the regression guard for
// the alphabetical catch-all M0 replaced: account_id and thread_id are identical
// on every row of an export, so keying on either would collapse the whole
// conversation into one record.
func TestParseMessages_NeverKeysOnAccountLevelConstants(t *testing.T) {
	messages, err := ParseMessages(strings.NewReader(sampleExport))
	if err != nil {
		t.Fatalf("ParseMessages() error: %v", err)
	}
	for i, m := range messages {
		if m.UniqueKey == "wxid_self" {
			t.Errorf("messages[%d].UniqueKey = %q: an account-level constant must never be the message key", i, m.UniqueKey)
		}
		if m.UniqueKey == "thread-one" {
			t.Errorf("messages[%d].UniqueKey = %q: a conversation-level constant must never be the message key", i, m.UniqueKey)
		}
	}
}

func TestParseMessages_ReportsTheFailingRowIndex(t *testing.T) {
	const badTs = `[{"ts":0,"source_offset":"wechat_export:9"}]`
	_, err := ParseMessages(strings.NewReader(badTs))
	if err == nil {
		t.Fatal("ParseMessages(row with ts=0): want an error, got nil (silent swallow)")
	}
	if !strings.Contains(err.Error(), "export row 0") {
		t.Errorf("error = %v, want it to name the failing row index", err)
	}
}

func TestParseMessages_RejectsMalformedJSON(t *testing.T) {
	if _, err := ParseMessages(strings.NewReader("<<<not json>>>")); err == nil {
		t.Fatal("ParseMessages(garbage): want a decode error, got nil")
	}
}

// TestDecodeMessages_RefusesAnythingButOneCompleteArray guards the silent
// partial ingest defect. A decoder that stops at the first complete JSON value
// reported no error for two concatenated arrays, for trailing garbage, and for
// the bare literal `null` — so an export that was appended to instead of
// overwritten would ingest only its first half, and a failed export would be
// indistinguishable from a legitimately empty conversation. All of those must
// fail loudly.
func TestDecodeMessages_RefusesAnythingButOneCompleteArray(t *testing.T) {
	const oneRow = `[{"ts":1792166715.0,"source_offset":"wechat_export:1"}]`
	cases := []struct {
		name string
		in   string
	}{
		{"two concatenated arrays", oneRow + oneRow},
		{"two arrays separated by whitespace", oneRow + "\n\n" + oneRow},
		{"trailing garbage", oneRow + " garbage"},
		{"trailing bare value", oneRow + `42`},
		{"bare null document", `null`},
		{"object instead of array", `{"messages":[]}`},
		{"empty document", ``},
		{"whitespace only", "  \n\t "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := DecodeMessages(strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("DecodeMessages(%d bytes) = %d row(s), nil error: want a loud failure", len(tc.in), len(rows))
			}
		})
	}
}

// TestDecodeMessages_AcceptsEmptyArray is the other half of the guard above: a
// legitimately empty export must still parse, so strictness cannot be bought by
// rejecting empty input wholesale.
func TestDecodeMessages_AcceptsEmptyArray(t *testing.T) {
	rows, err := DecodeMessages(strings.NewReader(`[]`))
	if err != nil {
		t.Fatalf("DecodeMessages(`[]`) error: %v — an empty export is valid, not malformed", err)
	}
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(rows))
	}
}

func TestDecodeMessages_TreatsNullServerIDAsEmptyNotAnError(t *testing.T) {
	rows := decodeSample(t)
	if rows[1].ServerID != "" {
		t.Errorf("ServerID = %q, want empty string for a JSON null", rows[1].ServerID)
	}
	if rows[1].SourceOffset == "" {
		t.Error("SourceOffset empty: the fallback identity was dropped during decode")
	}
}

// TestParseMessages_SampleFixture is the contract test between the committed
// synthetic corpus and this adapter: fixtures/sample_chat.json is generated in
// chatlog-keeper's measured format, so it must ingest without error and key
// every row. It needs no private data, so it runs in every `go test ./...` and
// fails loudly if either side of the contract drifts.
func TestParseMessages_SampleFixture(t *testing.T) {
	const fixture = "../../../../fixtures/sample_chat.json"
	file, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open committed fixture %q: %v", fixture, err)
	}
	defer file.Close()

	messages, err := ParseMessages(file)
	if err != nil {
		t.Fatalf("ParseMessages(%q) error: %v — the synthetic corpus and this adapter disagree", fixture, err)
	}
	if len(messages) == 0 {
		t.Fatal("fixture parsed to zero messages")
	}
	seen := make(map[string]struct{}, len(messages))
	for i, m := range messages {
		if m.UniqueKey == "" {
			t.Fatalf("messages[%d] has an empty UniqueKey: the corpus no longer carries a usable identity", i)
		}
		if _, dup := seen[m.UniqueKey]; dup {
			t.Fatalf("messages[%d] duplicates key %q: the corpus collapsed two messages onto one key", i, m.UniqueKey)
		}
		seen[m.UniqueKey] = struct{}{}
		if m.TimestampMs <= 0 {
			t.Fatalf("messages[%d].TimestampMs = %d, want a positive epoch", i, m.TimestampMs)
		}
	}
	t.Logf("synthetic fixture: %d messages, %d distinct keys", len(messages), len(seen))
}

// TestParseMessages_RealExport is the opt-in M0 acceptance check: it points the
// adapter at a real chatlog-keeper export and asserts the identity contract
// holds across the whole file. It is skipped unless CHATLOG_EVIDENCE names a
// file, so a plain `go test ./...` never depends on private data that exists on
// only one machine.
//
// Reproduce (the export lives in the gitignored workmem/ tree). `go test` runs
// the test binary with its working directory set to this package, so the
// relative path counts from backend-go/internal/source/chatlogkeeper:
//
//	CHATLOG_EVIDENCE=../../../../workmem/evidence/full/wechat_messages.json \
//	  go test ./internal/source/chatlogkeeper/ -run RealExport -v
func TestParseMessages_RealExport(t *testing.T) {
	path := os.Getenv("CHATLOG_EVIDENCE")
	if path == "" {
		t.Skip("CHATLOG_EVIDENCE not set; skipping real-export integration check")
	}
	file, err := os.Open(path)
	if err != nil {
		if !filepath.IsAbs(path) {
			cwd, cwdErr := os.Getwd()
			if cwdErr == nil {
				t.Fatalf("open evidence export %q: %v (a relative CHATLOG_EVIDENCE resolves from the package directory %q; pass an absolute path to avoid the guesswork)", filepath.ToSlash(path), err, filepath.ToSlash(cwd))
			}
		}
		t.Fatalf("open evidence export %q: %v", path, err)
	}
	defer file.Close()

	messages, err := ParseMessages(file)
	if err != nil {
		t.Fatalf("ParseMessages(%q) error: %v", path, err)
	}
	if len(messages) == 0 {
		t.Fatal("parsed zero messages from a real export")
	}

	keys := make(map[string]int, len(messages))
	constant := map[string]string{"account_id": "", "thread_id": ""}
	for i, m := range messages {
		if m.UniqueKey == "" {
			t.Fatalf("messages[%d] has an empty UniqueKey", i)
		}
		if m.TimestampMs <= 0 {
			t.Fatalf("messages[%d].TimestampMs = %d, want a positive epoch", i, m.TimestampMs)
		}
		keys[m.UniqueKey]++
	}
	for _, field := range []string{"account_id", "thread_id"} {
		if v, ok := messages[0].Extra[field].(string); ok && v != "" {
			constant[field] = v
		}
	}
	for i, m := range messages {
		for field, value := range constant {
			if value == "" {
				continue
			}
			if m.UniqueKey == value {
				t.Fatalf("messages[%d].UniqueKey = %q: the %s constant became the message key", i, value, field)
			}
		}
	}

	duplicates := 0
	for _, count := range keys {
		if count > 1 {
			duplicates++
		}
	}
	t.Logf("real export: %d messages, %d distinct keys, %d colliding key(s)", len(messages), len(keys), duplicates)
	if duplicates > 0 {
		t.Errorf("%d key(s) collide across distinct messages: uniqueness is not holding on the real export", duplicates)
	}
}