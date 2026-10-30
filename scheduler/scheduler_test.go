// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// testKeyObservedNodeLabel constructs the store path for a node label in tests.
// Production code reads this via prefix scan, not through a shared key function.
func testKeyObservedNodeLabel(nodeID, label string) string {
	return fmt.Sprintf("%s/node/%s/label/%s", types.PrefixObserved, nodeID, label)
}

// testKeyObservedNodeRestrict constructs the store path for a node restriction in tests.
// Production code reads this via prefix scan, not through a shared key function.
func testKeyObservedNodeRestrict(nodeID, label string) string {
	return fmt.Sprintf("%s/node/%s/restrict/%s", types.PrefixObserved, nodeID, label)
}

// newTestScheduler creates a Scheduler with grace period disabled for tests
// that need immediate placement on first reconcile call.
func newTestScheduler() *Scheduler {
	return &Scheduler{placementGracePeriod: 0}
}

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

// filterPlacementChanges returns only changes that write to the placement/
// prefix, filtering out unplaced-demand facts.
func filterPlacementChanges(changes []controllers.Change) []controllers.Change {
	var placements []controllers.Change
	for _, change := range changes {
		if strings.HasPrefix(change.Key, types.ScanPlacements) {
			placements = append(placements, change)
		}
	}
	return placements
}

// filterUnplacedDemandChanges returns only changes that write to the
// derived/scheduler/unplaced/ prefix.
func filterUnplacedDemandChanges(changes []controllers.Change) []controllers.Change {
	var unplacedChanges []controllers.Change
	for _, change := range changes {
		if strings.HasPrefix(change.Key, types.ScanDerivedSchedulerUnplaced) {
			unplacedChanges = append(unplacedChanges, change)
		}
	}
	return unplacedChanges
}

func TestPlacePendingInstance(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for already-placed instance, got %d", len(changes))
	}
}

func TestSkipRunningInstances(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for running instance, got %d", len(changes))
	}
}

func TestSpreadAcrossNodes(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should prefer node-2 (less loaded), got %s", changes[0].Value)
	}
}

func TestSkipUnreachableNodes(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should skip unreachable node-1, got %s", changes[0].Value)
	}
}

func TestNoAliveNodes(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "unreachable"),
	)

	allChanges, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	placements := filterPlacementChanges(allChanges)
	if len(placements) != 0 {
		t.Fatalf("expected 0 placements with no alive nodes, got %d", len(placements))
	}
	unplacedChanges := filterUnplacedDemandChanges(allChanges)
	if len(unplacedChanges) != 2 {
		t.Fatalf("expected 2 unplaced-demand facts (reason+requirements), got %d", len(unplacedChanges))
	}
}

func TestNoInstancesToPlace(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes with no pending instances, got %d", len(changes))
	}
}

func TestResourceFit(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has capacity), got %s", changes[0].Value)
	}
}

func TestResourceExhaustion(t *testing.T) {
	placementScheduler := newTestScheduler()

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

	allChanges, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	placements := filterPlacementChanges(allChanges)
	if len(placements) != 2 {
		t.Fatalf("expected 2 placements (node full after 2), got %d", len(placements))
	}
	unplacedChanges := filterUnplacedDemandChanges(allChanges)
	if len(unplacedChanges) != 2 {
		t.Fatalf("expected 2 unplaced-demand facts for 1 remaining instance, got %d", len(unplacedChanges))
	}
}

func TestResourceSpreadWithAccounting(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (no requirements = fits anywhere), got %d", len(changes))
	}
}

func TestRequireLabelPlacement(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		// node-1 has no gpu label — should be skipped.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		// node-2 has gpu=true — should be selected.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(testKeyObservedNodeLabel("node-2", "gpu"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has gpu=true), got %s", changes[0].Value)
	}
}

func TestRequireLabelMismatch(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		// node-1 has gpu=false — label exists but value doesn't match.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeLabel("node-1", "gpu"), "false"),
		// node-2 has no gpu label at all.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	// No node satisfies require — falls back to all candidates.
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (fallback), got %d", len(changes))
	}
}

