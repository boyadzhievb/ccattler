package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

type stubController struct {
	name     string
	prefixes []string
}

func (stub *stubController) Name() string      { return stub.name }
func (stub *stubController) Watch() []string    { return stub.prefixes }
func (stub *stubController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	return nil, nil
}

func TestTopologicalSortLinearChain(t *testing.T) {
	names := []string{"C", "B", "A"}
	edges := map[string][]string{
		"B": {"A"},
		"C": {"B"},
	}

	result := topologicalSort(names, edges)
	if len(result.cyclicControllers) != 0 {
		t.Fatalf("unexpected cycles: %v", result.cyclicControllers)
	}

	positionOf := make(map[string]int)
	for index, name := range result.sortedControllers {
		positionOf[name] = index
	}
	if positionOf["A"] >= positionOf["B"] {
		t.Error("A must come before B")
	}
	if positionOf["B"] >= positionOf["C"] {
		t.Error("B must come before C")
	}
}

func TestTopologicalSortDetectsCycle(t *testing.T) {
	names := []string{"A", "B", "C"}
	edges := map[string][]string{
		"A": {"C"},
		"B": {"A"},
		"C": {"B"},
	}

	result := topologicalSort(names, edges)
	if len(result.cyclicControllers) == 0 {
		t.Fatal("expected cycle detection, got none")
	}
	if len(result.cyclicControllers) != 3 {
		t.Errorf("expected 3 cyclic nodes, got %d: %v", len(result.cyclicControllers), result.cyclicControllers)
	}
}

func TestTopologicalSortNoDependencies(t *testing.T) {
	names := []string{"A", "B", "C"}
	edges := map[string][]string{}

	result := topologicalSort(names, edges)
	if len(result.sortedControllers) != 3 {
		t.Fatalf("expected 3 sorted controllers, got %d", len(result.sortedControllers))
	}
	if len(result.cyclicControllers) != 0 {
		t.Errorf("unexpected cycles: %v", result.cyclicControllers)
	}
}

func TestTopologicalSortDiamondDependency(t *testing.T) {
	names := []string{"D", "B", "C", "A"}
	edges := map[string][]string{
		"B": {"A"},
		"C": {"A"},
		"D": {"B", "C"},
	}

	result := topologicalSort(names, edges)
	if len(result.cyclicControllers) != 0 {
		t.Fatalf("unexpected cycles: %v", result.cyclicControllers)
	}

	positionOf := make(map[string]int)
	for index, name := range result.sortedControllers {
		positionOf[name] = index
	}
	if positionOf["A"] >= positionOf["B"] || positionOf["A"] >= positionOf["C"] {
		t.Error("A must come before B and C")
	}
	if positionOf["B"] >= positionOf["D"] || positionOf["C"] >= positionOf["D"] {
		t.Error("B and C must come before D")
	}
}

func TestSortControllersByDependencyRespectsPrefixes(t *testing.T) {
	controllers := []Controller{
		&stubController{name: "endpoint", prefixes: []string{"observed/instance/", "observed/node/", "desired/service/", "endpoint/"}},
		&stubController{name: "instance", prefixes: []string{"effective/service/", "observed/instance/"}},
		&stubController{name: "intent-resolver", prefixes: []string{"desired/service/", "intent/autoscaler/", "effective/service/"}},
	}

	sorted, cycles := SortControllersByDependency(controllers)
	if len(cycles) != 0 {
		t.Fatalf("unexpected cycles: %v", cycles)
	}

	positionOf := make(map[string]int)
	for index, controller := range sorted {
		positionOf[controller.Name()] = index
	}

	if positionOf["intent-resolver"] >= positionOf["instance"] {
		t.Error("intent-resolver writes effective/ which instance reads — must come first")
	}
	if positionOf["instance"] >= positionOf["endpoint"] {
		t.Error("instance writes observed/instance/ which endpoint reads — must come first")
	}
}

func TestSortControllersByDependencyHandlesUnknownController(t *testing.T) {
	controllers := []Controller{
		&stubController{name: "custom-plugin", prefixes: []string{"desired/service/"}},
	}

	sorted, cycles := SortControllersByDependency(controllers)
	if len(cycles) != 0 {
		t.Fatalf("unexpected cycles: %v", cycles)
	}
	if len(sorted) != 1 {
		t.Fatalf("expected 1 controller, got %d", len(sorted))
	}
}
