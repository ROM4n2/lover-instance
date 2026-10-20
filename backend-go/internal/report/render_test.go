package report

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func render(t *testing.T, view View) string {
	t.Helper()
	var out bytes.Buffer
	if err := Render(&out, view); err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	return out.String()
}

func renderedView(t *testing.T) string {
	t.Helper()
	s := newStore(t)
	seedConversation(t, s)
	return render(t, collect(t, s))
}

// TestRender_IsSelfContained is the ADR-0003 property: the file is opened with
// no network, so it must not ask for anything.
//
// The assertion on the raw document is deliberately paired with the
// ExternalRefs result. ExternalRefs returning empty is only meaningful if the
// document does contain URLs somewhere — otherwise it would pass on any
// empty or truncated output and prove nothing.
func TestRender_IsSelfContained(t *testing.T) {
	html := renderedView(t)

	if !strings.HasPrefix(html, "<!DOCTYPE html>") {
		t.Fatalf("output does not start with a doctype: %.60q", html)
	}
	if !strings.Contains(html, "https://") {
		t.Fatal("the vendored chart library carries no URLs, so an empty ExternalRefs result cannot distinguish a stripped document from a missing one")
	}
	if refs := ExternalRefs([]byte(html)); len(refs) != 0 {
		t.Errorf("the report reaches the network in %d places:\n%s", len(refs), strings.Join(refs, "\n"))
	}
	// The strip must be surgical: both vendor blocks and the data block are
	// still present in what the reader opens.
	for _, want := range []string{`data-vendor="echarts"`, `data-vendor="echarts-wordcloud"`, `id="report-data"`} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered document is missing %s", want)
		}
	}
}

func TestRender_DrawsEveryChartContainer(t *testing.T) {
	html := renderedView(t)

	for _, id := range []string{"heat", "wcSelf", "wcPartner", "emoSelf", "emoPartner", "radar", "emoBar", "trend", "types"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("no container for the %q chart", id)
		}
	}
	// A container without the library that fills it renders as an empty box,
	// which is the defect the word cloud test in collect_test.go exists for.
	if !strings.Contains(html, "echarts.init") {
		t.Error("the document carries no chart initialisation")
	}
}

// TestRender_ShowsTheNumbersWithoutJavaScript keeps the honest part of the
// report independent of the decorative part: the disclaimer and the headline
// counts are in the markup, not drawn by the chart code.
func TestRender_ShowsTheNumbersWithoutJavaScript(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)
	html := render(t, view)

	if !strings.Contains(html, "非关系诊断") || !strings.Contains(html, "征得") {
		t.Error("the disclaimer is not in the static markup")
	}
	total := strconv.Itoa(view.Summary.SelfMsgs + view.Summary.PartnerMsgs)
	if !strings.Contains(html, ">"+total+"<") {
		t.Errorf("the total message count %s is not in the static markup", total)
	}
	if !strings.Contains(html, view.Meta.TalkerHash) {
		t.Error("the conversation fingerprint is not in the static markup")
	}
	// The design requires the report to state the resolution the latencies
	// were measured at. Without it "16.0 秒" reads as a precise measurement
	// rather than a second-granularity one.
	if !strings.Contains(html, "同秒内的回复记 0 秒") {
		t.Error("the report does not state the reply-latency resolution")
	}
	if !strings.Contains(html, "按相邻两条消息计算") {
		t.Error("the report does not say what a reply latency is measured between")
	}
}

// TestRender_CarriesNoConversationKey. The fingerprint replaced the key in the
// view; this checks the template did not reintroduce it by another route.
func TestRender_CarriesNoConversationKey(t *testing.T) {
	html := renderedView(t)

	if strings.Contains(html, testTalker) {
		t.Error("the rendered report contains the conversation key")
	}
}

// TestRender_DataCannotEscapeItsScriptElement drives the injection through the
// one field a message can put arbitrary text into: a token that reached the
// word cloud.
func TestRender_DataCannotEscapeItsScriptElement(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)
	view.Words.Self = []WordCount{{Word: `</script><script>alert(1)</script>`, Count: 3}}

	html := render(t, view)

	if strings.Contains(html, `</script><script>`) {
		t.Fatal("a token closed the data element and opened a new script element")
	}
	// Positive evidence, not just the absence of the bad shape: the token must
	// be in the document, in its \\u003c form. Without this the test would also
	// pass on a renderer that dropped the word entirely.
	if !strings.Contains(html, `\u003c/script\u003e`) {
		t.Error("the token was dropped or left unescaped; it should appear as \\u003c/script\\u003e")
	}
}