func TestMultipleRequireLabels(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "special"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true AND ssd=true.
		kv(types.KeyDesiredServicePlacementRequire("special", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementRequire("special", "ssd"), "true"),
		// node-1 has gpu=true only.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeLabel("node-1", "gpu"), "true"),
		// node-2 has gpu=true and ssd=true.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(testKeyObservedNodeLabel("node-2", "gpu"), "true"),
		kv(testKeyObservedNodeLabel("node-2", "ssd"), "true"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (has both labels), got %s", changes[0].Value)
	}
}

func TestPreferLabelScoring(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service prefers region=us-east.
		kv(types.KeyDesiredServicePlacementPrefer("web", "region"), "us-east"),
		// node-1 has region=us-west.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeLabel("node-1", "region"), "us-west"),
		// node-2 has region=us-east — preferred.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(testKeyObservedNodeLabel("node-2", "region"), "us-east"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should prefer node-2 (region=us-east), got %s", changes[0].Value)
	}
}

func TestPreferFallsBackToLeastLoaded(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	// Both score 0 preferences, falls back to least loaded (first alphabetically).
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should fall back to least-loaded node-1, got %s", changes[0].Value)
	}
}

func TestRestrictedNodeExcluded(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// node-1 is restricted with "dedicated-compute".
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 has no restrictions.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should skip restricted node-1, got %s", changes[0].Value)
	}
}

func TestAcceptRestrictedNode(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service accepts "dedicated-compute" restriction.
		kv(types.KeyDesiredServicePlacementAccept("ml-training", "dedicated-compute"), ""),
		// node-1 is restricted — service accepts it.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 has no restrictions.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	// Both nodes eligible — node-1 accepted, node-2 unrestricted. Least-loaded picks first.
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (restriction accepted), got %s", changes[0].Value)
	}
}

func TestAcceptMissingRestrictionLabel(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service accepts "gpu-pool" but node-1 has "dedicated-compute" restriction.
		kv(types.KeyDesiredServicePlacementAccept("web", "gpu-pool"), ""),
		// node-1 restricted with "dedicated-compute" — not accepted.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2 unrestricted.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should skip node-1 (wrong restriction), got %s", changes[0].Value)
	}
}

func TestRequireAndPreferCombined(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true, prefers region=us-east.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementPrefer("ml-training", "region"), "us-east"),
		// node-1: gpu=true, region=us-west — satisfies require, not preferred.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeLabel("node-1", "gpu"), "true"),
		kv(testKeyObservedNodeLabel("node-1", "region"), "us-west"),
		// node-2: gpu=true, region=us-east — satisfies require + preferred.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(testKeyObservedNodeLabel("node-2", "gpu"), "true"),
		kv(testKeyObservedNodeLabel("node-2", "region"), "us-east"),
		// node-3: no gpu — filtered out by require.
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(testKeyObservedNodeLabel("node-3", "region"), "us-east"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should place on node-2 (gpu=true + preferred region), got %s", changes[0].Value)
	}
}

func TestRestrictAndRequireCombined(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "ml-training"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		// Service requires gpu=true and accepts dedicated-compute.
		kv(types.KeyDesiredServicePlacementRequire("ml-training", "gpu"), "true"),
		kv(types.KeyDesiredServicePlacementAccept("ml-training", "dedicated-compute"), ""),
		// node-1: gpu=true, restricted — service accepts restriction.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(testKeyObservedNodeLabel("node-1", "gpu"), "true"),
		kv(testKeyObservedNodeRestrict("node-1", "dedicated-compute"), ""),
		// node-2: gpu=false, no restriction.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(testKeyObservedNodeLabel("node-2", "gpu"), "false"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (gpu + accepted restriction), got %s", changes[0].Value)
	}
}

func TestZoneSpreadPlacement(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (amd64), got %s", changes[0].Value)
	}
}

func TestArchitectureFilterArm64(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	placedNode := string(changes[0].Value)
	if placedNode != "node-2" && placedNode != "node-3" {
		t.Errorf("should place on arm64 node (node-2 or node-3), got %s", placedNode)
	}
}

func TestArchitectureEmptyPassesAny(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should place on node-1 (no arch = passes any constraint), got %s", changes[0].Value)
	}
}

func TestControllerInterface(t *testing.T) {
	placementScheduler := newTestScheduler()
	if placementScheduler.Name() != "scheduler" {
		t.Fatalf("name: got %s, want scheduler", placementScheduler.Name())
	}
	if len(placementScheduler.Watch()) != 6 {
		t.Fatalf("expected 6 watch prefixes, got %d", len(placementScheduler.Watch()))
	}
}

