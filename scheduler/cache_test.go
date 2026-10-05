// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import "testing"

func TestNodeCapacityCacheBestNode(testHandle *testing.T) {
	candidates := []candidateNode{
		{id: "node-1", availCPU: 4000, availMemory: 8192},
		{id: "node-2", availCPU: 4000, availMemory: 8192},
		{id: "node-3", availCPU: 4000, availMemory: 8192},
	}
	loadPerNode := map[string]int{"node-1": 5, "node-2": 2, "node-3": 8}
	cache := NewNodeCapacityCache(candidates, loadPerNode, 500, 1024)

	bestNode := cache.BestNode()
	if bestNode != "node-2" {
		testHandle.Errorf("expected node-2 (load=2), got %s", bestNode)
	}
}

func TestNodeCapacityCacheRecordPlacement(testHandle *testing.T) {
	candidates := []candidateNode{
		{id: "node-1", availCPU: 4000, availMemory: 8192},
		{id: "node-2", availCPU: 4000, availMemory: 8192},
	}
	loadPerNode := map[string]int{"node-1": 0, "node-2": 0}
	cache := NewNodeCapacityCache(candidates, loadPerNode, 500, 1024)

	// Both at load 0, node-1 wins by ID tiebreak.
	firstBest := cache.BestNode()
	if firstBest != "node-1" {
		testHandle.Fatalf("expected node-1, got %s", firstBest)
	}

	// Place on node-1 — its load goes to 1, node-2 (load 0) should now be best.
	cache.RecordPlacement("node-1", 500, 1024)
	secondBest := cache.BestNode()
	if secondBest != "node-2" {
		testHandle.Errorf("after placement on node-1, expected node-2, got %s", secondBest)
	}
}

func TestNodeCapacityCacheEvictsExhaustedNode(testHandle *testing.T) {
	candidates := []candidateNode{
		{id: "node-1", availCPU: 600, availMemory: 8192},
		{id: "node-2", availCPU: 4000, availMemory: 8192},
	}
	loadPerNode := map[string]int{}
	cache := NewNodeCapacityCache(candidates, loadPerNode, 500, 1024)

	if cache.Len() != 2 {
		testHandle.Fatalf("expected 2 nodes, got %d", cache.Len())
	}

	// Place on node-1 (600 - 500 = 100 CPU left, below the 500 minimum).
	cache.RecordPlacement("node-1", 500, 1024)
	if cache.Len() != 1 {
		testHandle.Errorf("expected node-1 evicted, cache len %d", cache.Len())
	}
	if cache.BestNode() != "node-2" {
		testHandle.Errorf("expected node-2 remaining, got %s", cache.BestNode())
	}
}

func TestNodeCapacityCacheEmptyReturnsEmpty(testHandle *testing.T) {
	cache := NewNodeCapacityCache(nil, nil, 0, 0)
	if cache.BestNode() != "" {
		testHandle.Errorf("expected empty, got %s", cache.BestNode())
	}
	if cache.Len() != 0 {
		testHandle.Errorf("expected len 0, got %d", cache.Len())
	}
}

func TestNodeCapacityCacheBestNodeInZones(testHandle *testing.T) {
	candidates := []candidateNode{
		{id: "node-1", availCPU: 4000, availMemory: 8192, zone: "us-east-1a"},
		{id: "node-2", availCPU: 4000, availMemory: 8192, zone: "us-east-1b"},
		{id: "node-3", availCPU: 4000, availMemory: 8192, zone: "us-east-1a"},
	}
	loadPerNode := map[string]int{"node-1": 0, "node-2": 0, "node-3": 0}
	cache := NewNodeCapacityCache(candidates, loadPerNode, 500, 1024)

	// Restrict to zone b — only node-2 qualifies.
	zoneB := map[string]bool{"us-east-1b": true}
	bestInB := cache.BestNodeInZones(zoneB)
	if bestInB != "node-2" {
		testHandle.Errorf("expected node-2 in zone b, got %s", bestInB)
	}

	// Empty zone set falls back to overall best.
	bestAny := cache.BestNodeInZones(nil)
	if bestAny != "node-1" {
		testHandle.Errorf("expected node-1 (overall best), got %s", bestAny)
	}
}

func TestNodeCapacityCacheMultiplePlacements(testHandle *testing.T) {
	candidates := []candidateNode{
		{id: "node-1", availCPU: 2000, availMemory: 8192},
		{id: "node-2", availCPU: 2000, availMemory: 8192},
		{id: "node-3", availCPU: 2000, availMemory: 8192},
	}
	loadPerNode := map[string]int{}
	cache := NewNodeCapacityCache(candidates, loadPerNode, 500, 1024)

	// Place 4 instances per node (2000 / 500 = 4), total 12.
	placedCount := 0
	for cache.Len() > 0 && placedCount < 20 {
		nodeID := cache.BestNode()
		cache.RecordPlacement(nodeID, 500, 1024)
		placedCount++
	}
	if placedCount != 12 {
		testHandle.Errorf("expected 12 placements (3 nodes × 4), got %d", placedCount)
	}
	if cache.Len() != 0 {
		testHandle.Errorf("expected all nodes exhausted, got %d remaining", cache.Len())
	}
}
