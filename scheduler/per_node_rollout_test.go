// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestPerNodePlacesReplacementOnNodeOfStoppedInstance: testbed T2 (M241 2e.2).
// A per-node rollout stops the old instance on node-1 (observed state stopped;
// its placement stays until instance GC, which waits for the replacement). The
// replacement is placed on node-1: a stopped instance does not occupy its node.
func TestPerNodePlacesReplacementOnNodeOfStoppedInstance(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("old-1"), "agentd"),
		kv(types.KeyObservedInstanceState("old-1"), string(types.InstanceStopped)),
		kv(types.KeyPlacementInstance("old-1"), "node-1"),
		kv(types.KeyObservedInstanceService("old-2"), "agentd"),
		kv(types.KeyObservedInstanceState("old-2"), string(types.InstanceRunning)),
		kv(types.KeyPlacementInstance("old-2"), "node-2"),
		kv(types.KeyObservedInstanceService("old-3"), "agentd"),
		kv(types.KeyObservedInstanceState("old-3"), string(types.InstanceRunning)),
		kv(types.KeyPlacementInstance("old-3"), "node-3"),
		kv(types.KeyObservedInstanceService("new-1"), "agentd"),
		kv(types.KeyObservedInstanceState("new-1"), string(types.InstancePending)),
		kv(types.KeyDesiredServicePlacementMode("agentd"), string(types.PlacementModePerNode)),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeState("node-2"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeState("node-3"), string(types.NodeAlive)),
	)
	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	for _, change := range filterPlacementChanges(changes) {
		if change.Key == types.KeyPlacementInstance("new-1") {
			if string(change.Value) != "node-1" {
				t.Errorf("new-1 placed on %s, want node-1 (the stopped instance's node)", change.Value)
			}
			return
		}
	}
	t.Errorf("new-1 not placed, want node-1; changes: %v", changes)
}

// TestPerNodeReplacementWithResourcesFitsBesideStoppedInstance: review round 4
// (M241 2g.1). A stopped instance's placement stays until GC, but its requests
// are no longer counted on its node, so a per-node replacement that requests 1500m
// of a 2000m node is placed there instead of being insufficient_capacity.
func TestPerNodeReplacementWithResourcesFitsBesideStoppedInstance(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("old-1"), "agentd"),
		kv(types.KeyObservedInstanceState("old-1"), string(types.InstanceStopped)),
		kv(types.KeyPlacementInstance("old-1"), "node-1"),
		kv(types.KeyObservedInstanceService("new-1"), "agentd"),
		kv(types.KeyObservedInstanceState("new-1"), string(types.InstancePending)),
		kv(types.KeyDesiredServicePlacementMode("agentd"), string(types.PlacementModePerNode)),
		kv(types.KeyDesiredServiceResourcesCPU("agentd"), "1500m"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "2000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	)
	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	for _, change := range filterPlacementChanges(changes) {
		if change.Key == types.KeyPlacementInstance("new-1") && string(change.Value) == "node-1" {
			return
		}
	}
	t.Errorf("new-1 not placed on node-1 beside the stopped old-1; changes: %v", changes)
}

// TestSpreadIgnoresStoppedInstance: review round 5 (M241 2h.1). A stopped web
// instance on node-1 (placement kept until GC) does not count for spread, so
// its replacement goes back to node-1, although node-1 carries more load,
// instead of giving node-2 a second web replica and node-1 none.
func TestSpreadIgnoresStoppedInstance(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-old-1"), "web"),
		kv(types.KeyObservedInstanceState("web-old-1"), string(types.InstanceStopped)),
		kv(types.KeyPlacementInstance("web-old-1"), "node-1"),
		kv(types.KeyObservedInstanceService("web-old-2"), "web"),
		kv(types.KeyObservedInstanceState("web-old-2"), string(types.InstanceRunning)),
		kv(types.KeyPlacementInstance("web-old-2"), "node-2"),
		kv(types.KeyObservedInstanceService("cache-1"), "cache"),
		kv(types.KeyObservedInstanceState("cache-1"), string(types.InstanceRunning)),
		kv(types.KeyPlacementInstance("cache-1"), "node-1"),
		kv(types.KeyObservedInstanceService("cache-2"), "cache"),
		kv(types.KeyObservedInstanceState("cache-2"), string(types.InstanceRunning)),
		kv(types.KeyPlacementInstance("cache-2"), "node-1"),
		kv(types.KeyObservedInstanceService("web-new"), "web"),
		kv(types.KeyObservedInstanceState("web-new"), string(types.InstancePending)),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeState("node-2"), string(types.NodeAlive)),
	)
	assertPlacedOn(t, placementScheduler, facts, "web-new", "node-1")
}

