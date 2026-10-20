package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/narrative"
	"lover-instance/backend-go/internal/store"
)

// fakeNarrator records what it was handed and returns a fixed reply or error.
// It is the seam's reason for existing: the report can be exercised without a
// model, and the test can look at exactly what crossed the boundary.
type fakeNarrator struct {
	text  string
	err   error
	calls int
	got   narrative.Input
}

func (f *fakeNarrator) Narrate(_ context.Context, in narrative.Input) (string, error) {
	f.calls++
	f.got = in
	return f.text, f.err
}

// collectWith runs Collect with an injected narrator.
func collectWith(t *testing.T, s *store.Store, narrator Narrator) View {
	t.Helper()
	view, err := Collect(context.Background(), s, testTalker, store.BehaviorModelVersion, testModelVersion, narrator)
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	return view
}

func unconfigured(t *testing.T) View {
	t.Helper()
	s := newStore(t)
	seedConversation(t, s)
	return collectWith(t, s, nil)
}

// TestNarrative_NilNarratorSaysItIsUnconfigured keeps the section honest when
// no model was named: it is present and explains itself rather than leaving a
// gap the reader has to interpret.
func TestNarrative_NilNarratorSaysItIsUnconfigured(t *testing.T) {
	view := unconfigured(t)

	if view.Narrative != "" {
		t.Errorf("Narrative = %q, want empty when no narrator is configured", view.Narrative)
	}
	if !strings.Contains(view.NarrativeNote, "未配置") {
		t.Errorf("NarrativeNote = %q, want it to say the narrator is unconfigured", view.NarrativeNote)
	}
	html := render(t, view)
	if !strings.Contains(html, "这段时间") {
		t.Error("the narrative section is missing")
	}
}

// TestNarrative_AppearsInTheRenderedReport is the positive path: the paragraph
// the narrator returned must be in the document, escaped by html/template.
func TestNarrative_AppearsInTheRenderedReport(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	fake := &fakeNarrator{text: "这段时间的统计显示双方消息量接近。"}
	view := collectWith(t, s, fake)

	if fake.calls != 1 {
		t.Fatalf("narrator called %d times, want 1", fake.calls)
	}
	if view.Narrative != fake.text {
		t.Errorf("Narrative = %q, want the narrator's text", view.Narrative)
	}
	if view.NarrativeNote != "" {
		t.Errorf("NarrativeNote = %q, want empty on success", view.NarrativeNote)
	}
	if html := render(t, view); !strings.Contains(html, fake.text) {
		t.Error("the narration did not reach the rendered report")
	}
}

// TestNarrative_FailureDegradesWithoutLeakingTheError is the "降级不致命" rule
// plus the privacy guard: the report is still produced, the note names the
// cause, and the raw error — which here carries a path — never reaches the
// document.
func TestNarrative_FailureDegradesWithoutLeakingTheError(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	fake := &fakeNarrator{err: errors.New("dial tcp: open C:\\secret\\path\\model.bin: refused")}
	view := collectWith(t, s, fake)

	if view.Narrative != "" {
		t.Errorf("Narrative = %q, want empty on failure", view.Narrative)
	}
	if view.NarrativeNote == "" {
		t.Fatal("NarrativeNote is empty on failure; the section would be blank")
	}
	html := render(t, view)
	if strings.Contains(html, "secret") || strings.Contains(html, "C:\\") {
		t.Error("the raw error (with a filesystem path) leaked into the report")
	}
	if !strings.Contains(html, view.NarrativeNote) {
		t.Error("the degradation reason is not in the report")
	}
}

// TestNarrative_ExternalRefsStayEmpty checks the self-contained property in all
// three narration states, not just the happy one.
func TestNarrative_ExternalRefsStayEmpty(t *testing.T) {
	cases := []struct {
		name string
		make func(*testing.T) View
	}{
		{"unconfigured", unconfigured},
		{"success", func(t *testing.T) View {
			s := newStore(t)
			seedConversation(t, s)
			return collectWith(t, s, &fakeNarrator{text: "一段描述。"})
		}},
		{"failure", func(t *testing.T) View {
			s := newStore(t)
			seedConversation(t, s)
			return collectWith(t, s, &fakeNarrator{err: errors.New("boom")})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if refs := ExternalRefs([]byte(render(t, tc.make(t)))); len(refs) != 0 {
				t.Errorf("the report reaches the network: %v", refs)
			}
		})
	}
}

