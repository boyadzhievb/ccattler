package scheduler

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func buildFacts(entries ...struct{ k, v string }) []store.Fact {
	facts := make([]store.Fact, len(entries))
	for index, entry := range entries {
		facts[index] = store.Fact{Key: entry.k, Value: []byte(entry.v)}
	}
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Key < facts[j].Key
	})
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

func TestRequireLabelPlacement(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		// node-1 has no gpu label — should be skipped.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		// node-2 has gpu=true — should be selected.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeLabel("node-2", "gpu"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has gpu=true), got %s", changes[0].Value)
	}
}

func TestRequireLabelMismatch(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		// node-1 has gpu=false — label exists but value doesn't match.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeLabel("node-1", "gpu"), "false"),
		// node-2 has no gpu label at all.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	// No node satisfies require — falls back to all candidates.
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (fallback), got %d", len(changes))
	}
}

func TestMultipleRequireLabels(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "special"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true AND ssd=true.
		kv(types.KeyDesiredServicePlacementRequire("special", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementRequire("special", "ssd"), "true"),
		// node-1 has gpu=true only.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeLabel("node-1", "gpu"), "true"),
		// node-2 has gpu=true and ssd=true.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeLabel("node-2", "gpu"), "true"),
		kv(types.KeyObservedNodeLabel("node-2", "ssd"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has both labels), got %s", changes[0].Value)
	}
}

func TestPreferLabelScoring(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service prefers region=us-east.
		kv(types.KeyDesiredServicePlacementPrefer("web", "region"), "us-east"),
		// node-1 has region=us-west.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeLabel("node-1", "region"), "us-west"),
		// node-2 has region=us-east — preferred.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeLabel("node-2", "region"), "us-east"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should prefer node-2 (region=us-east), got %s", changes[0].Value)
	}
}

func TestPreferFallsBackToLeastLoaded(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service prefers region=us-east, but no node has that label.
		kv(types.KeyDesiredServicePlacementPrefer("web", "region"), "us-east"),
		// Both nodes have equal load, neither matches.
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
	// Both score 0 preferences, falls back to least loaded (first alphabetically).
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should fall back to least-loaded node-1, got %s", changes[0].Value)
	}
}

func TestRestrictedNodeExcluded(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// node-1 is restricted with "dedicated-compute".
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 has no restrictions.
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
		t.Errorf("should skip restricted node-1, got %s", changes[0].Value)
	}
}

func TestAcceptRestrictedNode(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service accepts "dedicated-compute" restriction.
		kv(types.KeyDesiredServicePlacementAccept("ml-training", "dedicated-compute"), ""),
		// node-1 is restricted — service accepts it.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 has no restrictions.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	// Both nodes eligible — node-1 accepted, node-2 unrestricted. Least-loaded picks first.
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (restriction accepted), got %s", changes[0].Value)
	}
}

func TestAcceptMissingRestrictionLabel(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service accepts "gpu-pool" but node-1 has "dedicated-compute" restriction.
		kv(types.KeyDesiredServicePlacementAccept("web", "gpu-pool"), ""),
		// node-1 restricted with "dedicated-compute" — not accepted.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 unrestricted.
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
		t.Errorf("should skip node-1 (wrong restriction), got %s", changes[0].Value)
	}
}

func TestRequireAndPreferCombined(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true, prefers region=us-east.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementPrefer("ml-training", "region"), "us-east"),
		// node-1: gpu=true, region=us-west — satisfies require, not preferred.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeLabel("node-1", "gpu"), "true"),
		kv(types.KeyObservedNodeLabel("node-1", "region"), "us-west"),
		// node-2: gpu=true, region=us-east — satisfies require + preferred.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeLabel("node-2", "gpu"), "true"),
		kv(types.KeyObservedNodeLabel("node-2", "region"), "us-east"),
		// node-3: no gpu — filtered out by require.
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedNodeLabel("node-3", "region"), "us-east"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (gpu=true + preferred region), got %s", changes[0].Value)
	}
}

func TestRestrictAndRequireCombined(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true and accepts dedicated-compute.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementAccept("ml-training", "dedicated-compute"), ""),
		// node-1: gpu=true, restricted — service accepts restriction.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeLabel("node-1", "gpu"), "true"),
		kv(types.KeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2: gpu=false, no restriction.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeLabel("node-2", "gpu"), "false"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (gpu + accepted restriction), got %s", changes[0].Value)
	}
}

