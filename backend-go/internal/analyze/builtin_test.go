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

	if len(plan) != 2 {
		t.Fatalf("expected 2 analyzers, got %d", len(plan))
	}

	if plan[0].ID() != "behavior" {
		t.Errorf("step 0: expected behavior, got %s", plan[0].ID())
	}
	if plan[1].ID() != "sentiment" {
		t.Errorf("step 1: expected sentiment, got %s", plan[1].ID())
	}
}

func TestDefaultRegistry_WithoutClient(t *testing.T) {
	r := analyze.DefaultRegistry(nil)
	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 1 {
		t.Fatalf("expected 1 analyzer without client, got %d", len(plan))
	}
	if plan[0].ID() != "behavior" {
		t.Errorf("expected behavior, got %s", plan[0].ID())
	}
}