func TestGroupCoSchedulingSameNode(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement (web-1 only), got %d", len(changes))
	}

	if string(changes[0].Value) != "node-2" {
		t.Errorf("expected web-1 placed on node-2 (with proxy-1), got %s", changes[0].Value)
	}
}

func TestGroupDoesNotAffectUngroupedInstances(t *testing.T) {
	placementScheduler := newTestScheduler()

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
	changes = filterPlacementChanges(changes)
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

func TestDisabledNodeExcludedFromScheduler(t *testing.T) {
	placementScheduler := newTestScheduler()

	// Two nodes: node-1 is disabled, node-2 is alive.
	// A pending instance should only be placed on node-2.
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("inst-1"), "web"),
		kv(types.KeyObservedInstanceState("inst-1"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeDisabled)),
		kv(types.KeyObservedNodeCapacityCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeCapacityMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeCapacityCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeCapacityMemory("node-2"), "8192"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
	)

	proposedChanges, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	placements := filterPlacementChanges(proposedChanges)
	if len(placements) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(placements))
	}
	if string(placements[0].Value) != "node-2" {
		t.Errorf("expected placement on node-2, got %s", string(placements[0].Value))
	}
}

func TestDrainingNodeExcludedFromScheduler(t *testing.T) {
	placementScheduler := newTestScheduler()

	// Two nodes: node-1 is draining, node-2 is alive.
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("inst-1"), "web"),
		kv(types.KeyObservedInstanceState("inst-1"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeDraining)),
		kv(types.KeyObservedNodeCapacityCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeCapacityMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeCapacityCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeCapacityMemory("node-2"), "8192"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
	)

	proposedChanges, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	placements := filterPlacementChanges(proposedChanges)
	if len(placements) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(placements))
	}
	if string(placements[0].Value) != "node-2" {
		t.Errorf("expected placement on node-2, got %s", string(placements[0].Value))
	}
}

func TestUnplacedDemandEmittedForInsufficientCapacity(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "8000"),
		// Node has 4000 CPU — not enough for 8000 requirement.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "16384"),
	)

	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	placements := filterPlacementChanges(changes)
	if len(placements) != 0 {
		t.Fatalf("expected 0 placements (insufficient capacity), got %d", len(placements))
	}

	unplacedChanges := filterUnplacedDemandChanges(changes)
	if len(unplacedChanges) != 2 {
		t.Fatalf("expected 2 unplaced-demand facts, got %d", len(unplacedChanges))
	}

	foundReason := false
	for _, change := range unplacedChanges {
		if change.Key == types.KeyDerivedSchedulerUnplacedReason("aaa") {
			foundReason = true
			if string(change.Value) != string(types.UnplacedInsufficientCapacity) {
				t.Errorf("expected reason %s, got %s", types.UnplacedInsufficientCapacity, change.Value)
			}
		}
	}
	if !foundReason {
		t.Error("expected unplaced reason fact for instance aaa")
	}
}

func TestUnplacedDemandEmittedForUnsatisfiableConstraint(t *testing.T) {
	placementScheduler := newTestScheduler()

	// Instance requires arm64 and 8000 CPU (more than the node has). The node
	// is amd64. The scheduler's constraint filter falls back when no candidates
	// match, so CPU exhaustion prevents placement. The unplaced classifier
	// then detects the architecture mismatch and reports unsatisfiable_constraint.
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyDesiredServicePlacementArchitecture("web"), "arm64"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "8000"),
		// Node is amd64 with only 4000 CPU.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "16384"),
		kv(types.KeyObservedNodeArchitecture("node-1"), "amd64"),
	)

	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	placements := filterPlacementChanges(changes)
	if len(placements) != 0 {
		t.Fatalf("expected 0 placements (unsatisfiable constraint + insufficient CPU), got %d", len(placements))
	}

	unplacedChanges := filterUnplacedDemandChanges(changes)
	foundReason := false
	for _, change := range unplacedChanges {
		if change.Key == types.KeyDerivedSchedulerUnplacedReason("aaa") {
			foundReason = true
			if string(change.Value) != string(types.UnplacedUnsatisfiableConstraint) {
				t.Errorf("expected reason %s, got %s", types.UnplacedUnsatisfiableConstraint, change.Value)
			}
		}
	}
	if !foundReason {
		t.Error("expected unplaced reason fact for instance aaa")
	}
}

