// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import "container/heap"

// NodeCapacityCache wraps a min-heap of candidate nodes with O(1) lookup by
// node ID. After each placement the caller calls RecordPlacement to decrement
// the node's resources and increment its load, restoring the heap invariant in
// O(log n) via heap.Fix — instead of rebuilding the entire heap from scratch.
type NodeCapacityCache struct {
	// heapData is the min-heap ordered by load score.
	heapData nodeSelectionHeap
	// lookupMap maps node ID to its heap entry for O(1) access.
	lookupMap map[string]*nodeHeapEntry
	// minimumCPU is the per-instance CPU requirement used to evict exhausted nodes.
	minimumCPU int64
	// minimumMemory is the per-instance memory requirement used to evict exhausted nodes.
	minimumMemory int64
}

// NewNodeCapacityCache builds a persistent cache from the given candidates,
// filtering out nodes that lack sufficient resources. The cache can then be
// reused across multiple placements within a single service batch.
func NewNodeCapacityCache(candidates []candidateNode, loadPerNode map[string]int, requiredCPU int64, requiredMemory int64) *NodeCapacityCache {
	heapData, lookupMap := buildNodeHeap(candidates, loadPerNode, requiredCPU, requiredMemory)
	return &NodeCapacityCache{
		heapData:      heapData,
		lookupMap:     lookupMap,
		minimumCPU:    requiredCPU,
		minimumMemory: requiredMemory,
	}
}

// BestNode returns the node ID with the lowest load score, or empty string if
// no candidates remain in the cache.
func (cache *NodeCapacityCache) BestNode() string {
	if cache.heapData.Len() == 0 {
		return ""
	}
	return cache.heapData[0].node.id
}

// BestNodeInZones returns the node ID with the lowest load score whose zone is
// in the acceptable set. Falls back to BestNode if no zone-filtered candidate
// is found. This avoids rebuilding the heap when zone spread is active.
func (cache *NodeCapacityCache) BestNodeInZones(acceptableZones map[string]bool) string {
	if cache.heapData.Len() == 0 {
		return ""
	}
	if len(acceptableZones) == 0 {
		return cache.heapData[0].node.id
	}
	for _, entry := range cache.heapData {
		zone := entry.node.zone
		if zone == "" {
			zone = "_default"
		}
		if acceptableZones[zone] {
			return entry.node.id
		}
	}
	return cache.heapData[0].node.id
}

// RecordPlacement decrements the selected node's available resources,
// increments its load score, and restores the heap invariant. If the node no
// longer has sufficient resources for another instance, it is removed from the
// heap entirely.
func (cache *NodeCapacityCache) RecordPlacement(nodeID string, cpuCost int64, memoryCost int64) {
	entry, exists := cache.lookupMap[nodeID]
	if !exists || entry.heapIndex < 0 {
		return
	}
	entry.node.availCPU -= cpuCost
	entry.node.availMemory -= memoryCost
	entry.loadScore++

	exhausted := (cache.minimumCPU > 0 && entry.node.availCPU < cache.minimumCPU) ||
		(cache.minimumMemory > 0 && entry.node.availMemory < cache.minimumMemory)

	if exhausted {
		heap.Remove(&cache.heapData, entry.heapIndex)
	} else {
		heap.Fix(&cache.heapData, entry.heapIndex)
	}
}

// Len returns the number of candidate nodes still in the cache.
func (cache *NodeCapacityCache) Len() int {
	return cache.heapData.Len()
}
