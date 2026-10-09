// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	factStore       store.StateStore
	nodeCounter     atomic.Int64
	managedNodes    map[string]bool
	requestToNodeID map[string]string // maps requestID → nodeID for idempotency
	providerMutex   sync.Mutex
}

// NewSimulatorInfraProvider creates a SimulatorInfraProvider backed by the given store.
func NewSimulatorInfraProvider(factStore store.StateStore) *SimulatorInfraProvider {
	return &SimulatorInfraProvider{
		factStore:       factStore,
		managedNodes:    make(map[string]bool),
		requestToNodeID: make(map[string]string),
	}
}

// RequestNodeWithRequirements creates a new simulated node in the fact store
// matching the given requirements. If a node was already provisioned for the
// given requestID, the existing node ID is returned without creating a duplicate.
func (simulatorInfraProvider *SimulatorInfraProvider) RequestNodeWithRequirements(ctx context.Context, requestID string, requirements CapacityRequestRequirements) (string, error) {
	simulatorInfraProvider.providerMutex.Lock()
	if existingNodeID, alreadyProvisioned := simulatorInfraProvider.requestToNodeID[requestID]; alreadyProvisioned {
		simulatorInfraProvider.providerMutex.Unlock()
		return existingNodeID, nil
	}
	simulatorInfraProvider.providerMutex.Unlock()

	nodeNumber := simulatorInfraProvider.nodeCounter.Add(1)
	nodeID := fmt.Sprintf("auto-node-%d", nodeNumber)

	var capacityCPU int64 = types.DefaultSimulatedNodeCPU
	if requirements.CPU > capacityCPU {
		capacityCPU = requirements.CPU
	}
	var capacityMemory int64 = types.DefaultSimulatedNodeMemory
	if requirements.Memory > capacityMemory {
		capacityMemory = requirements.Memory
	}
	architecture := "amd64"
	if requirements.Architecture != "" {
		architecture = requirements.Architecture
	}

	if writeError := types.WriteNode(ctx, simulatorInfraProvider.factStore, types.Node{
		ID:              nodeID,
		State:           types.NodeAlive,
		CapacityCPU:     capacityCPU,
		CapacityMemory:  capacityMemory,
		AvailableCPU:    capacityCPU,
		AvailableMemory: capacityMemory,
		Architecture:    architecture,
	}); writeError != nil {
		return "", fmt.Errorf("failed to write simulated node %s: %w", nodeID, writeError)
	}

	simulatorInfraProvider.providerMutex.Lock()
	simulatorInfraProvider.managedNodes[nodeID] = true
	simulatorInfraProvider.requestToNodeID[requestID] = nodeID
	simulatorInfraProvider.providerMutex.Unlock()

	return nodeID, nil
}

// RemoveNode marks a simulated node as unreachable and removes it from tracking.
func (simulatorInfraProvider *SimulatorInfraProvider) RemoveNode(ctx context.Context, nodeID string) error {
	if _, putError := simulatorInfraProvider.factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeUnreachable))); putError != nil {
		return fmt.Errorf("failed to mark node %s unreachable: %w", nodeID, putError)
	}

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