func TestUnplacedDemandCleanedUpWhenInstancePlaced(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "16384"),
		// Stale unplaced fact from a previous cycle — aaa is now placeable.
		kv(types.KeyDerivedSchedulerUnplacedReason("aaa"), string(types.UnplacedInsufficientCapacity)),
		kv(types.KeyDerivedSchedulerUnplacedRequirements("aaa"), `{"cpu":1000}`),
	)

	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	placements := filterPlacementChanges(changes)
	if len(placements) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(placements))
	}

	deleteCount := 0
	for _, change := range changes {
		if change.Type == store.OpDelete && strings.HasPrefix(change.Key, types.ScanDerivedSchedulerUnplaced) {
			deleteCount++
		}
	}
	if deleteCount != 2 {
		t.Fatalf("expected 2 delete changes for stale unplaced facts, got %d", deleteCount)
	}
}

func TestNoNodesEmitsUnplacedDemand(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
	)

	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	unplacedChanges := filterUnplacedDemandChanges(changes)
	foundReason := false
	for _, change := range unplacedChanges {
		if change.Key == types.KeyDerivedSchedulerUnplacedReason("aaa") {
			foundReason = true
			if string(change.Value) != string(types.UnplacedNoNodes) {
				t.Errorf("expected reason %s, got %s", types.UnplacedNoNodes, change.Value)
			}
		}
	}
	if !foundReason {
		t.Error("expected unplaced reason fact for instance aaa with no_nodes")
	}
}

// --- Grace period tests ---

func TestGracePeriodDefersInitialPlacement(t *testing.T) {
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
	placements := filterPlacementChanges(changes)
	if len(placements) != 0 {
		t.Fatalf("grace period should defer placement, got %d changes", len(placements))
	}
}

func TestGracePeriodNotAppliedWithExistingPlacements(t *testing.T) {
	placementScheduler := NewScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("existing"), "api"),
		kv(types.KeyObservedInstanceState("existing"), "running"),
		kv(types.KeyPlacementInstance("existing"), "node-1"),
		kv(types.KeyObservedInstanceService("new"), "web"),
		kv(types.KeyObservedInstanceState("new"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	placements := filterPlacementChanges(changes)
	if len(placements) != 1 {
		t.Fatalf("grace period should not apply with existing placements, got %d", len(placements))
	}
}

func TestGracePeriodExpiresAndPlaces(t *testing.T) {
	placementScheduler := &Scheduler{placementGracePeriod: 1 * time.Millisecond}
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
	)
	// First call starts grace period
	changes, _ := placementScheduler.Reconcile(context.Background(), facts)
	if len(filterPlacementChanges(changes)) != 0 {
		t.Fatal("first call should defer")
	}
	time.Sleep(5 * time.Millisecond)
	// Second call after grace period expires
	changes, _ = placementScheduler.Reconcile(context.Background(), facts)
	if len(filterPlacementChanges(changes)) != 1 {
		t.Fatalf("expected 1 placement after grace period, got %d", len(filterPlacementChanges(changes)))
	}
}

// --- Rebalancing tests ---

func TestRebalanceSpreadsFromOverloadedNode(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 4 running instances all placed on node-1
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-1"),
		kv(types.KeyObservedInstanceService("ccc"), "api"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
		kv(types.KeyPlacementInstance("ccc"), "node-1"),
		kv(types.KeyObservedInstanceService("ddd"), "api"),
		kv(types.KeyObservedInstanceState("ddd"), "running"),
		kv(types.KeyPlacementInstance("ddd"), "node-1"),
		// Two empty nodes
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	// Should produce rebalance moves (up to maxRebalanceMovesPerCycle=2)
	moveCount := 0
	for _, change := range changes {
		if change.Type == store.OpPut && strings.HasPrefix(change.Key, types.ScanPlacements) &&
			strings.HasPrefix(change.Group, "rebalance/") {
			moveCount++
		}
	}
	if moveCount != maxRebalanceMovesPerCycle {
		t.Errorf("expected %d rebalance placements, got %d", maxRebalanceMovesPerCycle, moveCount)
	}
}

func TestRebalanceSkipsBalancedCluster(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 1 instance per node — balanced
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-2"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "rebalance/") {
			t.Error("balanced cluster should not produce rebalance changes")
		}
	}
}

