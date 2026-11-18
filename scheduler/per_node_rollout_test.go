// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"testing"

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
// (M241 2g.1). A stopped instance's placement stays until GC, but it no longer
// uses its node's CPU and memory, so a per-node replacement that requests 1500m
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
		kv(types.KeyDesiredServiceResourcesCPU("agentd"), "1500"),
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
