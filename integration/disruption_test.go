// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestDisruptionBudgetMinAvailableDuringDrain verifies that the DrainController
// respects disruption budgets: when draining a node, it never evicts instances
// that would drop the service below min_available.
func TestDisruptionBudgetMinAvailableDuringDrain(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	drainController := controllers.NewDrainController()

	runner := controllers.NewRunner(factStore, instanceController, schedulerController, drainController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register 2 nodes with enough capacity for 5 instances.
	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	go runner.Run(ctx)

	// Deploy service with 5 instances.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("5"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("5"))
	factStore.Put(ctx, types.KeyDesiredServiceDisruptionMinAvailable("web"), []byte("3"))

	// Wait for 5 placements.
	waitFor(t, 3*time.Second, "5 placements", func() bool {
		facts, _ := factStore.Scan(ctx, types.ScanPlacements)
		return len(facts) >= 5
	})

	// Simulate node agent reporting all instances as running.
	instances, _ := types.ListInstances(ctx, factStore)
	for _, instanceEntry := range instances {
		if instanceEntry.Service == "web" {
			factStore.Put(ctx, types.KeyObservedInstanceState(instanceEntry.ID), []byte("running"))
		}
	}

	// Verify all 5 are running.
	waitFor(t, 1*time.Second, "5 running instances", func() bool {
		currentInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instanceEntry := range currentInstances {
			if instanceEntry.Service == "web" && instanceEntry.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount == 5
	})

	// Find which instances are on node-1.
	placements, _ := factStore.Scan(ctx, types.ScanPlacements)
	instancesOnNodeOne := 0
	for _, placementFact := range placements {
		if string(placementFact.Value) == "node-1" {
			instancesOnNodeOne++
		}
	}
	t.Logf("instances on node-1 before drain: %d", instancesOnNodeOne)

	// Drain node-1.
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeDraining,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Let the drain controller run several cycles.
	time.Sleep(500 * time.Millisecond)

	// Count active (non-stopped) instances of web.
	currentInstances, _ := types.ListInstances(ctx, factStore)
	activeInstanceCount := 0
	for _, instanceEntry := range currentInstances {
		if instanceEntry.Service == "web" && instanceEntry.State != types.InstanceStopped && instanceEntry.State != types.InstanceFailed {
			activeInstanceCount++
		}
	}

	// The disruption budget requires min_available=3. The drain controller
	// should evict instances from node-1 but never drop below 3 active.
	if activeInstanceCount < 3 {
		t.Errorf("disruption budget violated: only %d active instances, min_available is 3", activeInstanceCount)
	}
	t.Logf("active instances after drain cycles: %d (min_available=3)", activeInstanceCount)
}