// TestNarrative_InputCarriesOnlyAggregates is the privacy boundary as an
// assertion: whatever crosses into the model is inspected, and neither a
// conversation key nor a body marker may be in it.
func TestNarrative_InputCarriesOnlyAggregates(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	// A message whose body must never reach the model.
	if _, err := s.DB().Exec(`INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper','leak',?, ?, 'wxid_peer', 0, 1, 0, 'MUST-NOT-LEAK', 'h')`, testBaseMs+300_000, testTalker); err != nil {
		t.Fatalf("insert marker message: %v", err)
	}

	fake := &fakeNarrator{text: "ok"}
	view := collectWith(t, s, fake)

	encoded, err := json.Marshal(fake.got)
	if err != nil {
		t.Fatalf("encode the received input: %v", err)
	}
	if strings.Contains(string(encoded), "wxid_") {
		t.Errorf("the input carries a conversation key: %s", encoded)
	}
	if strings.Contains(string(encoded), view.Meta.TalkerHash) {
		t.Errorf("the input carries the conversation fingerprint, which the model has no use for: %s", encoded)
	}
	if strings.Contains(string(encoded), "MUST-NOT-LEAK") {
		t.Errorf("a message body reached the narrator input: %s", encoded)
	}
	if fake.got.Messages == 0 || fake.got.RangeStart == "" {
		t.Errorf("the input is not populated with aggregates: %+v", fake.got)
	}
}

// TestNarrative_InputCarriesNoWords pins the decision to keep vocabulary out of
// the narrator's input.
//
// The first real run over this conversation's own export produced a paragraph
// naming two people — the third most frequent token in its word cloud is a
// personal name, 1,016 times, and no NER stands between that cloud and the model.
// The report is a file meant to be shared, so a name the sender never typed in
// the prompt would travel with it. Numbers only.
func TestNarrative_InputCarriesNoWords(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	if _, err := s.DB().Exec(`INSERT INTO word_freq_v2 (talker, period, is_self, model_version, word, cnt)
VALUES (?, '2026-08', 0, ?, '会出现在词云里的人名', 9)`, testTalker, testModelVersion); err != nil {
		t.Fatalf("insert a monthly word row: %v", err)
	}

	fake := &fakeNarrator{text: "ok"}
	view := collectWith(t, s, fake)
	if len(view.Words.Partner) == 0 && len(view.Monthly) == 0 {
		t.Fatal("the fixture has no words, so this test proves nothing")
	}
	if len(fake.got.TopWordsSelf) != 0 || len(fake.got.TopWordsPartner) != 0 ||
		len(fake.got.TopExpressions) != 0 || len(fake.got.MonthlyTop) != 0 {
		t.Errorf("the narrator received vocabulary: %+v", fake.got)
	}
}

// TestMonthly_WithDataRendersTheMonthAndWord is the positive path for the
// monthly chart: a month's words show up in the payload the chart reads.
func TestMonthly_WithDataRendersTheMonthAndWord(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	if _, err := s.DB().Exec(`INSERT INTO word_freq_v2 (talker, period, is_self, model_version, word, cnt)
VALUES (?, '2026-08', 0, ?, '月度专属词', 7)`, testTalker, testModelVersion); err != nil {
		t.Fatalf("insert monthly word row: %v", err)
	}

	view := collectWith(t, s, nil)
	if len(view.Monthly) == 0 {
		t.Fatal("Monthly is empty despite a monthly row")
	}
	html := render(t, view)
	if !strings.Contains(html, `id="monthly"`) {
		t.Error("the monthly chart container is missing")
	}
	if !strings.Contains(html, "2026-08") || !strings.Contains(html, "月度专属词") {
		t.Error("the month and its word are not in the rendered payload")
	}
}

// TestMonthly_WithoutDataExplainsTheRescan pins the pre-rescan state: months
// scored before monthly periods existed have no rows, and the section explains
// that rather than drawing an empty chart that reads as "no words that month".
func TestMonthly_WithoutDataExplainsTheRescan(t *testing.T) {
	view := unconfigured(t)
	if len(view.Monthly) != 0 {
		t.Fatalf("Monthly = %+v, want none for the seed fixture", view.Monthly)
	}
	html := render(t, view)
	if !strings.Contains(html, "重扫") {
		t.Error("the report does not explain that a rescan is needed for the monthly view")
	}
	if strings.Contains(html, `id="monthly"`) {
		t.Error("an empty monthly chart container was rendered instead of the explanation")
	}
}