func TestZoneSpreadPlacement(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
		// Service requires zone spread.
		kv(types.KeyDesiredServicePlacementZonePolicy("web"), "spread"),
		// Three nodes in three different zones.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeZone("node-1"), "zone-a"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeZone("node-2"), "zone-b"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedNodeZone("node-3"), "zone-c"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 placements, got %d", len(changes))
	}
	// Verify all three instances land on different nodes (one per zone).
	placedNodes := make(map[string]bool)
	for _, change := range changes {
		placedNodes[string(change.Value)] = true
	}
	if len(placedNodes) != 3 {
		t.Errorf("expected 3 unique nodes (one per zone), got %d: %v", len(placedNodes), placedNodes)
	}
}

func TestZoneSpreadUnbalanced(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// One new pending instance to schedule.
		kv(types.KeyObservedInstanceService("new-1"), "web"),
		kv(types.KeyObservedInstanceState("new-1"), "pending"),
		// Service requires zone spread.
		kv(types.KeyDesiredServicePlacementZonePolicy("web"), "spread"),
		// Two existing running instances already on node-1 (zone-a).
		kv(types.KeyObservedInstanceService("existing-1"), "web"),
		kv(types.KeyObservedInstanceState("existing-1"), "running"),
		kv(types.KeyPlacementInstance("existing-1"), "node-1"),
		kv(types.KeyObservedInstanceService("existing-2"), "web"),
		kv(types.KeyObservedInstanceState("existing-2"), "running"),
		kv(types.KeyPlacementInstance("existing-2"), "node-1"),
		// Three nodes in three zones.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeZone("node-1"), "zone-a"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeZone("node-2"), "zone-b"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedNodeZone("node-3"), "zone-c"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	placedNode := string(changes[0].Value)
	// New instance should go to zone-b or zone-c (0 existing) rather than zone-a (2 existing).
	if placedNode == "node-1" {
		t.Errorf("should avoid zone-a (already has 2 instances), placed on %s", placedNode)
	}
}

func TestArchitectureFilterAmd64(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "amd-app"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires amd64 architecture.
		kv(types.KeyDesiredServicePlacementArchitecture("amd-app"), "amd64"),
		// node-1 is amd64 — eligible.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-1"), "amd64"),
		// node-2 is arm64 — should be excluded.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-2"), "arm64"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (amd64), got %s", changes[0].Value)
	}
}

func TestArchitectureFilterArm64(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "arm-app"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires arm64 architecture.
		kv(types.KeyDesiredServicePlacementArchitecture("arm-app"), "arm64"),
		// node-1 is amd64 — should be excluded.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-1"), "amd64"),
		// node-2 is arm64 — eligible.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-2"), "arm64"),
		// node-3 is arm64 — also eligible.
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-3"), "arm64"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	placedNode := string(changes[0].Value)
	if placedNode != "node-2" && placedNode != "node-3" {
		t.Errorf("should place on arm64 node (node-2 or node-3), got %s", placedNode)
	}
}

func TestArchitectureEmptyPassesAny(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "any-arch"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires arm64 architecture.
		kv(types.KeyDesiredServicePlacementArchitecture("any-arch"), "arm64"),
		// node-1 has no architecture reported — should still be eligible.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		// node-2 is amd64 — should be excluded.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeArchitecture("node-2"), "amd64"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (no arch = passes any constraint), got %s", changes[0].Value)
	}
}

func TestControllerInterface(t *testing.T) {
	placementScheduler := NewScheduler()
	if placementScheduler.Name() != "scheduler" {
		t.Fatalf("name: got %s, want scheduler", placementScheduler.Name())
	}
	if len(placementScheduler.Watch()) != 5 {
		t.Fatalf("expected 5 watch prefixes, got %d", len(placementScheduler.Watch()))
	}
}

func TestGroupCoSchedulingSameNode(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// Two instances from two different services, both in group "frontend"
		kv(types.KeyObservedInstanceService("proxy-1"), "proxy"),
		kv(types.KeyObservedInstanceState("proxy-1"), "pending"),
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "pending"),
		// Two alive nodes
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
		// Group definition: "frontend" contains proxy and web
		kv(types.KeyDesiredGroupProcess("frontend", "proxy"), "true"),
		kv(types.KeyDesiredGroupProcess("frontend", "web"), "true"),
		kv(types.KeyDesiredGroupShareNetwork("frontend"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 placements, got %d", len(changes))
	}

	// Both instances must be placed on the same node.
	nodeForProxy := string(changes[0].Value)
	nodeForWeb := string(changes[1].Value)
	if nodeForProxy != nodeForWeb {
		t.Errorf("group members placed on different nodes: %s vs %s", nodeForProxy, nodeForWeb)
	}
}

