package infra

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// SimulatorInfraProvider is an in-memory infrastructure provider for testing.
// It creates simulated nodes in the fact store without provisioning real machines.
type SimulatorInfraProvider struct {
	factStore     store.StateStore
	nodeCounter   atomic.Int64
	managedNodes  map[string]bool
	providerMutex sync.Mutex
}

// NewSimulatorInfraProvider creates a SimulatorInfraProvider backed by the given store.
func NewSimulatorInfraProvider(factStore store.StateStore) *SimulatorInfraProvider {
	return &SimulatorInfraProvider{
		factStore:    factStore,
		managedNodes: make(map[string]bool),
	}
}

// RequestNode creates a new simulated node in the fact store with default capacity.
func (simulatorInfraProvider *SimulatorInfraProvider) RequestNode(ctx context.Context) (string, error) {
	nodeNumber := simulatorInfraProvider.nodeCounter.Add(1)
	nodeID := fmt.Sprintf("auto-node-%d", nodeNumber)

	types.WriteNode(ctx, simulatorInfraProvider.factStore, types.Node{
		ID:              nodeID,
		State:           types.NodeAlive,
		CapacityCPU:     4000,
		CapacityMemory:  8192,
		AvailableCPU:    4000,
		AvailableMemory: 8192,
		Architecture:    "amd64",
	})

	simulatorInfraProvider.providerMutex.Lock()
	simulatorInfraProvider.managedNodes[nodeID] = true
	simulatorInfraProvider.providerMutex.Unlock()

	return nodeID, nil
}

// RemoveNode marks a simulated node as unreachable and removes it from tracking.
func (simulatorInfraProvider *SimulatorInfraProvider) RemoveNode(ctx context.Context, nodeID string) error {
	simulatorInfraProvider.factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeUnreachable)))

	simulatorInfraProvider.providerMutex.Lock()
	delete(simulatorInfraProvider.managedNodes, nodeID)
	simulatorInfraProvider.providerMutex.Unlock()

	return nil
}

// NodeCount returns the number of currently managed simulated nodes.
func (simulatorInfraProvider *SimulatorInfraProvider) NodeCount(_ context.Context) (int, error) {
	simulatorInfraProvider.providerMutex.Lock()
	defer simulatorInfraProvider.providerMutex.Unlock()
	return len(simulatorInfraProvider.managedNodes), nil
}