func TestRebalanceSkipsGroupedInstances(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 3 grouped instances on node-1, node-2 empty
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "api"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-1"),
		kv(types.KeyObservedInstanceService("ccc"), "db"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
		kv(types.KeyPlacementInstance("ccc"), "node-1"),
		// All services belong to the same group
		kv(types.ScanDesiredGroups+"mygroup/process/web", ""),
		kv(types.ScanDesiredGroups+"mygroup/process/api", ""),
		kv(types.ScanDesiredGroups+"mygroup/process/db", ""),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "rebalance/") {
			t.Error("grouped instances should not be rebalanced individually")
		}
	}
}

func TestRebalanceRespectsConstraints(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 4 instances on node-1 (amd64), node-2 is arm64
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-1"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
		kv(types.KeyPlacementInstance("ccc"), "node-1"),
		kv(types.KeyObservedInstanceService("ddd"), "web"),
		kv(types.KeyObservedInstanceState("ddd"), "running"),
		kv(types.KeyPlacementInstance("ddd"), "node-1"),
		// web requires amd64
		kv(types.ScanDesiredServices+"web/placement/architecture", "amd64"),
		// node-1 is amd64, node-2 is arm64
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.ScanObservedNodes+"node-1/architecture", "amd64"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.ScanObservedNodes+"node-2/architecture", "arm64"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "rebalance/") {
			t.Error("should not rebalance to incompatible architecture node")
		}
	}
}

func TestRebalanceMaxMovesPerCycle(t *testing.T) {
	placementScheduler := newTestScheduler()
	// 8 instances on node-1, node-2 and node-3 empty
	var factEntries []struct{ k, v string }
	for index := 0; index < 8; index++ {
		instanceID := fmt.Sprintf("inst-%02d", index)
		factEntries = append(factEntries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyPlacementInstance(instanceID), "node-1"),
		)
	}
	factEntries = append(factEntries,
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	facts := buildFacts(factEntries...)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	moveCount := 0
	for _, change := range changes {
		if change.Type == store.OpPut && strings.HasPrefix(change.Key, types.ScanPlacements) &&
			strings.HasPrefix(change.Group, "rebalance/") {
			moveCount++
		}
	}
	if moveCount != maxRebalanceMovesPerCycle {
		t.Errorf("expected exactly %d moves per cycle, got %d", maxRebalanceMovesPerCycle, moveCount)
	}
}

