package scheduler

import (
	"container/heap"
	"testing"
)

func TestBuildNodeHeapSelectsLeastLoaded(t *testing.T) {
	candidates := []candidateNode{
		{id: "node-a", availCPU: 1000, availMemory: 512},
		{id: "node-b", availCPU: 1000, availMemory: 512},
		{id: "node-c", availCPU: 1000, availMemory: 512},
	}
	loadPerNode := map[string]int{"node-a": 5, "node-b": 2, "node-c": 8}

	heapData, _ := buildNodeHeap(candidates, loadPerNode, 0, 0)
	selected := selectFromHeap(&heapData)

	if selected != "node-b" {
		t.Errorf("expected node-b (load 2), got %s", selected)
	}
}

func TestBuildNodeHeapFiltersInsufficientResources(t *testing.T) {
	candidates := []candidateNode{
		{id: "node-a", availCPU: 100, availMemory: 512},
		{id: "node-b", availCPU: 500, availMemory: 512},
	}
	loadPerNode := map[string]int{"node-a": 0, "node-b": 0}

	heapData, _ := buildNodeHeap(candidates, loadPerNode, 200, 0)
	selected := selectFromHeap(&heapData)

	if selected != "node-b" {
		t.Errorf("expected node-b (only one with enough CPU), got %s", selected)
	}
}

func TestBuildNodeHeapEmptyWhenNoCapacity(t *testing.T) {
	candidates := []candidateNode{
		{id: "node-a", availCPU: 100, availMemory: 256},
	}
	loadPerNode := map[string]int{}

	heapData, _ := buildNodeHeap(candidates, loadPerNode, 500, 0)
	selected := selectFromHeap(&heapData)

	if selected != "" {
		t.Errorf("expected empty string when no capacity, got %s", selected)
	}
}

func TestBuildNodeHeapDeterministicTieBreaking(t *testing.T) {
	candidates := []candidateNode{
		{id: "node-c", availCPU: 1000, availMemory: 512},
		{id: "node-a", availCPU: 1000, availMemory: 512},
		{id: "node-b", availCPU: 1000, availMemory: 512},
	}
	loadPerNode := map[string]int{"node-a": 3, "node-b": 3, "node-c": 3}

	heapData, _ := buildNodeHeap(candidates, loadPerNode, 0, 0)
	selected := selectFromHeap(&heapData)

	// Tie-breaking prefers input order (preferRank), so node-c (index 0) wins.
	if selected != "node-c" {
		t.Errorf("expected node-c (first in input order), got %s", selected)
	}
}

func TestNodeHeapUpdateAfterPlacement(t *testing.T) {
	candidates := []candidateNode{
		{id: "node-a", availCPU: 1000, availMemory: 512},
		{id: "node-b", availCPU: 1000, availMemory: 512},
	}
	loadPerNode := map[string]int{"node-a": 1, "node-b": 3}

	heapData, lookupMap := buildNodeHeap(candidates, loadPerNode, 0, 0)

	first := selectFromHeap(&heapData)
	if first != "node-a" {
		t.Fatalf("expected first selection node-a, got %s", first)
	}

	entry := lookupMap["node-a"]
	entry.loadScore = 5
	heap.Fix(&heapData, entry.heapIndex)

	second := selectFromHeap(&heapData)
	if second != "node-b" {
		t.Errorf("expected node-b after updating node-a load, got %s", second)
	}
}
