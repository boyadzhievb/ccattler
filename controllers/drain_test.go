// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// countEvictionMarkers returns how many drain eviction markers are in the
// proposed changes, and optionally returns the set of evicted instance IDs.
func countEvictionMarkers(proposedChanges []Change) (int, map[string]bool) {
	evictedInstances := make(map[string]bool)
	for _, change := range proposedChanges {
		if strings.Contains(change.Key, "/drain/evict/") && string(change.Value) == "true" {
			parts := strings.Split(change.Key, "/drain/evict/")
			if len(parts) == 2 {
				evictedInstances[parts[1]] = true
			}
		}
	}
	return len(evictedInstances), evictedInstances
}

func TestDrainControllerEvictsInstancesFromDrainingNode(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is draining with 3 running instances of the same service.
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-3"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	// Rate-limited: only 1 instance per service per cycle should be evicted.
	evictedCount, evictedInstances := countEvictionMarkers(proposedChanges)
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction per cycle (rate-limited by service), got %d", evictedCount)
	}

	// The first instance alphabetically (inst-1) should be evicted.
	if !evictedInstances["inst-1"] {
		t.Error("expected inst-1 (first alphabetically) to be evicted")
	}
}

func TestDrainControllerSkipsAliveNodes(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is alive — instances should not be touched.
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(proposedChanges) != 0 {
		t.Errorf("expected no changes for alive node, got %d", len(proposedChanges))
	}
}

func TestDrainControllerSkipsAlreadyStoppedInstances(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is draining but its only instance is already stopped.
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		// Also include a failed instance — should also be skipped.
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("failed")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	// No active instances means no evictions, but we should see a drain-complete marker.
	evictedCount, _ := countEvictionMarkers(proposedChanges)
	drainCompleteWritten := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedNodeDrainComplete("node-1") {
			drainCompleteWritten = true
		}
	}
	if evictedCount != 0 {
		t.Errorf("expected no evictions for stopped/failed instances, got %d", evictedCount)
	}
	if !drainCompleteWritten {
		t.Error("expected drain-complete marker when zero active instances remain")
	}
}