// TestRender_IsByteStable. Two runs over the same data must produce the same
// file. The view holds maps, and Go randomises map iteration, so an
// unguarded range in the collector or an unsorted field would make the
// output differ between runs for no reason.
func TestRender_IsByteStable(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)

	first := render(t, view)
	for i := 0; i < 5; i++ {
		if again := render(t, view); again != first {
			t.Fatalf("run %d differs from the first render", i+2)
		}
	}
}

// barWidthMatches picks up every paired-bar width the template writes.
var barWidthMatches = regexp.MustCompile(`<i style="width:(-?[0-9.]+)%;`)

// TestRender_BarsStayInsideTheirStrip. The turn balance figures are ratios, not
// shares, and the first run over a real export — where one side wrote 27% more
// than the other — rendered a 126.8% width and a partner segment of minus 26.8%
// beside it. A width outside the strip is an arithmetic error, not a style
// choice, and it is invisible to tests that only compare the numbers.
func TestRender_BarsStayInsideTheirStrip(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	view := collect(t, s)
	view.Summary.SelfChars = 213_154
	view.Summary.PartnerChars = 168_134
	view.Summary.TurnBalanceCharRatio = 213_154.0 / 168_134.0
	view.Summary.TurnBalanceMsgRatio = 17_919.0 / 14_705.0

	html := render(t, view)
	matches := barWidthMatches.FindAllStringSubmatch(html, -1)
	if len(matches) < 4 {
		t.Fatalf("found only %d bar segments; the paired bars are not being rendered", len(matches))
	}
	for _, match := range matches {
		width, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			t.Fatalf("unparsable width %q", match[1])
		}
		if width < 0 || width > 100 {
			t.Errorf("a bar segment is %v%% wide, which is outside its strip: %q", width, match[0])
		}
	}
	// The turn balance ratio has to be legible as a ratio somewhere, or the
	// bar alone would hide which side the number came from.
	if !strings.Contains(html, "1.27 : 1") {
		t.Error("the ratio itself is not stated")
	}
	if !strings.Contains(html, "1.22 : 1") {
		t.Errorf("the message-count ratio is missing; the design asks for both")
	}
}

// TestExternalRefs_ReportsWhatItStrips is the reverse verification: the check
// must fail on a document that reaches out, and it must do so for each shape
// of reference, not only the one that happens to be easiest to spot.
func TestExternalRefs_ReportsWhatItStrips(t *testing.T) {
	base := renderedView(t)

	cases := []struct {
		name     string
		injected string
	}{
		{"absolute image", `<img src="https://collector.example/pixel.png">`},
		{"protocol relative script", `<script src="//cdn.example/lib.js"></script>`},
		{"inlined stylesheet import", `<style>@import url(//fonts.example/x.css);</style>`},
		{"inline css url", `<div style="background:url(https://tracker.example/a.png)"></div>`},
		{"inline fetch", `<script>fetch("https://api.example/post")</script>`},
		{"image constructor", `<script>new Image().src=("https://beacon.example/hit")</script>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The injection point is anchored on the vendor script tag alone, not
			// on the blank line before it: a checkout with core.autocrlf turns that
			// blank line into \r\n\r\n, and a test that silently stops finding its
			// own injection point is worse than a failing one.
			broken := strings.Replace(base, "<script data-vendor", tc.injected+"<script data-vendor", 1)
			if broken == base {
				t.Fatal("the injection point was not found, so this case tested nothing")
			}
			if refs := ExternalRefs([]byte(broken)); len(refs) == 0 {
				t.Error("ExternalRefs passed a document that reaches the network")
			}
		})
	}
}

// TestExternalRefs_LeavesTheDocumentAlone states the negative control: a clean
// document must not be reported, or the gate would be impossible to satisfy.
func TestExternalRefs_LeavesTheDocumentAlone(t *testing.T) {
	if refs := ExternalRefs([]byte(renderedView(t))); len(refs) != 0 {
		t.Fatalf("a clean report was reported as external: %v", refs)
	}
}

func TestDurationAndPercentFormatting(t *testing.T) {
	if got := percent(0.1234); got != "12.3%" {
		t.Errorf("percent(0.1234) = %q", got)
	}
	if got := remainder(0.75); got != "25.0%" {
		t.Errorf("remainder(0.75) = %q", got)
	}
	if got := duration(0); got != "—" {
		t.Errorf("duration(0) = %q, want a dash rather than 0 毫秒", got)
	}
	if got := duration(900); got != "900 毫秒" {
		t.Errorf("duration(900) = %q", got)
	}
	if got := duration(90_000); got != "1.5 分" {
		t.Errorf("duration(90000) = %q", got)
	}
	if got := dateOf(0); got != "—" {
		t.Errorf("dateOf(0) = %q, want a dash rather than 1970", got)
	}
}
