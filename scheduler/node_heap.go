package scheduler

import "container/heap"

// nodeHeapEntry pairs a candidate node with its current load score for
// min-heap ordering. The heap selects the least-loaded node with sufficient
// resources in O(log n) instead of O(n). The preferRank preserves the
// caller's preference ordering (lower = more preferred).
type nodeHeapEntry struct {
	node       candidateNode
	loadScore  int
	preferRank int
	heapIndex  int
}

// nodeSelectionHeap implements heap.Interface, ordering candidates by load
// score (ascending). Ties are broken by node ID for determinism.
type nodeSelectionHeap []*nodeHeapEntry

func (heapSlice nodeSelectionHeap) Len() int { return len(heapSlice) }

func (heapSlice nodeSelectionHeap) Less(indexA, indexB int) bool {
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
	entry := element.(*nodeHeapEntry)
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
// lack sufficient resources. Returns the heap and a map from node ID to entry
// for O(1) lookup when updating after placement.
func buildNodeHeap(candidates []candidateNode, loadPerNode map[string]int, requiredCPU, requiredMemory int64) (nodeSelectionHeap, map[string]*nodeHeapEntry) {
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
			node:       candidate,
			loadScore:  loadPerNode[candidate.id],
			preferRank: candidateIndex,
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
