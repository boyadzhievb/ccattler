// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestPerNodePlacementLifecycle(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	ccaInput := `
service node-monitor {
    image monitor:1.0
    instances 1
    placement {
        mode per-node
    }
}
`
	if err := lang.Apply(ctx, factStore, ccaInput); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "3 placements for per-node service", func() bool {
		return countPlacementsForService(ctx, factStore, "node-monitor") == 3
	})

	waitFor(t, 5*time.Second, "each node has exactly 1 instance", func() bool {
		nodeCounts := placementNodeCounts(ctx, factStore, "node-monitor")
		return nodeCounts["node-1"] == 1 && nodeCounts["node-2"] == 1 && nodeCounts["node-3"] == 1
	})

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-4", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	waitFor(t, 5*time.Second, "4 placements after node-4 joins", func() bool {
		return countPlacementsForService(ctx, factStore, "node-monitor") == 4
	})

	waitFor(t, 5*time.Second, "node-4 has an instance", func() bool {
		nodeCounts := placementNodeCounts(ctx, factStore, "node-monitor")
		return nodeCounts["node-4"] == 1
	})

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-4", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	waitFor(t, 5*time.Second, "3 active instances after node-4 leaves", func() bool {
		return countPlacementsForService(ctx, factStore, "node-monitor") == 3
	})
}

func TestPerNodePlacementWithArchitectureFilter(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive, Architecture: "amd64",
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-2", State: types.NodeAlive, Architecture: "amd64",
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-3", State: types.NodeAlive, Architecture: "arm64",
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	ccaInput := `
service amd64-agent {
    image agent:2.0
    instances 1
    placement {
        mode per-node
        architecture amd64
    }
}
`
	if err := lang.Apply(ctx, factStore, ccaInput); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "2 placements for amd64-only per-node", func() bool {
		return countPlacementsForService(ctx, factStore, "amd64-agent") == 2
	})

	waitFor(t, 5*time.Second, "instances on amd64 nodes only", func() bool {
		nodeCounts := placementNodeCounts(ctx, factStore, "amd64-agent")
		return nodeCounts["node-1"] == 1 && nodeCounts["node-2"] == 1 && nodeCounts["node-3"] == 0
	})
}

func TestPerNodePlacementRespectsMaxPolicy(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2", "node-3", "node-4", "node-5"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	ccaInput := `
service capped-daemon {
    image daemon:1.0
    instances 1
    placement {
        mode per-node
    }
    scale {
        horizontal {
            max 3
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, ccaInput); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "exactly 3 placements (max policy caps)", func() bool {
		return countPlacementsForService(ctx, factStore, "capped-daemon") == 3
	})

	time.Sleep(500 * time.Millisecond)

	placementCount := countPlacementsForService(ctx, factStore, "capped-daemon")
	if placementCount != 3 {
		t.Fatalf("expected exactly 3 placements (policy max), got %d", placementCount)
	}
}

// countPlacementsForService returns the number of active placement facts for
// the given service. It filters out stopped/failed instances and those marked
// controller_stopped (pending agent processing).
func countPlacementsForService(ctx context.Context, factStore store.StateStore, serviceName string) int {
	placements, _ := factStore.Scan(ctx, types.ScanPlacements)
	count := 0
	for _, placement := range placements {
		instanceID := extractInstanceIDFromPlacementKey(placement.Key)
		serviceKey := types.KeyObservedInstanceService(instanceID)
		serviceFact, err := factStore.Get(ctx, serviceKey)
		if err != nil || string(serviceFact.Value) != serviceName {
			continue
		}
		stateKey := types.KeyObservedInstanceState(instanceID)
		stateFact, err := factStore.Get(ctx, stateKey)
		if err == nil && (string(stateFact.Value) == "stopped" || string(stateFact.Value) == "failed") {
			continue
		}
		stoppedKey := types.KeyDerivedInstanceControllerStopped(instanceID)
		stoppedFact, err := factStore.Get(ctx, stoppedKey)
		if err == nil && string(stoppedFact.Value) == "true" {
			continue
		}
		count++
	}
	return count
}

// placementNodeCounts returns a map of nodeID → active instance count for a
// given service's placements, excluding stopped/failed/controller_stopped instances.
func placementNodeCounts(ctx context.Context, factStore store.StateStore, serviceName string) map[string]int {
	nodeCounts := make(map[string]int)
	placements, _ := factStore.Scan(ctx, types.ScanPlacements)
	for _, placement := range placements {
		instanceID := extractInstanceIDFromPlacementKey(placement.Key)
		serviceKey := types.KeyObservedInstanceService(instanceID)
		serviceFact, err := factStore.Get(ctx, serviceKey)
		if err != nil || string(serviceFact.Value) != serviceName {
			continue
		}
		stateKey := types.KeyObservedInstanceState(instanceID)
		stateFact, err := factStore.Get(ctx, stateKey)
		if err == nil && (string(stateFact.Value) == "stopped" || string(stateFact.Value) == "failed") {
			continue
		}
		stoppedKey := types.KeyDerivedInstanceControllerStopped(instanceID)
		stoppedFact, err := factStore.Get(ctx, stoppedKey)
		if err == nil && string(stoppedFact.Value) == "true" {
			continue
		}
		nodeCounts[string(placement.Value)]++
	}
	return nodeCounts
}

// extractInstanceIDFromPlacementKey extracts the instance ID from a placement
// key like "placement/instance/{id}".
func extractInstanceIDFromPlacementKey(placementKey string) string {
	const prefix = "placement/instance/"
	if len(placementKey) > len(prefix) {
		return placementKey[len(prefix):]
	}
	return ""
}
