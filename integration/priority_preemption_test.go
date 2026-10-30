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

func TestPriorityPreemptionLifecycle(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 1000, CapacityMemory: 2048,
			AvailableCPU: 1000, AvailableMemory: 2048,
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

	backgroundInput := `
service background {
    image worker:1.0
    instances 2
    priority 100
}
`
	if err := lang.Apply(ctx, factStore, backgroundInput); err != nil {
		t.Fatalf("apply background failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("background"), []byte("1000"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("background"), []byte("2048"))

	waitFor(t, 5*time.Second, "2 background instances placed", func() bool {
		return countPlacementsForService(ctx, factStore, "background") == 2
	})

	simulateInstancesRunning(ctx, factStore, "background")

	criticalInput := `
service critical {
    image api:2.0
    instances 1
    priority 800
}
`
	if err := lang.Apply(ctx, factStore, criticalInput); err != nil {
		t.Fatalf("apply critical failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("critical"), []byte("1000"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("critical"), []byte("2048"))

	waitFor(t, 5*time.Second, "a background instance preempted", func() bool {
		facts, _ := factStore.Scan(ctx, types.ScanDerivedInstances)
		for _, fact := range facts {
			if len(fact.Key) > len(types.ScanDerivedInstances) {
				suffix := fact.Key[len(types.ScanDerivedInstances):]
				if len(suffix) > len("/controller_stopped") {
					instanceID := suffix[:len(suffix)-len("/controller_stopped")]
					serviceFact, err := factStore.Get(ctx, types.KeyObservedInstanceService(instanceID))
					if err == nil && string(serviceFact.Value) == "background" && string(fact.Value) == "true" {
						return true
					}
				}
			}
		}
		return false
	})
}

func TestPriorityPreemptionRespectsEqual(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 1000, CapacityMemory: 2048,
			AvailableCPU: 1000, AvailableMemory: 2048,
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

	input := `
service alpha {
    image worker:1.0
    instances 2
    priority 500
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply alpha failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("alpha"), []byte("1000"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("alpha"), []byte("2048"))

	waitFor(t, 5*time.Second, "2 alpha instances placed", func() bool {
		return countPlacementsForService(ctx, factStore, "alpha") == 2
	})

	betaInput := `
service beta {
    image worker:1.0
    instances 1
    priority 500
}
`
	if err := lang.Apply(ctx, factStore, betaInput); err != nil {
		t.Fatalf("apply beta failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("beta"), []byte("1000"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("beta"), []byte("2048"))

	time.Sleep(scaleDuration(500 * time.Millisecond))

	waitFor(t, 2*time.Second, "no alpha instances preempted (equal priority)", func() bool {
		return countPlacementsForService(ctx, factStore, "alpha") == 2
	})
}

func TestPriorityEvictionOrder(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()
	evictionController := controllers.NewEvictionController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
		evictionController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	lowPriorityInput := `
service low-priority {
    image worker:1.0
    instances 1
    priority 100
}
`
	if err := lang.Apply(ctx, factStore, lowPriorityInput); err != nil {
		t.Fatalf("apply low-priority failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("low-priority"), []byte("2048"))

	highPriorityInput := `
service high-priority {
    image api:2.0
    instances 1
    priority 800
}
`
	if err := lang.Apply(ctx, factStore, highPriorityInput); err != nil {
		t.Fatalf("apply high-priority failed: %v", err)
	}

	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("high-priority"), []byte("2048"))

	waitFor(t, 5*time.Second, "both services placed", func() bool {
		return countPlacementsForService(ctx, factStore, "low-priority") == 1 &&
			countPlacementsForService(ctx, factStore, "high-priority") == 1
	})

	simulateInstancesRunning(ctx, factStore, "low-priority")
	simulateInstancesRunning(ctx, factStore, "high-priority")

	factStore.Put(ctx, types.KeyDerivedServiceQoSClass("low-priority"), []byte("Burstable"))
	factStore.Put(ctx, types.KeyDerivedServiceQoSClass("high-priority"), []byte("Burstable"))

	factStore.Put(ctx, types.KeyObservedNodeCapacityMemory("node-1"), []byte("8192"))
	factStore.Put(ctx, types.KeyObservedNodeAvailableMemory("node-1"), []byte("400"))

	waitFor(t, 5*time.Second, "low-priority instance evicted first", func() bool {
		facts, _ := factStore.Scan(ctx, types.ScanDerivedInstances)
		for _, fact := range facts {
			if len(fact.Key) > len(types.ScanDerivedInstances) {
				suffix := fact.Key[len(types.ScanDerivedInstances):]
				if len(suffix) > len("/controller_stopped") {
					instanceID := suffix[:len(suffix)-len("/controller_stopped")]
					serviceFact, err := factStore.Get(ctx, types.KeyObservedInstanceService(instanceID))
					if err == nil && string(serviceFact.Value) == "low-priority" && string(fact.Value) == "true" {
						return true
					}
				}
			}
		}
		return false
	})

	waitFor(t, 2*time.Second, "high-priority instance NOT evicted", func() bool {
		return countPlacementsForService(ctx, factStore, "high-priority") == 1
	})
}

// simulateInstancesRunning finds all placed instances of a service and writes
// observed state/node facts to simulate the node agent transitioning them to
// running. Without this, instances remain "pending" and the scheduler/eviction
// controller cannot treat them as preemption/eviction candidates.
func simulateInstancesRunning(ctx context.Context, factStore store.StateStore, serviceName string) {
	placements, _ := factStore.Scan(ctx, types.ScanPlacements)
	for _, placement := range placements {
		instanceID := extractInstanceIDFromPlacementKey(placement.Key)
		serviceKey := types.KeyObservedInstanceService(instanceID)
		serviceFact, err := factStore.Get(ctx, serviceKey)
		if err != nil || string(serviceFact.Value) != serviceName {
			continue
		}
		nodeID := string(placement.Value)
		factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceRunning)))
		factStore.Put(ctx, types.KeyObservedInstanceNode(instanceID), []byte(nodeID))
	}
}
