package network

import (
	"container/heap"
	"sync"
)

// backendLoad tracks the number of active (in-flight) connections for a backend.
type backendLoad struct {
	address          string
	activeConnections int64
	heapIndex        int
}

// backendHeap implements heap.Interface for least-connections selection.
type backendHeap []*backendLoad

func (backendHeapSlice backendHeap) Len() int { return len(backendHeapSlice) }

func (backendHeapSlice backendHeap) Less(indexA, indexB int) bool {
	return backendHeapSlice[indexA].activeConnections < backendHeapSlice[indexB].activeConnections
}

func (backendHeapSlice backendHeap) Swap(indexA, indexB int) {
	backendHeapSlice[indexA], backendHeapSlice[indexB] = backendHeapSlice[indexB], backendHeapSlice[indexA]
	backendHeapSlice[indexA].heapIndex = indexA
	backendHeapSlice[indexB].heapIndex = indexB
}

func (backendHeapSlice *backendHeap) Push(element interface{}) {
	entry := element.(*backendLoad)
	entry.heapIndex = len(*backendHeapSlice)
	*backendHeapSlice = append(*backendHeapSlice, entry)
}

func (backendHeapSlice *backendHeap) Pop() interface{} {
	oldSlice := *backendHeapSlice
	lastIndex := len(oldSlice) - 1
	entry := oldSlice[lastIndex]
	oldSlice[lastIndex] = nil
	*backendHeapSlice = oldSlice[:lastIndex]
	entry.heapIndex = -1
	return entry
}

// LeastConnectionsBalancer selects backends by choosing the one with the fewest
// active connections. It uses a min-heap for O(log n) selection and updates.
type LeastConnectionsBalancer struct {
	heapData  backendHeap
	lookupMap map[string]*backendLoad
	mutex     sync.Mutex
}

// NewLeastConnectionsBalancer creates a new least-connections load balancer.
func NewLeastConnectionsBalancer() *LeastConnectionsBalancer {
	return &LeastConnectionsBalancer{
		heapData:  make(backendHeap, 0),
		lookupMap: make(map[string]*backendLoad),
	}
}

// SelectBackend returns the backend address with the fewest active connections
// from the given candidate list. It also increments the active connection count
// for the selected backend. Returns empty string if no candidates are available.
func (balancer *LeastConnectionsBalancer) SelectBackend(candidateAddresses []string) string {
	if len(candidateAddresses) == 0 {
		return ""
	}

	balancer.mutex.Lock()
	defer balancer.mutex.Unlock()

	for _, candidateAddress := range candidateAddresses {
		if _, exists := balancer.lookupMap[candidateAddress]; !exists {
			entry := &backendLoad{address: candidateAddress}
			heap.Push(&balancer.heapData, entry)
			balancer.lookupMap[candidateAddress] = entry
		}
	}

	candidateSet := make(map[string]bool, len(candidateAddresses))
	for _, candidateAddress := range candidateAddresses {
		candidateSet[candidateAddress] = true
	}

	var bestEntry *backendLoad
	for _, entry := range balancer.heapData {
		if !candidateSet[entry.address] {
			continue
		}
		if bestEntry == nil || entry.activeConnections < bestEntry.activeConnections {
			bestEntry = entry
		}
	}

	if bestEntry == nil {
		return ""
	}

	bestEntry.activeConnections++
	heap.Fix(&balancer.heapData, bestEntry.heapIndex)
	return bestEntry.address
}

// ReleaseBackend decrements the active connection count for a backend after
// a request completes. Must be called when the response finishes.
func (balancer *LeastConnectionsBalancer) ReleaseBackend(backendAddress string) {
	balancer.mutex.Lock()
	defer balancer.mutex.Unlock()

	entry, exists := balancer.lookupMap[backendAddress]
	if !exists {
		return
	}

	if entry.activeConnections > 0 {
		entry.activeConnections--
		heap.Fix(&balancer.heapData, entry.heapIndex)
	}
}

// ActiveConnections returns the current connection count for a backend.
func (balancer *LeastConnectionsBalancer) ActiveConnections(backendAddress string) int64 {
	balancer.mutex.Lock()
	defer balancer.mutex.Unlock()

	entry, exists := balancer.lookupMap[backendAddress]
	if !exists {
		return 0
	}
	return entry.activeConnections
}
