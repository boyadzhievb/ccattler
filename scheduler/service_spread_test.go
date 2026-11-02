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

// placedInstanceFacts returns the facts for a running instance of service on node.
func placedInstanceFacts(instanceID, serviceName, nodeID string) []struct{ k, v string } {
	return []struct{ k, v string }{
		kv(types.KeyObservedInstanceService(instanceID), serviceName),
		kv(types.KeyObservedInstanceState(instanceID), "running"),
		kv(types.KeyPlacementInstance(instanceID), nodeID),
	}
}

// TestReplacementAvoidsNodeHostingSameService reproduces the deploy-testbed
// churn case: whoami scaled 2→1→2 while its surviving instance sits on the
// least-loaded node. The replacement must go to a node without whoami even
// though that node has more instances in total.
func TestReplacementAvoidsNodeHostingSameService(t *testing.T) {
	placementScheduler := newTestScheduler()
	var entries []struct{ k, v string }
	entries = append(entries, placedInstanceFacts("whoami-a", "whoami", "node-1")...)
	entries = append(entries, placedInstanceFacts("multiport-a", "multiport", "node-2")...)
	entries = append(entries, placedInstanceFacts("gate-a", "gate", "node-2")...)
	entries = append(entries, placedInstanceFacts("multiport-b", "multiport", "node-3")...)
	entries = append(entries, placedInstanceFacts("gate-b", "gate", "node-3")...)
	entries = append(entries,
		kv(types.KeyObservedInstanceService("whoami-b"), "whoami"),
		kv(types.KeyObservedInstanceState("whoami-b"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), buildFacts(entries...))
	if err != nil {
		t.Fatal(err)
	}
	placementChanges := filterPlacementChanges(changes)
	if len(placementChanges) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(placementChanges))
	}
	if string(placementChanges[0].Value) == "node-1" {
		t.Error("replacement placed next to the surviving whoami instance on node-1")
	}
}

// TestBatchSpreadsServiceBeforeLoad verifies a batch of new instances of one
// service lands on distinct nodes even when one node is far less loaded.
func TestBatchSpreadsServiceBeforeLoad(t *testing.T) {
	placementScheduler := newTestScheduler()
	var entries []struct{ k, v string }
	for _, existingID := range []string{"other-1", "other-2", "other-3"} {
		entries = append(entries, placedInstanceFacts(existingID, "other", "node-2")...)
	}
	entries = append(entries,
		kv(types.KeyObservedInstanceService("web-a"), "web"),
		kv(types.KeyObservedInstanceState("web-a"), "pending"),
		kv(types.KeyObservedInstanceService("web-b"), "web"),
		kv(types.KeyObservedInstanceState("web-b"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), buildFacts(entries...))
	if err != nil {
		t.Fatal(err)
	}
	placedNodes := make(map[string]bool)
	for _, change := range filterPlacementChanges(changes) {
		placedNodes[string(change.Value)] = true
	}
	if len(placedNodes) != 2 {
		t.Errorf("expected web on both nodes, got %v", placedNodes)
	}
}

// TestRebalanceDoesNotStackSameService verifies rebalance never moves an
// instance onto a node that already hosts as many instances of its service.
func TestRebalanceDoesNotStackSameService(t *testing.T) {
	placementScheduler := newTestScheduler()
	var entries []struct{ k, v string }
	entries = append(entries, placedInstanceFacts("a-web-1", "web", "node-1")...)
	entries = append(entries, placedInstanceFacts("b-api-1", "api", "node-1")...)
	entries = append(entries, placedInstanceFacts("c-api-2", "api", "node-1")...)
	entries = append(entries, placedInstanceFacts("d-api-3", "api", "node-1")...)
	entries = append(entries, placedInstanceFacts("e-web-2", "web", "node-2")...)
	entries = append(entries, placedInstanceFacts("f-web-3", "web", "node-3")...)
	entries = append(entries, placedInstanceFacts("g-db-1", "db", "node-3")...)
	entries = append(entries,
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), buildFacts(entries...))
	if err != nil {
		t.Fatal(err)
	}
	movedInstances := 0
	for _, change := range changes {
		if !strings.HasPrefix(change.Group, "rebalance/") || change.Type != store.OpPut {
			continue
		}
		if change.Key == types.KeyPlacementInstance("a-web-1") {
			t.Errorf("web instance moved to %s, which already hosts web", change.Value)
		}
		if strings.HasPrefix(change.Key, types.ScanPlacements) {
			movedInstances++
		}
	}
	if movedInstances == 0 {
		t.Error("expected api instances to be rebalanced off node-1")
	}
}
