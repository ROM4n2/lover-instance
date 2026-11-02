package analyze_test

import (
	"testing"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/nlp"
)

func TestDefaultRegistry_Plan(t *testing.T) {
	c, err := nlp.NewClient("http://127.0.0.1:8000")
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}
	r := analyze.DefaultRegistry(c)

	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("unexpected error planning default registry: %v", err)
	}

	if len(plan) != 3 {
		t.Fatalf("expected 3 analyzers, got %d", len(plan))
	}

	if plan[0].ID() != "behavior" {
		t.Errorf("step 0: expected behavior, got %s", plan[0].ID())
	}
	ids := map[string]bool{plan[1].ID(): true, plan[2].ID(): true}
	if !ids["sentiment"] || !ids["highlights"] {
		t.Errorf("expected sentiment and highlights in plan, got %s and %s", plan[1].ID(), plan[2].ID())
	}
}

func TestDefaultRegistry_WithoutClient(t *testing.T) {
	r := analyze.DefaultRegistry(nil)
	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("expected 2 analyzers without client, got %d", len(plan))
	}
	if plan[0].ID() != "behavior" {
		t.Errorf("expected behavior, got %s", plan[0].ID())
	}
}