func TestDrainControllerRateLimitsByService(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is draining with 2 instances of service A and 2 of service B.
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("a-inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("a-inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("a-inst-1"), Value: []byte("svc-a")},
		{Key: types.KeyPlacementInstance("a-inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("a-inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("a-inst-2"), Value: []byte("svc-a")},
		{Key: types.KeyPlacementInstance("b-inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("b-inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("b-inst-1"), Value: []byte("svc-b")},
		{Key: types.KeyPlacementInstance("b-inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("b-inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("b-inst-2"), Value: []byte("svc-b")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	// Rate-limiting is per-service: should evict 1 of svc-a + 1 of svc-b = 2 total.
	_, evictedInstances := countEvictionMarkers(proposedChanges)

	svcACount := 0
	svcBCount := 0
	for instanceID := range evictedInstances {
		if strings.HasPrefix(instanceID, "a-") {
			svcACount++
		}
		if strings.HasPrefix(instanceID, "b-") {
			svcBCount++
		}
	}

	if svcACount != 1 {
		t.Errorf("expected 1 eviction for svc-a, got %d", svcACount)
	}
	if svcBCount != 1 {
		t.Errorf("expected 1 eviction for svc-b, got %d", svcBCount)
	}
}

func TestDrainControllerWritesDrainCompleteMarker(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is draining with zero active instances (all already stopped).
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	drainCompleteWritten := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedNodeDrainComplete("node-1") && string(change.Value) == "true" {
			drainCompleteWritten = true
		}
	}
	if !drainCompleteWritten {
		t.Error("expected drain-complete marker to be written when no active instances remain")
	}
}

func TestDrainControllerDoesNotDuplicateCompleteMarker(t *testing.T) {
	drainController := NewDrainController()

	inputFacts := []store.Fact{
		// node-1 is draining, zero active instances, and complete marker already exists.
		{Key: types.KeyDerivedNodeDrainComplete("node-1"), Value: []byte("true")},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedNodeDrainComplete("node-1") {
			t.Error("should not re-write drain-complete marker when it already exists")
		}
	}
}

func TestDrainControllerRespectsMinAvailableBudget(t *testing.T) {
	drainController := NewDrainController()

	// 5 instances total: 2 on draining node-1, 3 on healthy node-2.
	// Disruption budget: min_available 4.
	// Only 1 can be evicted (5-1=4 >= 4), second eviction blocked (4-1=3 < 4).
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("5")},
		{Key: types.KeyDesiredServiceDisruptionMinAvailable("web"), Value: []byte("4")},
		// Instances on draining node-1.
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		// Instances on healthy node-2.
		{Key: types.KeyPlacementInstance("inst-3"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-4"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-4"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-4"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-5"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-5"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-5"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	evictedCount, _ := countEvictionMarkers(proposedChanges)
	// Rate-limit already caps at 1 per service per cycle, and the budget allows
	// exactly 1 eviction (5-1=4 >= min_available 4).
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction (budget allows it), got %d", evictedCount)
	}
}

func TestDrainControllerBlocksEvictionWhenAtMinAvailable(t *testing.T) {
	drainController := NewDrainController()

	// 3 instances total: 1 on draining node-1, 2 on healthy node-2.
	// Disruption budget: min_available 3.
	// Evicting would drop to 2, violating the budget — must block.
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("3")},
		{Key: types.KeyDesiredServiceDisruptionMinAvailable("web"), Value: []byte("3")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-3"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	evictedCount, _ := countEvictionMarkers(proposedChanges)
	if evictedCount != 0 {
		t.Errorf("expected no evictions when at min_available, got %d", evictedCount)
	}
}

func TestDrainControllerRespectsMaxUnavailableBudget(t *testing.T) {
	drainController := NewDrainController()

	// 5 desired, 4 currently running (1 already unavailable).
	// Disruption budget: max_unavailable 2.
	// 1 already unavailable, so 1 more eviction allowed (1+1=2 <= 2).
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("5")},
		{Key: types.KeyDesiredServiceDisruptionMaxUnavailable("web"), Value: []byte("2")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-3"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-4"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-4"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-4"), Value: []byte("web")},
		// inst-5 is stopped/failed — already unavailable.
		{Key: types.KeyPlacementInstance("inst-5"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-5"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-5"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	evictedCount, _ := countEvictionMarkers(proposedChanges)
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction (max_unavailable budget allows 1 more), got %d", evictedCount)
	}
}

func TestDrainControllerBlocksEvictionAtMaxUnavailable(t *testing.T) {
	drainController := NewDrainController()

	// 5 desired, 3 currently running (2 already unavailable).
	// Disruption budget: max_unavailable 2.
	// 2 already unavailable, so no more evictions allowed.
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("5")},
		{Key: types.KeyDesiredServiceDisruptionMaxUnavailable("web"), Value: []byte("2")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-3"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
		// 2 instances already unavailable.
		{Key: types.KeyPlacementInstance("inst-4"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-4"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-4"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-5"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("inst-5"), Value: []byte("stopped")},
		{Key: types.KeyObservedInstanceService("inst-5"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	evictedCount, _ := countEvictionMarkers(proposedChanges)
	if evictedCount != 0 {
		t.Errorf("expected no evictions when at max_unavailable limit, got %d", evictedCount)
	}
}

func TestDrainControllerNoBudgetAllowsEviction(t *testing.T) {
	drainController := NewDrainController()

	// No disruption budget — eviction should proceed normally.
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	evictedCount, _ := countEvictionMarkers(proposedChanges)
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction (no budget), got %d", evictedCount)
	}
}

func TestDrainControllerSkipsAlreadyEvictedInstances(t *testing.T) {
	drainController := NewDrainController()

	// node-1 is draining with 2 running instances, but inst-1 already has
	// an eviction marker from a prior cycle.
	inputFacts := []store.Fact{
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("draining")},
		{Key: types.KeyDerivedNodeDrainEvict("node-1", "inst-1"), Value: []byte("true")},
		{Key: types.KeyPlacementInstance("inst-1"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyPlacementInstance("inst-2"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := drainController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	_, evictedInstances := countEvictionMarkers(proposedChanges)
	if evictedInstances["inst-1"] {
		t.Error("should not re-evict inst-1 which already has an eviction marker")
	}
	if !evictedInstances["inst-2"] {
		t.Error("expected inst-2 to be evicted (no prior marker)")
	}
}

func TestDrainControllerImplementsInterface(t *testing.T) {
	var _ Controller = NewDrainController()
}

func TestDrainControllerName(t *testing.T) {
	drainController := NewDrainController()
	if drainController.Name() != "drain" {
		t.Errorf("name: got %s, want drain", drainController.Name())
	}
}
