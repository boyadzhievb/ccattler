// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestEvictionControllerEvictsBestEffortFirst verifies that under memory
// pressure, BestEffort instances are evicted before Guaranteed instances.
func TestEvictionControllerEvictsBestEffortFirst(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 95% utilization: capacity 1Gi, available ~50Mi
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "50Mi"),

		// Two instances on the node
		kv(types.KeyObservedInstanceNode("inst-a"), "node01"),
		kv(types.KeyObservedInstanceService("inst-a"), "cache"),
		kv(types.KeyObservedInstanceState("inst-a"), "running"),

		kv(types.KeyObservedInstanceNode("inst-b"), "node01"),
		kv(types.KeyObservedInstanceService("inst-b"), "db"),
		kv(types.KeyObservedInstanceState("inst-b"), "running"),

		// QoS classes
		kv(types.KeyDerivedServiceQoSClass("cache"), "BestEffort"),
		kv(types.KeyDerivedServiceQoSClass("db"), "Guaranteed"),

		// Memory requests
		kv(types.KeyDesiredServiceResourcesMemory("cache"), "256Mi"),
		kv(types.KeyDesiredServiceResourcesMemory("db"), "512Mi"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Should evict BestEffort (cache) first, plus set pressure marker
	if len(changes) < 1 {
		t.Fatalf("expected at least 1 eviction change, got %d", len(changes))
	}

	// First change should stop the BestEffort instance
	foundCacheStop := false
	foundDBStop := false
	foundPressure := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-a") {
			foundCacheStop = true
		}
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-b") {
			foundDBStop = true
		}
		if change.Key == types.KeyDerivedNodePressure("node01") {
			foundPressure = true
		}
	}

	if !foundCacheStop {
		t.Error("expected BestEffort instance (cache/inst-a) to be evicted")
	}
	if !foundPressure {
		t.Error("expected node pressure marker to be set")
	}
	if foundDBStop {
		t.Error("Guaranteed instance (db/inst-b) should not be evicted when BestEffort is sufficient")
	}
}

// TestEvictionControllerNoEvictionBelowThreshold verifies that no instances
// are evicted when memory utilization is below the hard threshold.
func TestEvictionControllerNoEvictionBelowThreshold(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 70% utilization: capacity 1Gi, available ~300Mi
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "300Mi"),

		// Instance on the node
		kv(types.KeyObservedInstanceNode("inst-a"), "node01"),
		kv(types.KeyObservedInstanceService("inst-a"), "web"),
		kv(types.KeyObservedInstanceState("inst-a"), "running"),

		kv(types.KeyDerivedServiceQoSClass("web"), "BestEffort"),
		kv(types.KeyDesiredServiceResourcesMemory("web"), "128Mi"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes below threshold, got %d", len(changes))
	}
}

// TestEvictionControllerClearsPressureOnRecovery verifies that the pressure
// marker is deleted when node memory drops below the soft threshold.
func TestEvictionControllerClearsPressureOnRecovery(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 75% utilization — below soft threshold
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "256Mi"),

		// Existing pressure marker
		kv(types.KeyDerivedNodePressure("node01"), "memory"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change (pressure delete), got %d", len(changes))
	}
	if changes[0].Key != types.KeyDerivedNodePressure("node01") {
		t.Errorf("expected pressure key deletion, got %s", changes[0].Key)
	}
	if changes[0].Type != store.OpDelete {
		t.Error("expected change to be a deletion")
	}
}

// TestEvictionControllerSkipsNonRunningInstances verifies that instances not
// in "running" state are not considered for eviction.
func TestEvictionControllerSkipsNonRunningInstances(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 95% utilization
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "50Mi"),

		// Pending instance (not running)
		kv(types.KeyObservedInstanceNode("inst-a"), "node01"),
		kv(types.KeyObservedInstanceService("inst-a"), "web"),
		kv(types.KeyObservedInstanceState("inst-a"), "pending"),

		kv(types.KeyDerivedServiceQoSClass("web"), "BestEffort"),
		kv(types.KeyDesiredServiceResourcesMemory("web"), "256Mi"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Only pressure marker, no evictions (nothing running to evict)
	foundEviction := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-a") {
			foundEviction = true
		}
	}
	if foundEviction {
		t.Error("pending instances should not be evicted")
	}
}

// TestEvictionControllerBurstableBeforeGuaranteed verifies the three-tier
// eviction order: BestEffort → Burstable → Guaranteed.
func TestEvictionControllerBurstableBeforeGuaranteed(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 95% utilization
		kv(types.KeyObservedNodeCapacityMemory("node01"), "4Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "100Mi"),

		// Three instances, one per QoS class
		kv(types.KeyObservedInstanceNode("inst-be"), "node01"),
		kv(types.KeyObservedInstanceService("inst-be"), "cache"),
		kv(types.KeyObservedInstanceState("inst-be"), "running"),

		kv(types.KeyObservedInstanceNode("inst-bu"), "node01"),
		kv(types.KeyObservedInstanceService("inst-bu"), "worker"),
		kv(types.KeyObservedInstanceState("inst-bu"), "running"),

		kv(types.KeyObservedInstanceNode("inst-gu"), "node01"),
		kv(types.KeyObservedInstanceService("inst-gu"), "db"),
		kv(types.KeyObservedInstanceState("inst-gu"), "running"),

		kv(types.KeyDerivedServiceQoSClass("cache"), "BestEffort"),
		kv(types.KeyDerivedServiceQoSClass("worker"), "Burstable"),
		kv(types.KeyDerivedServiceQoSClass("db"), "Guaranteed"),

		// Small memory requests so multiple evictions needed
		kv(types.KeyDesiredServiceResourcesMemory("cache"), "256Mi"),
		kv(types.KeyDesiredServiceResourcesMemory("worker"), "512Mi"),
		kv(types.KeyDesiredServiceResourcesMemory("db"), "1Gi"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Find eviction order
	evictionOrder := []string{}
	for _, change := range changes {
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-be") {
			evictionOrder = append(evictionOrder, "BestEffort")
		}
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-bu") {
			evictionOrder = append(evictionOrder, "Burstable")
		}
		if change.Key == types.KeyDerivedInstanceControllerStopped("inst-gu") {
			evictionOrder = append(evictionOrder, "Guaranteed")
		}
	}

	if len(evictionOrder) < 2 {
		t.Fatalf("expected at least 2 evictions, got %d", len(evictionOrder))
	}
	if evictionOrder[0] != "BestEffort" {
		t.Errorf("expected BestEffort evicted first, got %s", evictionOrder[0])
	}
	if evictionOrder[1] != "Burstable" {
		t.Errorf("expected Burstable evicted second, got %s", evictionOrder[1])
	}
}

// TestEvictionControllerNoChangeInDeadZone verifies that utilization between
// the soft and hard thresholds produces no changes (unless pressure exists).
func TestEvictionControllerNoChangeInDeadZone(t *testing.T) {
	evictionController := NewEvictionController()

	facts := buildFacts(
		// Node at 85% utilization — between soft (80) and hard (90)
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1Gi"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "153Mi"),
	)

	changes, err := evictionController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes in dead zone (between soft and hard thresholds), got %d", len(changes))
	}
}
