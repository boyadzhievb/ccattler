// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// FuzzSchedulerReconcile exercises the scheduler's Reconcile method with
// fuzz-generated service names and variable node/instance counts to ensure
// the scheduler never panics and always produces valid placement changes.
func FuzzSchedulerReconcile(f *testing.F) {
	// Seed corpus with realistic scheduling scenarios.
	f.Add("web", uint8(3), uint8(2))
	f.Add("api", uint8(1), uint8(1))
	f.Add("database", uint8(5), uint8(3))
	f.Add("worker", uint8(10), uint8(10))
	f.Add("", uint8(0), uint8(0))
	f.Add("edge-proxy", uint8(1), uint8(5))
	f.Add("my-service/with-slash", uint8(2), uint8(3))

	f.Fuzz(func(t *testing.T, serviceName string, instanceCount uint8, nodeCount uint8) {
		// Clamp node and instance counts to a reasonable range (1-10) to avoid
		// degenerate inputs while still exploring interesting scheduling states.
		clampedNodeCount := clampToRange(nodeCount, 1, 10)
		clampedInstanceCount := clampToRange(instanceCount, 1, 10)

		syntheticFacts := buildSyntheticFactSlice(serviceName, clampedInstanceCount, clampedNodeCount)

		placementScheduler := NewScheduler()
		changes, reconcileError := placementScheduler.Reconcile(context.Background(), syntheticFacts)

		// The scheduler must never return an error for well-formed fact slices.
		if reconcileError != nil {
			t.Fatalf("Reconcile returned unexpected error: %v", reconcileError)
		}

		// Every returned change must be a placement put operation with a
		// non-empty key and non-empty value (the target node ID).
		for changeIndex, change := range changes {
			if change.Type != store.OpPut {
				t.Errorf("change[%d]: expected OpPut, got %d", changeIndex, change.Type)
			}
			if change.Key == "" {
				t.Errorf("change[%d]: placement key must not be empty", changeIndex)
			}
			if len(change.Value) == 0 {
				t.Errorf("change[%d]: placement value (node ID) must not be empty", changeIndex)
			}
		}
	})
}

// clampToRange constrains an unsigned byte value to the inclusive range
// [minimum, maximum]. Values below the minimum are raised; values above
// the maximum are lowered.
func clampToRange(value uint8, minimum uint8, maximum uint8) uint8 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// buildSyntheticFactSlice constructs a sorted slice of store.Fact entries that
// represent a minimal but valid cluster state: alive nodes with fixed resource
// capacity, pending instances belonging to the given service, and the desired
// service definition facts. The returned slice is sorted by key to match the
// store's scan ordering contract.
func buildSyntheticFactSlice(serviceName string, instanceCount uint8, nodeCount uint8) []store.Fact {
	var factEntries []store.Fact

	// Create alive nodes with fixed resource capacity.
	const fixedCPUCapacity = "4000"
	const fixedMemoryCapacity = "8192"

	for nodeIndex := uint8(0); nodeIndex < nodeCount; nodeIndex++ {
		nodeIdentifier := fmt.Sprintf("fuzz-node-%d", nodeIndex)
		factEntries = append(factEntries,
			store.Fact{Key: types.KeyObservedNodeState(nodeIdentifier), Value: []byte("alive")},
			store.Fact{Key: types.KeyObservedNodeAvailableCPU(nodeIdentifier), Value: []byte(fixedCPUCapacity)},
			store.Fact{Key: types.KeyObservedNodeAvailableMemory(nodeIdentifier), Value: []byte(fixedMemoryCapacity)},
		)
	}

	// Create pending instances for the service.
	for instanceIndex := uint8(0); instanceIndex < instanceCount; instanceIndex++ {
		instanceIdentifier := fmt.Sprintf("fuzz-inst-%d", instanceIndex)
		factEntries = append(factEntries,
			store.Fact{Key: types.KeyObservedInstanceState(instanceIdentifier), Value: []byte("pending")},
			store.Fact{Key: types.KeyObservedInstanceService(instanceIdentifier), Value: []byte(serviceName)},
		)
	}

	// Create desired service definition facts (marker, image, instance count).
	factEntries = append(factEntries,
		store.Fact{Key: types.KeyDesiredService(serviceName), Value: []byte("")},
		store.Fact{Key: types.KeyDesiredServiceImage(serviceName), Value: []byte("nginx:1.27")},
		store.Fact{Key: types.KeyDesiredServiceInstances(serviceName), Value: []byte(fmt.Sprintf("%d", instanceCount))},
		store.Fact{Key: types.KeyDesiredServiceResourcesCPU(serviceName), Value: []byte("500")},
		store.Fact{Key: types.KeyDesiredServiceResourcesMemory(serviceName), Value: []byte("512")},
	)

	// Sort by key to match the store's scan ordering contract, which the
	// FactsWithPrefix helper depends on for correct prefix filtering.
	sort.Slice(factEntries, func(i, j int) bool {
		return factEntries[i].Key < factEntries[j].Key
	})

	return factEntries
}