func TestRebalanceMoveTargetsLeastLoaded(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 4 on node-1, 1 on node-2, 0 on node-3
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-1"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
		kv(types.KeyPlacementInstance("ccc"), "node-1"),
		kv(types.KeyObservedInstanceService("ddd"), "web"),
		kv(types.KeyObservedInstanceState("ddd"), "running"),
		kv(types.KeyPlacementInstance("ddd"), "node-1"),
		kv(types.KeyObservedInstanceService("eee"), "web"),
		kv(types.KeyObservedInstanceState("eee"), "running"),
		kv(types.KeyPlacementInstance("eee"), "node-2"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	changes, _ := placementScheduler.Reconcile(context.Background(), facts)
	// First move should go to node-3 (0 instances), second to node-2 (1 instance).
	// After first move: node-1=3, node-2=1, node-3=1 → second targets node-2 or node-3.
	targets := make(map[string]int)
	for _, change := range changes {
		if change.Type == store.OpPut && strings.HasPrefix(change.Key, types.ScanPlacements) &&
			strings.HasPrefix(change.Group, "rebalance/") && !strings.Contains(change.Key, "/node/") {
			targets[string(change.Value)]++
		}
	}
	if targets["node-3"] == 0 {
		t.Error("first rebalance move should target the empty node-3")
	}
	if targets["node-1"] > 0 {
		t.Error("should never move instances to the overloaded source node")
	}
}

func TestPerNodePlacement(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "agent"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyObservedInstanceService("bbb"), "agent"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "agent"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
		kv(types.KeyDesiredServicePlacementMode("agent"), "per-node"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	placements := filterPlacementChanges(changes)

	nodeSet := make(map[string]bool)
	for _, change := range placements {
		if strings.HasPrefix(change.Key, "placement/instance/") {
			nodeSet[string(change.Value)] = true
		}
	}
	if len(nodeSet) != 3 {
		t.Errorf("expected instances on 3 distinct nodes, got %d: %v", len(nodeSet), nodeSet)
	}
}

func TestPerNodeSkipsOccupiedNode(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// Existing instance already placed on node-1
		kv(types.KeyObservedInstanceService("existing"), "agent"),
		kv(types.KeyObservedInstanceState("existing"), "running"),
		kv(types.KeyPlacementInstance("existing"), "node-1"),
		// New pending instance
		kv(types.KeyObservedInstanceService("new-one"), "agent"),
		kv(types.KeyObservedInstanceState("new-one"), "pending"),
		kv(types.KeyDesiredServicePlacementMode("agent"), "per-node"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	placements := filterPlacementChanges(changes)

	for _, change := range placements {
		if change.Key == types.KeyPlacementInstance("new-one") {
			if string(change.Value) == "node-1" {
				t.Error("per-node placement should not place on node-1 (already occupied)")
			}
			return
		}
	}
	t.Error("expected placement for new-one, got none")
}

func TestPerNodeSkipsRebalance(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// 3 per-node instances, all on node-1 (extreme imbalance)
		kv(types.KeyObservedInstanceService("aaa"), "agent"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyPlacementInstance("aaa"), "node-1"),
		kv(types.KeyObservedInstanceService("bbb"), "agent"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyPlacementInstance("bbb"), "node-1"),
		kv(types.KeyObservedInstanceService("ccc"), "agent"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
		kv(types.KeyPlacementInstance("ccc"), "node-1"),
		kv(types.KeyDesiredServicePlacementMode("agent"), "per-node"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
	)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "rebalance/") {
			t.Error("per-node services should not be rebalanced")
		}
	}
}

func TestPreemptionHigherPriorityPreempts(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		// One node with 1000m CPU, running instance uses 1000m leaving no room
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),

		kv(types.KeyObservedInstanceService("running-low"), "cache"),
		kv(types.KeyObservedInstanceState("running-low"), "running"),
		kv(types.KeyPlacementInstance("running-low"), "node-1"),
		kv(types.KeyPlacementNodeInstance("node-1", "running-low"), ""),
		kv(types.KeyDesiredServicePriority("cache"), "100"),
		kv(types.KeyDesiredServiceResourcesCPU("cache"), "1000"),

		// High-priority pending instance that needs 500m CPU
		kv(types.KeyObservedInstanceService("pending-high"), "web"),
		kv(types.KeyObservedInstanceState("pending-high"), "pending"),
		kv(types.KeyDesiredServicePriority("web"), "800"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "500"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	foundPreemption := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("running-low") {
			foundPreemption = true
		}
	}
	if !foundPreemption {
		t.Error("expected preemption of low-priority instance running-low")
	}
}

func TestPreemptionEqualPriorityNoPreemption(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedInstanceService("running-a"), "cache"),
		kv(types.KeyObservedInstanceState("running-a"), "running"),
		kv(types.KeyPlacementInstance("running-a"), "node-1"),
		kv(types.KeyPlacementNodeInstance("node-1", "running-a"), ""),
		kv(types.KeyDesiredServicePriority("cache"), "500"),
		kv(types.KeyDesiredServiceResourcesCPU("cache"), "1000"),

		kv(types.KeyObservedInstanceService("pending-b"), "web"),
		kv(types.KeyObservedInstanceState("pending-b"), "pending"),
		kv(types.KeyDesiredServicePriority("web"), "500"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "500"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("running-a") {
			t.Error("equal-priority instances should not preempt each other")
		}
	}
}

func TestPreemptionZeroPriorityNoPreemption(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedInstanceService("running-a"), "cache"),
		kv(types.KeyObservedInstanceState("running-a"), "running"),
		kv(types.KeyPlacementInstance("running-a"), "node-1"),
		kv(types.KeyPlacementNodeInstance("node-1", "running-a"), ""),
		kv(types.KeyDesiredServiceResourcesCPU("cache"), "1000"),

		kv(types.KeyObservedInstanceService("pending-b"), "web"),
		kv(types.KeyObservedInstanceState("pending-b"), "pending"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "500"),
		// web has no priority set (default 0)
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("running-a") {
			t.Error("priority-0 service should not preempt")
		}
	}
}

