package analyze

import (
	"fmt"
	"sort"
)

// Registry manages Analyzer registration and determines an execution order via DAG topological sort.
type Registry struct {
	analyzers map[string]Analyzer
	order     []string
}

// NewRegistry returns an empty Analyzer registry.
func NewRegistry() *Registry {
	return &Registry{
		analyzers: make(map[string]Analyzer),
		order:     make([]string, 0),
	}
}

// Register adds an Analyzer to the registry. It fails if an analyzer with the same ID is already registered.
func (r *Registry) Register(a Analyzer) error {
	if a == nil {
		return fmt.Errorf("analyze: cannot register nil analyzer")
	}
	id := a.ID()
	if id == "" {
		return fmt.Errorf("analyze: analyzer ID cannot be empty")
	}
	if _, exists := r.analyzers[id]; exists {
		return fmt.Errorf("analyze: analyzer %q is already registered", id)
	}
	r.analyzers[id] = a
	r.order = append(r.order, id)
	return nil
}

// Get returns the Analyzer registered under id, or nil if none exists.
func (r *Registry) Get(id string) Analyzer {
	return r.analyzers[id]
}

// Plan computes a valid execution sequence respecting all declared dependencies.
// It returns an error if any dependency is missing or if a cycle is detected.
func (r *Registry) Plan() ([]Analyzer, error) {
	inDegree := make(map[string]int, len(r.analyzers))
	dependents := make(map[string][]string, len(r.analyzers))

	for id := range r.analyzers {
		inDegree[id] = 0
	}

	for id, a := range r.analyzers {
		for _, dep := range a.Dependencies() {
			if _, exists := r.analyzers[dep]; !exists {
				return nil, fmt.Errorf("analyze: analyzer %q depends on missing analyzer %q", id, dep)
			}
			inDegree[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}

	// Queue nodes with in-degree 0 in registration order.
	queue := make([]string, 0, len(r.analyzers))
	for _, id := range r.order {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	result := make([]Analyzer, 0, len(r.analyzers))
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		result = append(result, r.analyzers[curr])

		// For deterministic order, sort dependents before queuing
		deps := dependents[curr]
		sort.Slice(deps, func(i, j int) bool {
			return deps[i] < deps[j]
		})

		for _, dep := range deps {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(result) != len(r.analyzers) {
		return nil, fmt.Errorf("analyze: cyclic dependency detected among registered analyzers")
	}

	return result, nil
}
