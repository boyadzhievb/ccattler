// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import "container/heap"

// nodeHeapEntry pairs a candidate node with its scores for min-heap ordering.
// The heap selects, in O(log n), the node with the fewest instances of the
// service being placed (soft spread), then the least total load. The
// preferRank preserves the caller's preference ordering (lower = more preferred).
type nodeHeapEntry struct {
	node                 candidateNode // node is the candidate with its remaining resources.
	serviceInstanceCount int           // serviceInstanceCount is how many instances of the service being placed the node already hosts.
	loadScore            int           // loadScore is the total number of instances placed on the node.
	preferRank           int           // preferRank is the candidate's index in the caller's preference order.
	heapIndex            int           // heapIndex is the entry's position in the heap (-1 once removed).
}

// nodeSelectionHeap implements heap.Interface, ordering candidates by
// same-service instance count, then load score, then preference rank, then
// node ID (all ascending) so placement spreads a service and stays deterministic.
type nodeSelectionHeap []*nodeHeapEntry

func (heapSlice nodeSelectionHeap) Len() int { return len(heapSlice) }

func (heapSlice nodeSelectionHeap) Less(indexA, indexB int) bool {
	if heapSlice[indexA].serviceInstanceCount != heapSlice[indexB].serviceInstanceCount {
		return heapSlice[indexA].serviceInstanceCount < heapSlice[indexB].serviceInstanceCount
	}
	if heapSlice[indexA].loadScore != heapSlice[indexB].loadScore {
		return heapSlice[indexA].loadScore < heapSlice[indexB].loadScore
	}
	if heapSlice[indexA].preferRank != heapSlice[indexB].preferRank {
		return heapSlice[indexA].preferRank < heapSlice[indexB].preferRank
	}
	return heapSlice[indexA].node.id < heapSlice[indexB].node.id
}

func (heapSlice nodeSelectionHeap) Swap(indexA, indexB int) {
	heapSlice[indexA], heapSlice[indexB] = heapSlice[indexB], heapSlice[indexA]
	heapSlice[indexA].heapIndex = indexA
	heapSlice[indexB].heapIndex = indexB
}

func (heapSlice *nodeSelectionHeap) Push(element interface{}) {
	entry, _ := element.(*nodeHeapEntry)
	entry.heapIndex = len(*heapSlice)
	*heapSlice = append(*heapSlice, entry)
}

func (heapSlice *nodeSelectionHeap) Pop() interface{} {
	oldSlice := *heapSlice
	lastIndex := len(oldSlice) - 1
	entry := oldSlice[lastIndex]
	oldSlice[lastIndex] = nil
	*heapSlice = oldSlice[:lastIndex]
	entry.heapIndex = -1
	return entry
}

// buildNodeHeap creates a min-heap from candidates, filtering out nodes that
// lack sufficient resources. serviceInstancesPerNode (may be nil) holds the
// placed instances of the service being scheduled per node. Returns the heap
// and a map from node ID to entry for O(1) lookup when updating after placement.
func buildNodeHeap(candidates []candidateNode, loadPerNode map[string]int, serviceInstancesPerNode map[string]int, requiredCPU, requiredMemory int64) (nodeSelectionHeap, map[string]*nodeHeapEntry) {
	heapData := make(nodeSelectionHeap, 0, len(candidates))
	lookupMap := make(map[string]*nodeHeapEntry, len(candidates))

	for candidateIndex, candidate := range candidates {
		if requiredCPU > 0 && candidate.availCPU < requiredCPU {
			continue
		}
		if requiredMemory > 0 && candidate.availMemory < requiredMemory {
			continue
		}
		entry := &nodeHeapEntry{
			node:                 candidate,
			serviceInstanceCount: serviceInstancesPerNode[candidate.id],
			loadScore:            loadPerNode[candidate.id],
			preferRank:           candidateIndex,
		}
		heapData = append(heapData, entry)
		lookupMap[candidate.id] = entry
	}

	heap.Init(&heapData)
	return heapData, lookupMap
}

// selectFromHeap returns the node ID with the lowest load score, or empty
// string if the heap is empty.
func selectFromHeap(heapData *nodeSelectionHeap) string {
	if heapData.Len() == 0 {
		return ""
	}
	return (*heapData)[0].node.id
}