func TestPreemptionSkipsPerNodeVictims(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),

		// Per-node running instance consuming all CPU
		kv(types.KeyObservedInstanceService("daemon-inst"), "monitor"),
		kv(types.KeyObservedInstanceState("daemon-inst"), "running"),
		kv(types.KeyPlacementInstance("daemon-inst"), "node-1"),
		kv(types.KeyPlacementNodeInstance("node-1", "daemon-inst"), ""),
		kv(types.KeyDesiredServicePlacementMode("monitor"), "per-node"),
		kv(types.KeyDesiredServicePriority("monitor"), "100"),
		kv(types.KeyDesiredServiceResourcesCPU("monitor"), "1000"),

		// High-priority pending
		kv(types.KeyObservedInstanceService("pending-high"), "critical"),
		kv(types.KeyObservedInstanceState("pending-high"), "pending"),
		kv(types.KeyDesiredServicePriority("critical"), "900"),
		kv(types.KeyDesiredServiceResourcesCPU("critical"), "500"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("daemon-inst") {
			t.Error("per-node service instances should never be preempted")
		}
	}
}

func TestPreemptionSkipsStatefulVictims(t *testing.T) {
	placementScheduler := newTestScheduler()
	facts := buildFacts(
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),

		// Stateful running instance consuming all CPU
		kv(types.KeyObservedInstanceService("db-inst"), "postgres"),
		kv(types.KeyObservedInstanceState("db-inst"), "running"),
		kv(types.KeyPlacementInstance("db-inst"), "node-1"),
		kv(types.KeyPlacementNodeInstance("node-1", "db-inst"), ""),
		kv(types.KeyDesiredServiceStateful("postgres"), "true"),
		kv(types.KeyDesiredServicePriority("postgres"), "100"),
		kv(types.KeyDesiredServiceResourcesCPU("postgres"), "1000"),

		// High-priority pending
		kv(types.KeyObservedInstanceService("pending-high"), "critical"),
		kv(types.KeyObservedInstanceState("pending-high"), "pending"),
		kv(types.KeyDesiredServicePriority("critical"), "900"),
		kv(types.KeyDesiredServiceResourcesCPU("critical"), "500"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("db-inst") {
			t.Error("stateful service instances should never be preempted")
		}
	}
}

func TestPreemptionMaxCap(t *testing.T) {
	placementScheduler := newTestScheduler()

	// 5 nodes each with 1000m CPU, fully consumed by low-priority running instances
	entries := []struct{ k, v string }{}
	for nodeIndex := 1; nodeIndex <= 5; nodeIndex++ {
		nodeID := "node-" + strconv.Itoa(nodeIndex)
		instanceID := "low-" + strconv.Itoa(nodeIndex)
		entries = append(entries,
			kv(types.KeyObservedNodeState(nodeID), "alive"),
			kv(types.KeyObservedNodeAvailableCPU(nodeID), "1000"),
			kv(types.KeyObservedInstanceService(instanceID), "filler"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyPlacementInstance(instanceID), nodeID),
			kv(types.KeyPlacementNodeInstance(nodeID, instanceID), ""),
		)
	}
	entries = append(entries,
		kv(types.KeyDesiredServicePriority("filler"), "50"),
		kv(types.KeyDesiredServiceResourcesCPU("filler"), "1000"),
	)

	// 5 high-priority pending instances that need CPU
	for pendingIndex := 1; pendingIndex <= 5; pendingIndex++ {
		instanceID := "high-" + strconv.Itoa(pendingIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "critical"),
			kv(types.KeyObservedInstanceState(instanceID), "pending"),
		)
	}
	entries = append(entries,
		kv(types.KeyDesiredServicePriority("critical"), "900"),
		kv(types.KeyDesiredServiceResourcesCPU("critical"), "500"),
	)

	facts := buildFacts(entries...)
	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	preemptionCount := 0
	for _, change := range changes {
		if strings.HasPrefix(change.Key, "derived/instance/") &&
			strings.HasSuffix(change.Key, "/controller_stopped") {
			preemptionCount++
		}
	}
	if preemptionCount > maxPreemptionsPerCycle {
		t.Errorf("preemptions should be capped at %d, got %d", maxPreemptionsPerCycle, preemptionCount)
	}
	if preemptionCount != maxPreemptionsPerCycle {
		t.Errorf("expected exactly %d preemptions (5 pending, 5 victims available), got %d",
			maxPreemptionsPerCycle, preemptionCount)
	}
}

