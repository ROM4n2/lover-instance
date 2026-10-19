package report

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/*.js
var assetsFS embed.FS

//go:embed templates/*.tmpl
var templateFS embed.FS

// vendorScriptMatches returns the complete <script> element carrying a
// data-vendor attribute, which is how the inlined chart libraries are
// identified when deciding whether the document reaches outside itself.
var vendorScriptMatches = regexp.MustCompile(`(?s)<script[^>]*\sdata-vendor="[^"]*"[^>]*>.*?</script>`)

// dataBlockMatches returns the element holding the report payload. It is
// application/json and never executed, so it is not a request either — but a
// token inside it could be a URL someone pasted into the conversation, and
// treating that as an external reference would be a false alarm that makes the
// real check untrustworthy.
var dataBlockMatches = regexp.MustCompile(`(?s)<script[^>]*\sid="report-data"[^>]*>.*?</script>`)

// externalRefPatterns are the shapes that make a browser fetch something. The
// protocol-relative form is included because it is served over whatever scheme
// the page used, so it reaches the network without ever spelling "http".
var externalRefPatterns = []string{"http://", "https://", `="//`, `='//`, `url(//`, `@import`, "fetch(", "XMLHttpRequest", "new Image"}

// Render writes the self-contained report.
func Render(w io.Writer, view View) error {
	echarts, err := assetsFS.ReadFile("assets/echarts.min.js")
	if err != nil {
		return fmt.Errorf("report: read chart library: %w", err)
	}
	wordcloud, err := assetsFS.ReadFile("assets/echarts-wordcloud.min.js")
	if err != nil {
		return fmt.Errorf("report: read word cloud plugin: %w", err)
	}
	data, err := encodeReportData(view)
	if err != nil {
		return err
	}
	// The headline figures and the disclaimer are rendered server-side rather
	// than drawn by the chart code, so a reader whose browser blocks scripts
	// still gets the numbers and the caveats. The charts are the only part
	// that needs JavaScript.
	tmpl, err := template.New("report.html.tmpl").Funcs(template.FuncMap{
		"pct":   percent,
		"rest":  remainder,
		"split": splitOf,
		"num1":  oneDecimal,
		"num2":  twoDecimals,
		"date":  dateOf,
		"span":  duration,
		"add":   sum,
	}).ParseFS(templateFS, "templates/report.html.tmpl")
	if err != nil {
		return fmt.Errorf("report: parse template: %w", err)
	}

	payload := struct {
		ECharts   template.JS
		WordCloud template.JS
		Data      template.JS
		View      View
	}{
		ECharts:   template.JS(echarts),
		WordCloud: template.JS(wordcloud),
		Data:      data,
		View:      view,
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, payload); err != nil {
		return fmt.Errorf("report: execute template: %w", err)
	}
	if _, err := io.Copy(w, &buffer); err != nil {
		return fmt.Errorf("report: write output: %w", err)
	}
	return nil
}

// encodeReportData marshals the view for inlining into a script element.
//
// json.Marshal already escapes '<', '>' and '&', so the payload cannot close
// the script element or open a tag. The two Unicode line separators are
// escaped as well: they are legal inside a JSON string but were illegal in a
// JavaScript string literal before ES2019, and the payload is going into a
// document that may be opened by an old browser.
func encodeReportData(view View) (template.JS, error) {
	raw, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("report: encode view: %w", err)
	}
	safe := strings.NewReplacer("\u2028", `\u2028`, "\u2029", `\u2029`).Replace(string(raw))
	return template.JS(safe), nil
}

// ExternalRefs reports the places the rendered document would reach the
// network.
//
// It is deliberately not a substring count over the whole file. The inlined
// chart library contains a handful of URLs of its own — SVG namespace names
// and licence headers — so counting "https://" anywhere would report a
// constant six or so hits and could never pass, while saying nothing about
// whether the document actually fetches anything. The check instead removes
// the two known vendor scripts and the inert data block, then looks for the
// shapes that cause a request. Anything it returns is a real external
// dependency, and an empty result is the property the report must have.
func ExternalRefs(html []byte) []string {
	stripped := vendorScriptMatches.ReplaceAll(html, nil)
	stripped = dataBlockMatches.ReplaceAll(stripped, nil)
	text := string(stripped)

	var found []string
	for _, pattern := range externalRefPatterns {
		for offset := 0; ; {
			index := strings.Index(text[offset:], pattern)
			if index < 0 {
				break
			}
			absolute := offset + index
			found = append(found, excerpt(text, absolute, pattern))
			offset = absolute + len(pattern)
		}
	}
	return found
}

// percent renders a share as a percentage. The template shows shares held as
// 0..1, and printing the raw float would read as a probability.
func percent(share float64) string {
	return strconv.FormatFloat(share*100, 'f', 1, 64) + "%"
}

// remainder is the other side of a two-way split, so the paired bars in the
// template always add up even when one share is rounded for display.
func remainder(share float64) string {
	return percent(1 - share)
}

// splitOf converts a one-side-against-the-other ratio into that side's share
// of the total. The turn balance figures are ratios — 1.27 means "1.27
// characters for every one of theirs" — and a bar needs a fraction of a whole,
// so r/(1+r) is what makes the two sides meet at 100% instead of overflowing
// the strip and giving its neighbour a negative width.
func splitOf(ratio float64) float64 {
	if ratio <= 0 || math.IsNaN(ratio) {
		return 0
	}
	if math.IsInf(ratio, 1) {
		return 1
	}
	return ratio / (1 + ratio)
}

// oneDecimal renders a small average, which is a count rather than a share.
// A non-finite value renders as a dash rather than as "NaN" in the report.
func oneDecimal(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	return strconv.FormatFloat(value, 'f', 1, 64)
}

// twoDecimals renders a ratio. "1.27 : 1" is how a reader says it aloud, and
// it is the figure the bar beside it is derived from.
func twoDecimals(ratio float64) string {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return "—"
	}
	return strconv.FormatFloat(ratio, 'f', 2, 64)
}

// sum adds two counts for the "total messages" card.
func sum(a, b int) int { return a + b }

// cst is the fixed offset every date in the report is rendered in. It is the
// same zone the daily buckets are cut in; using the reader's local zone would
// put the day boundary in a different place than the analysis did.
var cst = time.FixedZone("CST", 8*60*60)

// dateOf renders a millisecond timestamp as a calendar date in that zone.
// A zero timestamp means the conversation had no messages to span, and is
// shown as a dash rather than as 1970.
func dateOf(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).In(cst).Format("2006-01-02")
}

// duration renders a reply latency in the units a reader thinks in. The
// thresholds are coarse on purpose: the figure is a median over a noisy
// distribution and sub-second precision would imply an accuracy it does not
// have.
func duration(ms int64) string {
	switch {
	case ms <= 0:
		return "—"
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + " 毫秒"
	case ms < 60_000:
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " 秒"
	case ms < 3_600_000:
		return strconv.FormatFloat(float64(ms)/60_000, 'f', 1, 64) + " 分"
	default:
		return strconv.FormatFloat(float64(ms)/3_600_000, 'f', 1, 64) + " 小时"
	}
}

// excerpt renders a short window around a match so a failure names the line
// rather than just the pattern.
func excerpt(text string, at int, pattern string) string {
	start := at - 40
	if start < 0 {
		start = 0
	}
	end := at + len(pattern) + 40
	if end > len(text) {
		end = len(text)
	}
	return strings.TrimSpace(strings.ReplaceAll(text[start:end], "\n", " ")) + "  <<" + pattern
}
