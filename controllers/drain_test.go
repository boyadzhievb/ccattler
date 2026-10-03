package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

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
	evictedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			evictedCount++
		}
	}
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction per cycle (rate-limited by service), got %d", evictedCount)
	}

	// The first instance alphabetically (inst-1) should be evicted.
	foundEviction := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedInstanceState("inst-1") && string(change.Value) == string(types.InstanceStopped) {
			foundEviction = true
		}
	}
	if !foundEviction {
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
	evictionCount := 0
	drainCompleteWritten := false
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			evictionCount++
		}
		if change.Key == types.KeyDerivedNodeDrainComplete("node-1") {
			drainCompleteWritten = true
		}
	}
	if evictionCount != 0 {
		t.Errorf("expected no evictions for stopped/failed instances, got %d", evictionCount)
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
	evictedServices := make(map[string]int)
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			// Extract the instance ID from the key to find which service it belongs to.
			for _, instanceID := range []string{"a-inst-1", "a-inst-2", "b-inst-1", "b-inst-2"} {
				if change.Key == types.KeyObservedInstanceState(instanceID) {
					serviceName := ""
					for _, factEntry := range inputFacts {
						if factEntry.Key == types.KeyObservedInstanceService(instanceID) {
							serviceName = string(factEntry.Value)
						}
					}
					evictedServices[serviceName]++
				}
			}
		}
	}

	if evictedServices["svc-a"] != 1 {
		t.Errorf("expected 1 eviction for svc-a, got %d", evictedServices["svc-a"])
	}
	if evictedServices["svc-b"] != 1 {
		t.Errorf("expected 1 eviction for svc-b, got %d", evictedServices["svc-b"])
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

	evictedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			evictedCount++
		}
	}
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

	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			t.Error("expected no evictions when at min_available, but got one")
		}
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

	evictedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			evictedCount++
		}
	}
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

	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			t.Error("expected no evictions when at max_unavailable limit, but got one")
		}
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

	evictedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			evictedCount++
		}
	}
	if evictedCount != 1 {
		t.Errorf("expected 1 eviction (no budget), got %d", evictedCount)
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
