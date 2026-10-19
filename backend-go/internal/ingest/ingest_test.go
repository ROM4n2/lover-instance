package ingest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/store"
)

const testSource = "chatlog-keeper"

// testRow is the minimum a chatlog-keeper export row needs to be valid.
type testRow struct {
	tsSec   int64
	talker  string
	content string
	key     string
	isSelf  bool
}

// makeExport renders a valid export. The timestamp is emitted as a float
// because that is what the real exporter produces, and the adapter's job is to
// survive exactly that.
func makeExport(rows []testRow) string {
	parts := make([]string, 0, len(rows))
	for i, r := range rows {
		parts = append(parts, fmt.Sprintf(
			`{"ts":%d.0,"sender":"Peer","sender_wxid":"wxid_peer","chat_room":"%s","conversation_id":"%s","content":"%s","msg_type":1,"is_self":%t,"account_id":"acct","thread_id":"thr","server_id":"%s","source_offset":"off:%d","conversation_type":"direct","is_group_chat":false}`,
			r.tsSec, r.talker, r.talker, r.content, r.isSelf, r.key, i))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dsn, err := store.DSN(t.TempDir() + string(os.PathSeparator) + "data.db")
	if err != nil {
		t.Fatalf("store.DSN() error: %v", err)
	}
	s, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return s
}

func run(t *testing.T, s *store.Store, export string, talker string) (Report, error) {
	t.Helper()
	return Run(context.Background(), s, Options{Source: testSource, Talker: talker, Reader: strings.NewReader(export)})
}

// TestRun_FirstImportStoresEveryRow uses the committed synthetic corpus, which
// is the only export-shaped input in the repository.
func TestRun_FirstImportStoresEveryRow(t *testing.T) {
	// `go test` runs with the working directory set to this package, so the
	// committed fixture is three levels up.
	file, err := os.Open("../../../fixtures/sample_chat.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()

	s := newStore(t)
	report, err := Run(context.Background(), s, Options{Source: testSource, Reader: file})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Parsed == 0 {
		t.Fatal("Parsed = 0 for the committed fixture")
	}
	if report.Inserted != report.Parsed {
		t.Errorf("first import: inserted %d of %d parsed — the first import has no watermark and must take everything", report.Inserted, report.Parsed)
	}
	if report.WindowStartMs != 0 {
		t.Errorf("WindowStartMs = %d on a first import, want 0 (no window applies)", report.WindowStartMs)
	}
	if len(report.Talkers) != 1 || report.Talkers[0] != "wxid_peer_b" {
		t.Errorf("Talkers = %v, want [wxid_peer_b]", report.Talkers)
	}
}

func TestRun_SecondImportAddsNothing(t *testing.T) {
	const export = `[{"ts":1792166000.0,"sender":"Peer","sender_wxid":"wxid_peer","chat_room":"c","conversation_id":"c","content":"hi","msg_type":1,"is_self":false,"account_id":"acct","thread_id":"thr","server_id":"s1","source_offset":"off:0","conversation_type":"direct","is_group_chat":false}]`
	s := newStore(t)

	if _, err := run(t, s, export, ""); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	report, err := run(t, s, export, "")
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if report.Inserted != 0 || report.Updated != 0 || report.Skipped != 1 {
		t.Errorf("second import = %+v, want {Inserted:0 Skipped:1 Updated:0}", report)
	}
}

// TestRun_RescanWindowKeepsTheBoundaryMessage is the off-by-one guard. The
// window start is last_ts-60s, and a message sitting exactly on that edge must
// be re-checked: a `>` comparison would drop it on every rescan, and a message
// lost on the boundary of a repeating job is lost forever.
func TestRun_RescanWindowKeepsTheBoundaryMessage(t *testing.T) {
	const older = 1792166000
	const boundary = 1792166060
	const newest = 1792166120
	export := makeExport([]testRow{
		{tsSec: older, talker: "c", content: "old", key: "s1"},
		{tsSec: boundary, talker: "c", content: "on the edge", key: "s2"},
		{tsSec: newest, talker: "c", content: "new", key: "s3"},
	})

	s := newStore(t)
	if _, err := run(t, s, export, ""); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	report, err := run(t, s, export, "")
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if report.Parsed != 3 {
		t.Errorf("Parsed = %d, want 3", report.Parsed)
	}
	// The watermark after the first import is `newest`; the window starts 60s
	// before it, which is exactly `boundary`. So two rows are re-submitted:
	// `boundary` (the edge) and `newest`. `older` is behind the window.
	if report.Skipped != 2 || report.Inserted != 0 {
		t.Errorf("second import = %+v, want {Inserted:0 Skipped:2}: the boundary message must survive the window", report)
	}
	if want := int64(boundary) * 1000; report.WindowStartMs != want {
		t.Errorf("WindowStartMs = %d, want %d", report.WindowStartMs, want)
	}
}

func TestRun_EmptyExportIsAnError(t *testing.T) {
	if _, err := run(t, newStore(t), `[]`, ""); err == nil {
		t.Fatal("Run(`[]`) = nil error: an empty export must never be reported as a successful import")
	}
}

func TestRun_ExportWithNoMessagesAtAllIsAnError(t *testing.T) {
	if _, err := run(t, newStore(t), `[{"ts":1.0}]`, ""); err == nil {
		t.Fatal("Run(export with no parseable time) = nil error, want an error")
	}
}

// TestRun_RefusesATalkerThatIsNotInTheExport keeps a typo from looking like a
// successful empty analysis: the CLI takes a conversation id the user cannot
// easily read off the export, so a wrong value must fail loudly.
func TestRun_RefusesATalkerThatIsNotInTheExport(t *testing.T) {
	export := makeExport([]testRow{{tsSec: 1792166000, talker: "wxid_peer_b", content: "hi", key: "s1"}})
	_, err := run(t, newStore(t), export, "wxid_typo")
	if err == nil {
		t.Fatal("Run(unknown talker) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "wxid_typo") {
		t.Errorf("error = %v, want it to name the talker that was not found", err)
	}
}

func TestRun_EmptyTalkerImportsEveryConversation(t *testing.T) {
	export := makeExport([]testRow{
		{tsSec: 1792166000, talker: "c_one", content: "a", key: "s1"},
		{tsSec: 1792166001, talker: "c_two", content: "b", key: "s2"},
		{tsSec: 1792166002, talker: "c_one", content: "c", key: "s3"},
	})
	report, err := run(t, newStore(t), export, "")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(report.Talkers) != 2 || report.Talkers[0] != "c_one" || report.Talkers[1] != "c_two" {
		t.Errorf("Talkers = %v, want [c_one c_two]", report.Talkers)
	}
	if report.Inserted != 3 {
		t.Errorf("Inserted = %d, want 3", report.Inserted)
	}
}

func TestRun_OneTalkerOfManyIsScoped(t *testing.T) {
	export := makeExport([]testRow{
		{tsSec: 1792166000, talker: "c_one", content: "a", key: "s1"},
		{tsSec: 1792166001, talker: "c_two", content: "b", key: "s2"},
	})
	s := newStore(t)
	report, err := run(t, s, export, "c_two")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Inserted != 1 || len(report.Talkers) != 1 || report.Talkers[0] != "c_two" {
		t.Errorf("scoped import = %+v, want exactly c_two inserted", report)
	}
	if count, _ := s.CountMessages(context.Background(), "c_one"); count != 0 {
		t.Errorf("c_one has %d stored messages, want 0: the scope was ignored", count)
	}
}

// TestRun_ExhaustedWindowOnAnEmptyTableIsAnError covers a watermark that
// outlived its messages: the table is empty, so there is nothing to point a
// watermark at, and reporting "nothing new" would hide a wiped database.
func TestRun_ExhaustedWindowOnAnEmptyTableIsAnError(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.SaveIngestState(ctx, testSource, "c", 1792169000000, "s9"); err != nil {
		t.Fatalf("SaveIngestState() error: %v", err)
	}
	export := makeExport([]testRow{{tsSec: 1792166000, talker: "c", content: "hi", key: "s1"}})
	if _, err := run(t, s, export, ""); err == nil {
		t.Fatal("Run() = nil error for a watermark pointing past an empty table, want an error")
	}
}

// TestSaveIngestState_NeverMovesBackwards guards the rewind: import an old
// export after a newer one and the watermark must hold its position, or the
// next rescan re-reads history that is already stored.
func TestSaveIngestState_NeverMovesBackwards(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	newer := makeExport([]testRow{{tsSec: 1792169000, talker: "c", content: "new", key: "s1"}})
	if _, err := run(t, s, newer, ""); err != nil {
		t.Fatalf("newer Run() error: %v", err)
	}

	older := makeExport([]testRow{{tsSec: 1792160000, talker: "c", content: "old", key: "s0"}})
	if _, err := run(t, s, older, ""); err != nil {
		t.Fatalf("older Run() error: %v", err)
	}

	got, ok, err := s.IngestState(ctx, testSource, "c")
	if err != nil {
		t.Fatalf("IngestState() error: %v", err)
	}
	if !ok {
		t.Fatal("IngestState() reports no watermark after two imports")
	}
	if want := int64(1792169000000); got != want {
		t.Errorf("watermark = %d, want %d: an older import must not rewind it", got, want)
	}
}
