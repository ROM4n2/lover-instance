package report

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/store"
)

// secondVersion is the model version the M2 pass writes beside the lexicon.
// The name does not embed the lexicon's, which is what makes it eligible as an
// independent second opinion rather than a derivative of the first.
const secondVersion = "onnx-erlangshen-v1"

// seedSecondVersion scores the seeded four messages under the model version.
//
// The model produces no DUTIR labels — Task 3 of the M2 plan leaves `emotion`
// empty for it — so every emotion here is the empty string, which the store
// folds into the neutral bucket. That is the situation the report has to
// disclose rather than paper over.
func seedSecondVersion(t *testing.T, s *store.Store) {
	t.Helper()
	rows := []store.SentimentRow{
		{MsgID: 1, Polarity: 0.995, Emotion: "", Intensity: 0.995},
		{MsgID: 2, Polarity: -0.8, Emotion: "", Intensity: 0.8},
		{MsgID: 3, Polarity: -0.9, Emotion: "", Intensity: 0.9},
		{MsgID: 4, Polarity: 0.6, Emotion: "", Intensity: 0.6},
	}
	self := map[string]int{"今天": 1, "开心": 1}
	partner := map[string]int{"开心": 1, "难过": 1}
	if err := s.SaveScoredBlock(context.Background(), testTalker, secondVersion, store.AllPeriod, rows, self, partner); err != nil {
		t.Fatalf("SaveScoredBlock(%s) error: %v", secondVersion, err)
	}
}

// collectVersion collects the report with a chosen sentiment version, which is
// how the caller selects which version is "current".
func collectVersion(t *testing.T, s *store.Store, version string) View {
	t.Helper()
	view, err := Collect(context.Background(), s, testTalker, store.BehaviorModelVersion, version)
	if err != nil {
		t.Fatalf("Collect(%s) error: %v", version, err)
	}
	return view
}

// TestCollect_ComparisonAppearsWhenTwoVersionsExist is the point of the whole
// section: the reader has to see both versions and how far apart they are.
// The seed gives the lexicon 0.8 / 0.7 / −0.6 / 0.0 and the model
// 0.995 / −0.8 / −0.9 / 0.6, so two of the four messages change side and the
// disagreement rate is 2/4.
func TestCollect_ComparisonAppearsWhenTwoVersionsExist(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	seedSecondVersion(t, s)

	view := collectVersion(t, s, secondVersion)
	comparison := view.Comparison

	if !comparison.Present {
		t.Fatal("Comparison.Present = false with two scored versions; the section would be hidden")
	}
	if comparison.Current.Version != secondVersion || comparison.Other.Version != testModelVersion {
		t.Errorf("versions = %q vs %q, want the report's own version first and the other beside it: %+v",
			comparison.Current.Version, comparison.Other.Version, comparison)
	}

	// Current: +0.995 and +0.6 positive, −0.8 and −0.9 negative, nothing at zero.
	if comparison.Current.Positive != 2 || comparison.Current.Negative != 2 || comparison.Current.Neutral != 0 {
		t.Errorf("current split = %+v, want 2/2/0", comparison.Current)
	}
	if math.Abs(comparison.Current.PositiveRatio-0.5) > 1e-9 {
		t.Errorf("current positive ratio = %v, want 2/4", comparison.Current.PositiveRatio)
	}
	// Other (the lexicon): +0.8 and +0.7 positive, −0.6 negative, 0.0 neutral.
	if comparison.Other.Positive != 2 || comparison.Other.Negative != 1 || comparison.Other.Neutral != 1 {
		t.Errorf("other split = %+v, want 2/1/1", comparison.Other)
	}
	if math.Abs(comparison.Other.PositiveRatio-2.0/3.0) > 1e-9 {
		t.Errorf("other positive ratio = %v, want 2/3: the neutral message is not in the denominator", comparison.Other.PositiveRatio)
	}

	// Both passes scored all four messages; two flipped side (message 2 went
	// from +0.7 to −0.8, message 4 from 0.0 to +0.6).
	if comparison.BothScored != 4 || comparison.Differing != 2 {
		t.Errorf("disagreement = %d/%d, want 2 of 4", comparison.Differing, comparison.BothScored)
	}
	if math.Abs(comparison.DisagreementRate-0.5) > 1e-9 {
		t.Errorf("DisagreementRate = %v, want 0.5", comparison.DisagreementRate)
	}
}