func TestColocatePrefersNodeWithTargetService(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		// Pending instance of "web" needs placement.
		kv(types.KeyObservedInstanceService("web-001"), "web"),
		kv(types.KeyObservedInstanceState("web-001"), "pending"),
		// web wants to colocate with redis.
		kv(types.KeyDesiredServicePlacementColocate("web", "redis"), ""),
		// node-1 has a running redis instance.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedInstanceService("redis-001"), "redis"),
		kv(types.KeyObservedInstanceState("redis-001"), "running"),
		kv(types.KeyPlacementInstance("redis-001"), "node-1"),
		// node-2 has a different service (equal load to node-1).
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedInstanceService("other-001"), "other"),
		kv(types.KeyObservedInstanceState("other-001"), "running"),
		kv(types.KeyPlacementInstance("other-001"), "node-2"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should colocate on node-1 (has redis), got %s", changes[0].Value)
	}
}

func TestSeparateAvoidsNodeWithTargetService(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-001"), "web"),
		kv(types.KeyObservedInstanceState("web-001"), "pending"),
		// web wants to separate from redis.
		kv(types.KeyDesiredServicePlacementSeparate("web", "redis"), ""),
		// node-1 has a running redis instance.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedInstanceService("redis-001"), "redis"),
		kv(types.KeyObservedInstanceState("redis-001"), "running"),
		kv(types.KeyPlacementInstance("redis-001"), "node-1"),
		// node-2 has no redis.
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should separate to node-2 (no redis), got %s", changes[0].Value)
	}
}

func TestColocateFallsBackToLeastLoaded(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-001"), "web"),
		kv(types.KeyObservedInstanceState("web-001"), "pending"),
		// web wants to colocate with redis, but no node has redis.
		kv(types.KeyDesiredServicePlacementColocate("web", "redis"), ""),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	// Both score 0 affinity, falls back to least-loaded (first alphabetically).
	if string(changes[0].Value) != "node-1" {
		t.Errorf("should fall back to least-loaded node-1, got %s", changes[0].Value)
	}
}

func TestColocateAndSeparateCombined(t *testing.T) {
	placementScheduler := newTestScheduler()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-001"), "web"),
		kv(types.KeyObservedInstanceState("web-001"), "pending"),
		// web wants to colocate with redis and separate from logging.
		kv(types.KeyDesiredServicePlacementColocate("web", "redis"), ""),
		kv(types.KeyDesiredServicePlacementSeparate("web", "logging"), ""),
		// node-1 has redis AND logging → affinity: +1 - 1 = 0
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedInstanceService("redis-001"), "redis"),
		kv(types.KeyObservedInstanceState("redis-001"), "running"),
		kv(types.KeyPlacementInstance("redis-001"), "node-1"),
		kv(types.KeyObservedInstanceService("log-001"), "logging"),
		kv(types.KeyObservedInstanceState("log-001"), "running"),
		kv(types.KeyPlacementInstance("log-001"), "node-1"),
		// node-2 has redis only → affinity: +1 - 0 = +1 (best)
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedInstanceService("redis-002"), "redis"),
		kv(types.KeyObservedInstanceState("redis-002"), "running"),
		kv(types.KeyPlacementInstance("redis-002"), "node-2"),
		// node-3 has a filler service (equal load) → affinity: 0 - 0 = 0
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedInstanceService("filler-001"), "filler"),
		kv(types.KeyObservedInstanceState("filler-001"), "running"),
		kv(types.KeyPlacementInstance("filler-001"), "node-3"),
	)

	changes, err := placementScheduler.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	changes = filterPlacementChanges(changes)
	if len(changes) != 1 {
		t.Fatalf("expected 1 placement, got %d", len(changes))
	}
	if string(changes[0].Value) != "node-2" {
		t.Errorf("should pick node-2 (redis +1, no logging), got %s", changes[0].Value)
	}
}
