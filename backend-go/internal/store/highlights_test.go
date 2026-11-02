package store

import (
	"context"
	"testing"
)

func TestStore_HighlightsCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir(), "test.db")
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	events := []HighlightEvent{
		{
			Talker:      "wxid_test",
			Kind:        "origin",
			EventDate:   "2024-01-01",
			Title:       "启程之日",
			Description: "初次相遇",
			MetricValue: 100,
			ExtraJSON:   `{"days": 100}`,
		},
		{
			Talker:      "wxid_test",
			Kind:        "streak",
			EventDate:   "2024-02-01~2024-02-14",
			Title:       "心动连击",
			Description: "连续 14 天",
			MetricValue: 14,
			ExtraJSON:   `{}`,
		},
	}

	if err := s.SaveHighlights(ctx, "wxid_test", events); err != nil {
		t.Fatalf("save highlights: %v", err)
	}

	got, err := s.GetHighlights(ctx, "wxid_test")
	if err != nil {
		t.Fatalf("get highlights: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 highlights, got %d", len(got))
	}
	if got[0].Kind != "origin" || got[1].Kind != "streak" {
		t.Errorf("unexpected kinds: %+v", got)
	}

	// Idempotent overwrite
	events[0].Title = "启程之日(更新)"
	if err := s.SaveHighlights(ctx, "wxid_test", events); err != nil {
		t.Fatalf("re-save highlights: %v", err)
	}
	got2, err := s.GetHighlights(ctx, "wxid_test")
	if err != nil {
		t.Fatalf("get highlights after update: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("expected 2 highlights, got %d", len(got2))
	}
	if got2[0].Title != "启程之日(更新)" {
		t.Errorf("expected updated title, got %s", got2[0].Title)
	}
}