// TestStoppedInstanceRequestsReleasedBeforeContainerIsGone: review round 5
// (M241 2h.3), the accepted trade-off: once stopped is written, the scheduler
// no longer counts the instance's requests, though its container may still run
// (pre_stop, grace period), so another instance is placed beside it (M246).
func TestStoppedInstanceRequestsReleasedBeforeContainerIsGone(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-old"), "web"),
		kv(types.KeyObservedInstanceState("web-old"), string(types.InstanceStopped)),
		kv(types.KeyPlacementInstance("web-old"), "node-1"),
		kv(types.KeyObservedInstanceService("batch-1"), "batch"),
		kv(types.KeyObservedInstanceState("batch-1"), string(types.InstancePending)),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "1500m"),
		kv(types.KeyDesiredServiceResourcesCPU("batch"), "1500m"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "2000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	)
	// 1500m + 1500m on a 2000m node while web-old's container may still run.
	assertPlacedOn(t, placementScheduler, facts, "batch-1", "node-1")
}

// assertPlacedOn runs one scheduler pass and fails unless instanceID is placed on wantNode.
func assertPlacedOn(t *testing.T, placementScheduler *Scheduler, facts []store.Fact, instanceID string, wantNode string) {
	t.Helper()
	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	for _, change := range filterPlacementChanges(changes) {
		if change.Key == types.KeyPlacementInstance(instanceID) {
			if string(change.Value) != wantNode {
				t.Errorf("%s placed on %s, want %s", instanceID, change.Value, wantNode)
			}
			return
		}
	}
	t.Errorf("%s not placed, want %s; changes: %v", instanceID, wantNode, changes)
}

// TestSchedulerTreatsControllerStoppedAsStopped: testbed stall after 2g (M241
// 2i.2). A pending instance marked controller_stopped (surplus) is never
// placed, and a placed one counts for neither resources nor the per-node
// slot, so the real replacement fits on its node.
func TestSchedulerTreatsControllerStoppedAsStopped(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("surplus-placed"), "agentd"),
		kv(types.KeyObservedInstanceState("surplus-placed"), string(types.InstancePending)),
		kv(types.KeyDerivedInstanceControllerStopped("surplus-placed"), "true"),
		kv(types.KeyPlacementInstance("surplus-placed"), "node-1"),
		kv(types.KeyObservedInstanceService("surplus-pending"), "agentd"),
		kv(types.KeyObservedInstanceState("surplus-pending"), string(types.InstancePending)),
		kv(types.KeyDerivedInstanceControllerStopped("surplus-pending"), "true"),
		kv(types.KeyObservedInstanceService("replacement"), "agentd"),
		kv(types.KeyObservedInstanceState("replacement"), string(types.InstancePending)),
		kv(types.KeyDesiredServicePlacementMode("agentd"), string(types.PlacementModePerNode)),
		kv(types.KeyDesiredServiceResourcesCPU("agentd"), "1500m"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "2000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	)
	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	placedOnNode := map[string]string{}
	for _, change := range filterPlacementChanges(changes) {
		if instanceID, isPlacement := strings.CutPrefix(change.Key, types.ScanPlacements); isPlacement && !strings.Contains(instanceID, "/") {
			placedOnNode[instanceID] = string(change.Value)
		}
	}
	if nodeID, placed := placedOnNode["surplus-pending"]; placed {
		t.Errorf("controller_stopped surplus-pending placed on %s, want never placed", nodeID)
	}
	if placedOnNode["replacement"] != "node-1" {
		t.Errorf("replacement placed on %q, want node-1 (the surplus holds no slot or CPU); changes: %v", placedOnNode["replacement"], changes)
	}
}
