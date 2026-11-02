package analyze_test

import (
	"context"
	"testing"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/store"
)

type dummyAnalyzer struct {
	id   string
	name string
	deps []string
}

func (d *dummyAnalyzer) ID() string            { return d.id }
func (d *dummyAnalyzer) Name() string          { return d.name }
func (d *dummyAnalyzer) Dependencies() []string { return d.deps }
func (d *dummyAnalyzer) Run(ctx context.Context, s *store.Store, opts analyze.Options, onProgress func(done, total int)) error {
	return nil
}

func TestRegistry_Register(t *testing.T) {
	r := analyze.NewRegistry()

	if err := r.Register(nil); err == nil {
		t.Fatal("expected error registering nil analyzer")
	}

	a := &dummyAnalyzer{id: "test", name: "Test Analyzer"}
	if err := r.Register(a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := r.Register(a); err == nil {
		t.Fatal("expected error registering duplicate analyzer")
	}

	empty := &dummyAnalyzer{id: "", name: "Empty"}
	if err := r.Register(empty); err == nil {
		t.Fatal("expected error registering analyzer with empty ID")
	}
}

func TestRegistry_Plan_Linear(t *testing.T) {
	r := analyze.NewRegistry()
	_ = r.Register(&dummyAnalyzer{id: "c", name: "C", deps: []string{"b"}})
	_ = r.Register(&dummyAnalyzer{id: "a", name: "A", deps: nil})
	_ = r.Register(&dummyAnalyzer{id: "b", name: "B", deps: []string{"a"}})

	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan) != 3 {
		t.Fatalf("expected 3 analyzers in plan, got %d", len(plan))
	}
	expected := []string{"a", "b", "c"}
	for i, exp := range expected {
		if plan[i].ID() != exp {
			t.Errorf("step %d: expected %s, got %s", i, exp, plan[i].ID())
		}
	}
}

func TestRegistry_Plan_MissingDependency(t *testing.T) {
	r := analyze.NewRegistry()
	_ = r.Register(&dummyAnalyzer{id: "b", name: "B", deps: []string{"missing"}})

	_, err := r.Plan()
	if err == nil {
		t.Fatal("expected error for missing dependency, got nil")
	}
}

func TestRegistry_Plan_CycleDetection(t *testing.T) {
	r := analyze.NewRegistry()
	_ = r.Register(&dummyAnalyzer{id: "a", name: "A", deps: []string{"b"}})
	_ = r.Register(&dummyAnalyzer{id: "b", name: "B", deps: []string{"c"}})
	_ = r.Register(&dummyAnalyzer{id: "c", name: "C", deps: []string{"a"}})

	_, err := r.Plan()
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}