func TestCollect_ComparisonHidesWhenOnlyOneVersionExists(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)

	if view := collect(t, s); view.Comparison.Present {
		t.Errorf("Comparison.Present = true with a single version; the template would compare it with nothing: %+v", view.Comparison)
	}
}

// TestCollect_EmotionSourceNamesTheVersionWithLabels covers the honest fallback:
// the model version has no DUTIR labels at all, so the radar has to be the
// lexicon's and the report has to say whose it is. Silently labelling the
// model's polarity as seven emotions is the failure ADR-0004 forbids.
func TestCollect_EmotionSourceNamesTheVersionWithLabels(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	seedSecondVersion(t, s)

	view := collectVersion(t, s, secondVersion)

	if view.EmotionSource != testModelVersion {
		t.Errorf("EmotionSource = %q, want %q: the current version has no DUTIR labels", view.EmotionSource, testModelVersion)
	}
	// And the seven categories really are read from that version: the lexicon
	// matched 乐 twice and 哀 once in the seed.
	if view.Emotion.Counts["乐"] != 2 || view.Emotion.Counts["哀"] != 1 {
		t.Errorf("Counts = %v, want the lexicon's 乐:2 哀:1", view.Emotion.Counts)
	}
	if view.Emotion.ScoredTotal != 4 {
		t.Errorf("ScoredTotal = %d, want the 4 messages the lexicon scored", view.Emotion.ScoredTotal)
	}
	// The lexicon section's polarity split has to come from the same version as
	// its seven categories, or the two would describe different runs.
	if math.Abs(view.Emotion.PositiveRatio-2.0/3.0) > 1e-9 {
		t.Errorf("Emotion.PositiveRatio = %v, want the lexicon's 2/3", view.Emotion.PositiveRatio)
	}
}

func TestCollect_EmotionSourceIsEmptyWhenNoVersionHasLabels(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	if _, err := s.DB().Exec(`UPDATE sentiment SET emotion = ''`); err != nil {
		t.Fatalf("clear emotion labels: %v", err)
	}

	view := collect(t, s)

	if view.EmotionSource != "" {
		t.Errorf("EmotionSource = %q, want empty: no version produced a DUTIR label", view.EmotionSource)
	}
	for i, share := range view.Emotion.Radar {
		if share != 0 {
			t.Errorf("Radar[%d] = %v, want 0: the seven categories must not be faked from polarity", i, share)
		}
	}
}

// TestCollect_TrendViewMirrorsTheStorePoints pins the mapping, especially the
// two counts the template branches on: without Drawable the "样本不足" branch is
// dead code, and without Thin the chart draws noise as a trend.
func TestCollect_TrendViewMirrorsTheStorePoints(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s) // one day, 4 scored messages, mean 0.225
	// 40 days later: two messages, too few to draw. 80 days later: three
	// messages, all negative, so the drawable range has two distinct ends.
	putDirect(t, s, "thin1", testBaseMs+40*dayMs, testModelVersion, 0.5, "乐")
	putDirect(t, s, "thin2", testBaseMs+40*dayMs, testModelVersion, 0.5, "乐")
	for i, key := range []string{"low1", "low2", "low3"} {
		putDirect(t, s, key, testBaseMs+80*dayMs+int64(i)*1000, testModelVersion, -0.5, "哀")
	}

	view := collect(t, s)
	trend := view.Trend

	if len(trend.Points) != 3 {
		t.Fatalf("points = %d (%+v), want 3 days with scores", len(trend.Points), trend.Points)
	}
	if trend.Drawable != 2 || trend.Thin != 1 {
		t.Errorf("Drawable/Thin = %d/%d, want 2/1: the 2-message day is not drawable", trend.Drawable, trend.Thin)
	}
	for _, point := range trend.Points {
		if want := point.Count >= 3; point.Drawable != want {
			t.Errorf("point %s has Count %d but Drawable %v", point.Day, point.Count, point.Drawable)
		}
	}
	if math.Abs(trend.MeanLow-(-0.5)) > 1e-9 {
		t.Errorf("MeanLow = %v, want -0.5 from the drawable low day", trend.MeanLow)
	}
	if math.Abs(trend.MeanHigh-0.225) > 1e-9 {
		t.Errorf("MeanHigh = %v, want 0.225 from the seeded day", trend.MeanHigh)
	}
}