func TestGroupCoSchedulingJoinsExistingPlacement(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// proxy-1 is already running and placed on node-2
		kv(types.KeyObservedInstanceService("proxy-1"), "proxy"),
		kv(types.KeyObservedInstanceState("proxy-1"), "running"),
		kv(types.KeyPlacementInstance("proxy-1"), "node-2"),
		// web-1 is pending (not placed)
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "pending"),
		// Two alive nodes
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
		// Group: proxy and web co-scheduled
		kv(types.KeyDesiredGroupProcess("frontend", "proxy"), "true"),
		kv(types.KeyDesiredGroupProcess("frontend", "web"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (web-1 only), got %d", len(changes))
	}

	if string(changes[0].Value) != "node-2" {
		t.Errorf("expected web-1 placed on node-2 (with proxy-1), got %s", changes[0].Value)
	}
}

func TestGroupDoesNotAffectUngroupedInstances(t *testing.T) {
	placementScheduler := NewScheduler()

	facts := buildFacts(
		// Group instances
		kv(types.KeyObservedInstanceService("proxy-1"), "proxy"),
		kv(types.KeyObservedInstanceState("proxy-1"), "pending"),
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "pending"),
		// Ungrouped instance
		kv(types.KeyObservedInstanceService("api-1"), "api"),
		kv(types.KeyObservedInstanceState("api-1"), "pending"),
		// Two nodes
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
		// Group: only proxy and web
		kv(types.KeyDesiredGroupProcess("frontend", "proxy"), "true"),
		kv(types.KeyDesiredGroupProcess("frontend", "web"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 placements, got %d", len(changes))
	}

	// Find placements by instance key
	placementByInstance := make(map[string]string)
	for _, change := range changes {
		instanceID := strings.TrimPrefix(change.Key, types.ScanPlacements)
		placementByInstance[instanceID] = string(change.Value)
	}

	// Group members must be co-located
	if placementByInstance["proxy-1"] != placementByInstance["web-1"] {
		t.Errorf("group members not co-located: proxy-1=%s, web-1=%s",
			placementByInstance["proxy-1"], placementByInstance["web-1"])
	}

	// api-1 should be placed somewhere (possibly different node)
	if placementByInstance["api-1"] == "" {
		t.Error("ungrouped instance api-1 was not placed")
	}
}

func TestExtractServiceGroupMemberships(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyDesiredGroupProcess("frontend", "proxy"), "true"),
		kv(types.KeyDesiredGroupProcess("frontend", "web"), "true"),
		kv(types.KeyDesiredGroupProcess("backend", "api"), "true"),
		kv(types.KeyDesiredGroupShareNetwork("frontend"), "true"),
	)

	serviceToGroup := extractServiceGroupMemberships(facts)

	if serviceToGroup["proxy"] != "frontend" {
		t.Errorf("proxy: got %q, want frontend", serviceToGroup["proxy"])
	}
	if serviceToGroup["web"] != "frontend" {
		t.Errorf("web: got %q, want frontend", serviceToGroup["web"])
	}
	if serviceToGroup["api"] != "backend" {
		t.Errorf("api: got %q, want backend", serviceToGroup["api"])
	}
	if _, exists := serviceToGroup["unknown"]; exists {
		t.Error("unknown service should not be in group map")
	}
}

func TestPartitionUnplacedByGroup(t *testing.T) {
	instances := map[string]*schedulerInstanceInfo{
		"proxy-1": {service: "proxy", state: types.InstancePending},
		"web-1":   {service: "web", state: types.InstancePending},
		"api-1":   {service: "api", state: types.InstancePending},
	}
	serviceToGroup := map[string]string{
		"proxy": "frontend",
		"web":   "frontend",
	}

	grouped, ungrouped := partitionUnplacedByGroup(
		[]string{"api-1", "proxy-1", "web-1"}, instances, serviceToGroup,
	)

	if len(grouped["frontend"]) != 2 {
		t.Fatalf("expected 2 instances in frontend group, got %d", len(grouped["frontend"]))
	}
	if len(ungrouped) != 1 || ungrouped[0] != "api-1" {
		t.Fatalf("expected [api-1] ungrouped, got %v", ungrouped)
	}
}
