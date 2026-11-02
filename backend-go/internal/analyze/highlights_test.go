package analyze_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/model"
	"lover-instance/backend-go/internal/store"
)

func TestHighlightsAnalyzer_Empty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn, _ := store.DSN(filepath.Join(dir, "test.db"))
	s, _ := store.Open(dsn)
	defer s.Close()
	_ = s.Migrate(ctx)

	ha := &analyze.HighlightsAnalyzer{}
	if err := ha.Run(ctx, s, analyze.Options{Talker: "wxid_nobody"}, nil); err != nil {
		t.Fatalf("expected nil error for empty talker, got: %v", err)
	}

	highlights, err := s.GetHighlights(ctx, "wxid_nobody")
	if err != nil {
		t.Fatalf("get highlights: %v", err)
	}
	if len(highlights) != 0 {
		t.Fatalf("expected 0 highlights for empty store, got %d", len(highlights))
	}
}

func TestHighlightsAnalyzer_Calculation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn, _ := store.DSN(filepath.Join(dir, "test.db"))
	s, _ := store.Open(dsn)
	defer s.Close()
	_ = s.Migrate(ctx)

	// Inject messages over 3 consecutive days: 2024-05-20, 2024-05-21, 2024-05-22
	// 2024-05-20: 10 msgs (2 late night)
	// 2024-05-21: 20 msgs (6 late night 01:00)
	// 2024-05-22: 5 msgs
	loc := time.FixedZone("CST", 8*3600)
	d1, _ := time.ParseInLocation("2006-01-02 15:04:05", "2024-05-20 14:00:00", loc)
	d2Late, _ := time.ParseInLocation("2006-01-02 15:04:05", "2024-05-21 01:30:00", loc)
	d3, _ := time.ParseInLocation("2006-01-02 15:04:05", "2024-05-22 18:00:00", loc)

	msgs := make([]model.CanonicalMessage, 0)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, model.CanonicalMessage{
			UniqueKey: fmt.Sprintf("k1_%d", i),
			TimestampMs: d1.UnixMilli() + int64(i*1000),
			Talker: "wxid_lover", Sender: "wxid_lover",
			IsSelf: i%2 == 0, Type: 1, Content: "hello",
		})
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, model.CanonicalMessage{
			UniqueKey: fmt.Sprintf("k2_%d", i),
			TimestampMs: d2Late.UnixMilli() + int64(i*1000),
			Talker: "wxid_lover", Sender: "wxid_lover",
			IsSelf: i%2 == 0, Type: 1, Content: "late hello",
		})
	}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, model.CanonicalMessage{
			UniqueKey: fmt.Sprintf("k3_%d", i),
			TimestampMs: d3.UnixMilli() + int64(i*1000),
			Talker: "wxid_lover", Sender: "wxid_lover",
			IsSelf: i%2 == 0, Type: 1, Content: "bye",
		})
	}

	if _, err := s.UpsertMessages(ctx, "test", msgs); err != nil {
		t.Fatalf("upsert messages: %v", err)
	}

	ha := &analyze.HighlightsAnalyzer{}
	if err := ha.Run(ctx, s, analyze.Options{Talker: "wxid_lover"}, nil); err != nil {
		t.Fatalf("run highlights analyzer: %v", err)
	}

	highlights, err := s.GetHighlights(ctx, "wxid_lover")
	if err != nil {
		t.Fatalf("get highlights: %v", err)
	}

	if len(highlights) < 3 {
		t.Fatalf("expected at least 3 highlights (origin, streak, peak), got %d", len(highlights))
	}

	var hasOrigin, hasStreak, hasPeak, hasLateNight bool
	for _, h := range highlights {
		switch h.Kind {
		case "origin":
			hasOrigin = true
			if h.EventDate != "2024-05-20" {
				t.Errorf("origin date expected 2024-05-20, got %s", h.EventDate)
			}
		case "streak":
			hasStreak = true
			if h.MetricValue != 3 {
				t.Errorf("expected 3 days streak, got %v", h.MetricValue)
			}
		case "peak_day":
			hasPeak = true
			if h.EventDate == "2024-05-21" && h.MetricValue != 20 {
				t.Errorf("expected peak day 20 msgs, got %v", h.MetricValue)
			}
		case "late_night":
			hasLateNight = true
			if h.EventDate == "2024-05-21" && h.MetricValue != 20 {
				t.Errorf("expected late night 20 msgs, got %v", h.MetricValue)
			}
		}
	}

	if !hasOrigin {
		t.Error("missing origin highlight")
	}
	if !hasStreak {
		t.Error("missing streak highlight")
	}
	if !hasPeak {
		t.Error("missing peak highlight")
	}
	if !hasLateNight {
		t.Error("missing late night highlight")
	}
}
