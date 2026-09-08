package scheduler

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func buildFacts(entries ...struct{ k, v string }) []store.Fact {
	facts := make([]store.Fact, len(entries))
	for index, entry := range entries {
		facts[index] = store.Fact{Key: entry.k, Value: []byte(entry.v)}
	}
	return facts
}

func kv(k, v string) struct{ k, v string } {
	return struct{ k, v string }{k, v}
}

func TestPlacePendingInstance(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if changes[0].Key != types.KeyPlacementInstance("aaa") {
		t.Errorf("key: got %s, want %s", changes[0].Key, types.KeyPlacementInstance("aaa"))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("value: got %s, want node-1", changes[0].Value)
	}
}

func TestSkipAlreadyPlaced(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for already-placed instance, got %d", len(changes))
	}
}

func TestSkipRunningInstances(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for running instance, got %d", len(changes))
	}
}

func TestSpreadAcrossNodes(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// 3 pending instances, no placements yet.
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
		// 3 alive nodes.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 placements, got %d", len(changes))
	}

	// Each node should get exactly 1 instance (spread).
	nodeCount := make(map[string]int)
	for _, ch := range changes {
		nodeCount[string(ch.Value)]++
	}
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		if nodeCount[nodeID] != 1 {
			t.Errorf("node %s got %d instances, want 1", nodeID, nodeCount[nodeID])
		}
	}
}

func TestSpreadWithExistingLoad(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// Existing placement: node-1 already has one.
		kv(types.KeyObservedInstanceService("existing"), "api"),
		kv(types.KeyObservedInstanceState("existing"), "running"),
		kv(types.KeyPlacementInstance("existing"), "node-1"),
		// New pending instance.
		kv(types.KeyObservedInstanceService("new"), "web"),
		kv(types.KeyObservedInstanceState("new"), "pending"),
		// Two nodes.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should prefer node-2 (less loaded), got %s", changes[0].Value)
	}
}

func TestSkipUnreachableNodes(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "unreachable"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should skip unreachable node-1, got %s", changes[0].Value)
	}
}

func TestNoAliveNodes(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "unreachable"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 placements with no alive nodes, got %d", len(changes))
	}
}

func TestNoInstancesToPlace(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes with no pending instances, got %d", len(changes))
	}
}

func TestResourceFit(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// web requires 2000 CPU, 4096 memory.
		kv(types.KeyDesiredServiceResourcesCPU("web"), "2000"),
		kv(types.KeyDesiredServiceResourcesMemory("web"), "4096"),
		// node-1: only 1000 CPU available — too small.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		// node-2: 4000 CPU available — fits.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has capacity), got %s", changes[0].Value)
	}
}

func TestResourceExhaustion(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// 3 pending instances each needing 2000 CPU.
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "2000"),
		// One node with 4000 CPU — fits 2, not 3.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "16384"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 placements (node full after 2), got %d", len(changes))
	}
}

func TestResourceSpreadWithAccounting(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// 2 pending instances each needing 1000 CPU.
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "1000"),
		// node-1: 1500 available, node-2: 2500 available.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1500"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "2500"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 placements, got %d", len(changes))
	}

	// Both should be placed (spread), one per node.
	nodeCount := make(map[string]int)
	for _, ch := range changes {
		nodeCount[string(ch.Value)]++
	}
	if nodeCount["node-1"] != 1 || nodeCount["node-2"] != 1 {
		t.Errorf("expected 1 per node, got node-1=%d node-2=%d", nodeCount["node-1"], nodeCount["node-2"])
	}
}

func TestNoResourceRequirements(t *testing.T) {
	placementScheduler := NewScheduler()

	// No resource requirements → place anywhere (backwards compatible).
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "0"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (no requirements = fits anywhere), got %d", len(changes))
	}
}

func TestControllerInterface(t *testing.T) {
	placementScheduler := NewScheduler()
	if placementScheduler.Name() != "scheduler" {
		t.Fatalf("name: got %s, want scheduler", placementScheduler.Name())
	}
	if len(placementScheduler.Watch()) != 4 {
		t.Fatalf("expected 4 watch prefixes, got %d", len(placementScheduler.Watch()))
	}
}
