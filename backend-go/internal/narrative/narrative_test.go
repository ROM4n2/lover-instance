package narrative

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// sampleInput is a fully populated aggregate. It deliberately uses an
// eight-key emotion map so that removing the digest's key sort turns the
// "stable across calls" assertion red with near certainty.
func sampleInput() Input {
	return Input{
		Days:            30,
		Messages:        1000,
		SelfMessages:    520,
		PartnerMessages: 480,
		RangeStart:      "2026-08-15",
		RangeEnd:        "2026-09-14",
		TopWordsSelf:    []string{"早安", "晚安", "吃饭"},
		TopWordsPartner: []string{"好啊", "哈哈"},
		TopExpressions:  []string{"[表情]", "[捂脸]"},
		EmotionShares: map[string]float64{
			"好": 0.10, "乐": 0.20, "哀": 0.05, "怒": 0.02,
			"惧": 0.01, "恶": 0.03, "惊": 0.04, "neutral": 0.55,
		},
		TrendPoints: []TrendPoint{
			{Day: "2026-08-15", Mean: 0.3796374, StdDev: 0.1234, Count: 12},
			{Day: "2026-08-16", Mean: -0.2000, StdDev: 0.0500, Count: 9},
		},
		MonthlyTop: map[string][]string{
			"2026-08": {"早安", "晚安"},
			"2026-09": {"吃饭", "哈哈"},
		},
		SentimentShares: map[string]float64{
			"positive": 0.5, "negative": 0.25, "neutral": 0.25,
		},
	}
}

// TestInput_FieldWhitelist pins the field set of Input. Aggregates only means
// aggregates only: an added field must be whitelisted on purpose, and the
// identity/body names below are asserted absent so nobody can just add one to
// the whitelist to make this pass.
func TestInput_FieldWhitelist(t *testing.T) {
	allowed := map[string]bool{
		"Days":            true,
		"Messages":        true,
		"SelfMessages":    true,
		"PartnerMessages": true,
		"RangeStart":      true,
		"RangeEnd":        true,
		"TopWordsSelf":    true,
		"TopWordsPartner": true,
		"TopExpressions":  true,
		"EmotionShares":   true,
		"TrendPoints":     true,
		"MonthlyTop":      true,
		"SentimentShares": true,
	}

	typ := reflect.TypeOf(Input{})
	seen := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if !allowed[name] {
			t.Errorf("Input has unexpected field %q; aggregates only — add it to the whitelist deliberately if it really carries no message text", name)
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("Input is missing expected field %q", name)
		}
	}

	// These names must never appear in the whitelist, no matter how the
	// struct is reworked: each one would carry a message body or an account
	// identity into the prompt.
	forbidden := []string{"Content", "Talker", "Sender", "Wxid", "Chat", "Body", "Text", "Message"}
	for _, name := range forbidden {
		if allowed[name] {
			t.Errorf("the whitelist contains %q: message text and identity must never reach the narrator", name)
		}
	}
}

// TestDigest_StableAndSensitive proves three things at once: the digest is
// caller-independent, it does not depend on map iteration order, and it reacts
// to every meaningful change.
func TestDigest_StableAndSensitive(t *testing.T) {
	base := sampleInput()
	const model, version = "llama3", "1"

	if base.Digest(model, version) != base.Digest(model, version) {
		t.Fatal("Digest is not stable across calls on the same input; map key ordering is missing")
	}

	// Same content, keys inserted in reverse: same digest.
	reordered := sampleInput()
	reordered.EmotionShares = map[string]float64{
		"neutral": 0.55, "惊": 0.04, "恶": 0.03, "惧": 0.01,
		"怒": 0.02, "哀": 0.05, "乐": 0.20, "好": 0.10,
	}
	if base.Digest(model, version) != reordered.Digest(model, version) {
		t.Error("Digest depends on map insertion order; map fields must be serialised in key order")
	}
	reorderedMonthly := sampleInput()
	reorderedMonthly.MonthlyTop = map[string][]string{
		"2026-09": {"吃饭", "哈哈"},
		"2026-08": {"早安", "晚安"},
	}
	if base.Digest(model, version) != reorderedMonthly.Digest(model, version) {
		t.Error("Digest depends on MonthlyTop insertion order")
	}

	// Different model or prompt version must change the digest.
	if base.Digest("other-model", version) == base.Digest(model, version) {
		t.Error("Digest ignores the model name")
	}
	if base.Digest(model, "2") == base.Digest(model, version) {
		t.Error("Digest ignores the prompt version")
	}

	// A single changed field (including one word inside MonthlyTop) changes it.
	changed := sampleInput()
	changed.Messages++
	if changed.Digest(model, version) == base.Digest(model, version) {
		t.Error("Digest ignores a changed count")
	}
	changedMonth := sampleInput()
	changedMonth.MonthlyTop["2026-08"] = []string{"早安", "再见"}
	if changedMonth.Digest(model, version) == base.Digest(model, version) {
		t.Error("Digest ignores a changed word inside MonthlyTop")
	}
}

// TestRenderPrompt_NoRawFloatsNoParagraphs is the "prompt cannot carry prose"
// guard. It proves: every number is printed to a fixed number of decimals (no
// raw float leaks), supplied words do appear (so the assertion is actually
// exercising the renderer), the template never emits a blank line, and the
// prompt opens with the fixed header. It does NOT prove the supplied words are
// not message text — that is the type-level job of the field whitelist above.
func TestRenderPrompt_NoRawFloatsNoParagraphs(t *testing.T) {
	in := sampleInput()
	in.EmotionShares = map[string]float64{"好": 0.1234, "neutral": 0.876543}
	in.TrendPoints = []TrendPoint{{Day: "2026-08-15", Mean: 0.3796374, StdDev: 0.1234, Count: 12}}

	prompt := RenderPrompt(in)

	// 1) Every decimal token is calibrated to one or two places.
	decimal := regexp.MustCompile(`-?\d+\.\d+`)
	for _, num := range decimal.FindAllString(prompt, -1) {
		frac := num[strings.IndexByte(num, '.')+1:]
		if len(frac) != 1 && len(frac) != 2 {
			t.Errorf("number %q in the prompt has %d decimals; shares render to one and means to two, never a raw float", num, len(frac))
		}
	}

	// 2) The calibrated values are actually present.
	for _, want := range []string{"12.3%", "87.7%", "0.38", "0.12"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing calibrated value %q", want)
		}
	}

	// 3) No raw float leaked.
	for _, bad := range []string{"0.1234", "0.3796374", "0.876543"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("prompt leaks a raw float %q", bad)
		}
	}

	// 4) Supplied tokens are rendered (the renderer does surface the input).
	for _, want := range []string{"早安", "[表情]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt should mention the supplied token %q", want)
		}
	}

	// 5) The template never emits a blank line, so an injected multi-paragraph
	// body would have nowhere to hide without introducing one.
	if strings.Contains(prompt, "\n\n") {
		t.Error("prompt contains a blank line; the aggregate template never emits one")
	}

	// 6) The header is fixed.
	if !strings.HasPrefix(prompt, "你是一名只做描述的报告助手") {
		t.Errorf("prompt does not start with the fixed header: %q", firstLine(prompt))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
