// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
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

	// Let the drain controller run several cycles to write eviction markers.
	time.Sleep(500 * time.Millisecond)

	// Count eviction markers. The drain controller writes
	// derived/node/{nodeID}/drain/evict/{instanceID} = "true" for each evicted
	// instance, but the disruption budget limits how many markers can exist.
	derivedFacts, _ := factStore.Scan(ctx, types.ScanDerivedNodes)
	evictionCount := 0
	for _, derivedFact := range derivedFacts {
		if strings.Contains(derivedFact.Key, "/drain/evict/") {
			evictionCount++
		}
	}

	// 5 instances with min_available=3 means at most 2 can be evicted.
	// With instances spread across node-1 and node-2, only node-1's instances
	// are evicted. The budget should never allow more than 2 evictions.
	totalInstances := 5
	nonEvictedCount := totalInstances - evictionCount
	if nonEvictedCount < 3 {
		t.Errorf("disruption budget violated: only %d non-evicted instances, min_available is 3", nonEvictedCount)
	}
	t.Logf("eviction markers after drain cycles: %d (min_available=3, non-evicted=%d)", evictionCount, nonEvictedCount)
}

// TestDrainNodeMigratesInstances deploys a service across 3 nodes with real
// agents, drains one node, and verifies that all instances are migrated to
// the surviving nodes while total instance count is preserved.
func TestDrainNodeMigratesInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	clusterContext, clusterCancel := context.WithCancel(context.Background())
	defer clusterCancel()

	nodeIdentifiers := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIdentifiers {
		types.WriteNode(clusterContext, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	drainController := controllers.NewDrainController()

	controllerRunner := controllers.NewRunner(factStore, instanceController,
		schedulerController, endpointController, failureController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(clusterContext)

	// The drain controller runs in a separate runner with no input-key guards.
	// This avoids transaction conflicts with agent writes to observed/instance/.
	drainRunner := controllers.NewRunner(factStore, drainController)
	drainRunner.SetDebounce(10 * time.Millisecond)
	drainRunner.SetMaxInputKeyGuards(0)
	go drainRunner.Run(clusterContext)

	for _, nodeID := range nodeIdentifiers {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(clusterContext)
	}

	// Deploy 6 instances — expect roughly 2 per node.
	factStore.Put(clusterContext, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(clusterContext, types.KeyEffectiveServiceInstances("web"), []byte("6"))
	factStore.Put(clusterContext, types.KeyDesiredServiceInstances("web"), []byte("6"))

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(clusterContext, factStore, "web") == 6
	})

	// Verify instances are spread across at least 2 nodes.
	placementsBefore, _ := factStore.Scan(clusterContext, types.ScanPlacements)
	instancesPerNode := make(map[string]int)
	for _, placementFact := range placementsBefore {
		instancesPerNode[string(placementFact.Value)]++
	}
	if len(instancesPerNode) < 2 {
		t.Fatalf("instances placed on only %d node(s), expected spread across 2+", len(instancesPerNode))
	}
	instancesOnDrainedNode := instancesPerNode["node-1"]
	t.Logf("before drain: %v (node-1 has %d)", instancesPerNode, instancesOnDrainedNode)

	// Drain node-1.
	types.WriteNode(clusterContext, factStore, types.Node{
		ID: "node-1", State: types.NodeDraining,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Wait for all instances to migrate off node-1.
	waitFor(t, 10*time.Second, "zero running instances on node-1", func() bool {
		placements, _ := factStore.Scan(clusterContext, types.ScanPlacements)
		for _, placementFact := range placements {
			if string(placementFact.Value) == "node-1" {
				instanceID := placementFact.Key[len(types.ScanPlacements):]
				stateValue, err := factStore.Get(clusterContext, types.KeyObservedInstanceState(instanceID))
				if err == nil && string(stateValue.Value) == string(types.InstanceRunning) {
					return false
				}
			}
		}
		return true
	})

	// Verify total running count is still 6 — all migrated to node-2 and node-3.
	waitFor(t, 5*time.Second, "6 running web instances after drain", func() bool {
		return helperCountRunningInstances(clusterContext, factStore, "web") == 6
	})

	// Verify placements: all 6 instances now on node-2 and node-3 only.
	placementsAfter, _ := factStore.Scan(clusterContext, types.ScanPlacements)
	instancesPerNodeAfter := make(map[string]int)
	for _, placementFact := range placementsAfter {
		instanceID := placementFact.Key[len(types.ScanPlacements):]
		stateValue, _ := factStore.Get(clusterContext, types.KeyObservedInstanceState(instanceID))
		if string(stateValue.Value) == string(types.InstanceRunning) {
			instancesPerNodeAfter[string(placementFact.Value)]++
		}
	}
	t.Logf("after drain: %v", instancesPerNodeAfter)

	if instancesPerNodeAfter["node-1"] > 0 {
		t.Errorf("node-1 still has %d running instances after drain", instancesPerNodeAfter["node-1"])
	}
	totalRunningAfter := instancesPerNodeAfter["node-2"] + instancesPerNodeAfter["node-3"]
	if totalRunningAfter != 6 {
		t.Errorf("expected 6 running instances on node-2+node-3, got %d", totalRunningAfter)
	}
}