func TestCollect_TrendViewIsZeroWhenNothingIsDrawable(t *testing.T) {
	s := newStore(t)
	putDirect(t, s, "only", testBaseMs, testModelVersion, 0.5, "乐")

	view := collect(t, s)

	if view.Trend.Drawable != 0 || view.Trend.Thin != 1 {
		t.Errorf("Drawable/Thin = %d/%d, want 0/1", view.Trend.Drawable, view.Trend.Thin)
	}
	if view.Trend.MeanLow != 0 || view.Trend.MeanHigh != 0 {
		t.Errorf("MeanLow/High = %v/%v, want 0/0 with no drawable point", view.Trend.MeanLow, view.Trend.MeanHigh)
	}
}

// dayMs is one day in milliseconds, so the trend test can place messages on
// separate calendar days without importing time into a test about counts.
const dayMs = int64(24 * 60 * 60 * 1000)

// putDirect inserts a text message and one score for it, returning its id.
// The seeder only provides four fixed messages, and the trend needs messages
// spread over calendar days.
func putDirect(t *testing.T, s *store.Store, key string, tsMs int64, version string, polarity float64, emotion string) int64 {
	t.Helper()
	result, err := s.DB().Exec(`
INSERT INTO messages (source, source_msg_id, ts, talker, sender, is_self, msg_type, sub_type, content, content_sha1)
VALUES ('chatlog-keeper', ?, ?, ?, 'wxid_self', 1, 1, 0, '', ?)`, key, tsMs, testTalker, "h-"+key)
	if err != nil {
		t.Fatalf("insert message %s: %v", key, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read the id of %s: %v", key, err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO sentiment (msg_id, model_version, polarity, emotion, intensity) VALUES (?,?,?,?,?)`,
		id, version, polarity, emotion, 0.0); err != nil {
		t.Fatalf("score message %s: %v", key, err)
	}
	return id
}

// twoVersionView is a report where both versions have scored the conversation.
func twoVersionView(t *testing.T) View {
	t.Helper()
	s := newStore(t)
	seedConversation(t, s)
	seedSecondVersion(t, s)
	return collectVersion(t, s, testVersionForRender)
}

// testVersionForRender names which version the two-version render tests treat as
// current. It is the model version on purpose: that is the case where the radar
// has to fall back to the lexicon and say so.
const testVersionForRender = secondVersion

// TestRender_ComparisonSectionFollowsPresence is the disclosure guard: with a
// single version there is nothing to compare and the word must not appear at
// all; with two, the disagreement rate must.
func TestRender_ComparisonSectionFollowsPresence(t *testing.T) {
	single := renderedView(t)
	if strings.Contains(single, "不一致") {
		t.Error("a single-version report mentions 不一致: there is nothing to compare it with")
	}

	withBoth := render(t, twoVersionView(t))
	if !strings.Contains(withBoth, "不一致") {
		t.Fatal("a two-version report does not show the disagreement rate, which is the only honest answer to what the model changed")
	}
	if !strings.Contains(withBoth, "50.0%") {
		t.Error("the disagreement rate 2/4 is not rendered")
	}
}

// TestRender_KeepsTheHonestyCaveats pins the three sentences that are easy to
// delete during a redesign and impossible to reconstruct from the numbers:
// SD is message spread rather than a confidence interval, thin windows are not
// drawn, and a low mean is partly the zero-polarity neutral messages.
func TestRender_KeepsTheHonestyCaveats(t *testing.T) {
	html := render(t, twoVersionView(t))

	// The SD sentence is asserted in full rather than by its keywords, because
	// the keywords also appear in the chart's own legend: a check on
	// "消息离散度" alone still passes after the sentence explaining what it
	// means has been deleted.
	for _, want := range []string{
		"阴影是 ±1 SD（消息离散度，不是置信区间）",
		"样本不足",
		"中性消息",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the report no longer says %q", want)
		}
	}
	if !strings.Contains(html, testModelVersion) {
		t.Error("the report does not name the version the seven emotions came from")
	}
}

// TestRender_TwoVersionsStaySelfContained runs the ADR-0003 check again over the
// payload with both versions present, because the comparison section is new
// markup and new markup is where an external request gets introduced.
func TestRender_TwoVersionsStaySelfContained(t *testing.T) {
	html := render(t, twoVersionView(t))

	if !strings.Contains(html, "https://") {
		t.Fatal("the vendored library carries no URLs, so an empty result would prove nothing")
	}
	if refs := ExternalRefs([]byte(html)); len(refs) != 0 {
		t.Errorf("the two-version report reaches the network in %d places:\n%s", len(refs), strings.Join(refs, "\n"))
	}
}

// TestRender_ComparisonBarsStayInsideTheirStrip extends the M1 width guard to
// the new bars. The comparison figures are shares already, so they must not be
// divided by anything on the way in — but a share is exactly what produced the
// 126.8% width once before, so the arithmetic is asserted rather than assumed.
func TestRender_ComparisonBarsStayInsideTheirStrip(t *testing.T) {
	html := render(t, twoVersionView(t))

	matches := barWidthMatches.FindAllStringSubmatch(html, -1)
	// Two paired bars existed before (turn balance, emotion); the comparison
	// adds one pair per version.
	if len(matches) < 8 {
		t.Fatalf("found %d bar segments, want at least 8: the comparison bars are not rendered", len(matches))
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
	// Two versions, two positive ratios: 50.0% (model) and 66.7% (lexicon).
	for _, want := range []string{"50.0%", "66.7%"} {
		if !strings.Contains(html, want) {
			t.Errorf("the report does not show the %s positive ratio", want)
		}
	}
}

// renderedTrendPoints reads the chart payload back out of the document, so the
// x-axis assertion is made against what the browser receives rather than
// against what the collector intended.
var trendPayload = regexp.MustCompile(`"Trend":\{"Points":\[(.*?)\]`)

// TestRender_TrendWithoutDrawablePointsShowsWordsInsteadOfAnEmptyChart keeps the
// "样本不足" branch alive. A chart element with nothing in it looks like a
// rendering bug; a sentence says what happened.
func TestRender_TrendWithoutDrawablePointsShowsWordsInsteadOfAnEmptyChart(t *testing.T) {
	s := newStore(t)
	seedConversation(t, s)
	// Two scores left, which is one short of the three a point needs.
	if _, err := s.DB().Exec(`DELETE FROM sentiment WHERE msg_id IN (3, 4)`); err != nil {
		t.Fatalf("drop two scores: %v", err)
	}
	view := collect(t, s)
	if view.Trend.Drawable != 0 {
		t.Fatalf("Trend.Drawable = %d with three scored messages, want 0", view.Trend.Drawable)
	}

	html := render(t, view)

	if strings.Contains(html, `id="trend"`) {
		t.Error("the report still emits an empty chart container when nothing is drawable")
	}
	if !strings.Contains(html, "样本不足") {
		t.Error("the report does not say the samples are too thin")
	}
}

// TestRender_TrendPayloadCarriesCalendarDays states the property the chart code
// depends on: every point carries its own day, so the axis can be built from
// calendar dates instead of array indices. Index-based axes draw day 1 and day
// 10 as neighbours.
func TestRender_TrendPayloadCarriesCalendarDays(t *testing.T) {
	html := render(t, twoVersionView(t))

	match := trendPayload.FindStringSubmatch(html)
	if match == nil {
		t.Fatal("the rendered data block carries no trend points")
	}
	if !regexp.MustCompile(`"Day":"\d{4}-\d{2}-\d{2}"`).MatchString(match[1]) {
		t.Errorf("trend points do not carry a YYYY-MM-DD day: %s", match[1])
	}
	// The chart builds its axis from Day and must fill the gaps itself.
	if !strings.Contains(html, "connectNulls") {
		t.Error("the chart does not state connectNulls, so missing days would be joined")
	}
}
