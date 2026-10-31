// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// fixedTime returns a deterministic time function for GC tests.
func fixedTime(baseTime time.Time) func() time.Time {
	return func() time.Time { return baseTime }
}

// TestGCDeletesStoppedInstanceWhenServiceConverged verifies that a stopped
// instance is garbage collected when the service has enough running replacements
// and the retention period has elapsed.
func TestGCDeletesStoppedInstanceWhenServiceConverged(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 5 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	oldCreatedAt := now.Add(-10 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		// Stopped old instance.
		kv(types.KeyObservedInstance("old-abc"), ""),
		kv(types.KeyObservedInstanceService("old-abc"), "web"),
		kv(types.KeyObservedInstanceState("old-abc"), "failed"),
		kv(types.KeyObservedInstanceNode("old-abc"), "node01"),
		kv(types.KeyObservedInstanceCreatedAt("old-abc"), oldCreatedAt),
		kv(types.KeyObservedInstanceIP("old-abc"), "10.100.1.5"),
		kv(types.KeyDerivedInstanceControllerStopped("old-abc"), "true"),
		kv(types.KeyPlacementInstance("old-abc"), "node01"),

		// Running replacement.
		kv(types.KeyObservedInstance("new-def"), ""),
		kv(types.KeyObservedInstanceService("new-def"), "web"),
		kv(types.KeyObservedInstanceState("new-def"), "running"),
		kv(types.KeyObservedInstanceCreatedAt("new-def"), now.Add(-2*time.Minute).Format(time.RFC3339)),

		// Desired count: 1.
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) == 0 {
		t.Fatal("expected deletion changes for stopped instance, got none")
	}

	deletedKeys := make(map[string]bool)
	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("expected Delete op, got %v for key %s", change.Type, change.Key)
		}
		deletedKeys[change.Key] = true
	}

	expectedKeys := []string{
		types.KeyObservedInstance("old-abc"),
		types.KeyObservedInstanceService("old-abc"),
		types.KeyObservedInstanceState("old-abc"),
		types.KeyObservedInstanceNode("old-abc"),
		types.KeyObservedInstanceCreatedAt("old-abc"),
		types.KeyObservedInstanceIP("old-abc"),
		types.KeyDerivedInstanceControllerStopped("old-abc"),
		types.KeyPlacementInstance("old-abc"),
		types.KeyPlacementNodeInstance("node01", "old-abc"),
	}
	for _, expectedKey := range expectedKeys {
		if !deletedKeys[expectedKey] {
			t.Errorf("expected key to be deleted: %s", expectedKey)
		}
	}
}

// TestGCSkipsInstanceWithinRetentionPeriod verifies that stopped instances
// created recently (within the retention window) are not collected.
func TestGCSkipsInstanceWithinRetentionPeriod(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 10 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	recentCreatedAt := now.Add(-3 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyObservedInstance("recent-abc"), ""),
		kv(types.KeyObservedInstanceService("recent-abc"), "web"),
		kv(types.KeyObservedInstanceState("recent-abc"), "failed"),
		kv(types.KeyObservedInstanceCreatedAt("recent-abc"), recentCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("recent-abc"), "true"),

		kv(types.KeyObservedInstance("running-def"), ""),
		kv(types.KeyObservedInstanceService("running-def"), "web"),
		kv(types.KeyObservedInstanceState("running-def"), "running"),

		kv(types.KeyEffectiveServiceInstances("web"), "1"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes for instance within retention, got %d", len(changes))
	}
}

// TestGCSkipsWhenServiceNotConverged verifies that stopped instances are
// retained when the service does not have enough running instances yet.
func TestGCSkipsWhenServiceNotConverged(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 1 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	oldCreatedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyObservedInstance("old-abc"), ""),
		kv(types.KeyObservedInstanceService("old-abc"), "web"),
		kv(types.KeyObservedInstanceState("old-abc"), "failed"),
		kv(types.KeyObservedInstanceCreatedAt("old-abc"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("old-abc"), "true"),

		// No running instances for this service.
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes when service not converged, got %d", len(changes))
	}
}

// TestGCRespectsMaxDeletionsPerCycle verifies that the controller does not
// exceed the per-cycle deletion budget.
func TestGCRespectsMaxDeletionsPerCycle(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 1 * time.Minute
	gcController.MaxDeletionsPerCycle = 2
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	oldCreatedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		// Three stopped instances.
		kv(types.KeyObservedInstance("old-a"), ""),
		kv(types.KeyObservedInstanceService("old-a"), "web"),
		kv(types.KeyObservedInstanceState("old-a"), "stopped"),
		kv(types.KeyObservedInstanceCreatedAt("old-a"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("old-a"), "true"),

		kv(types.KeyObservedInstance("old-b"), ""),
		kv(types.KeyObservedInstanceService("old-b"), "web"),
		kv(types.KeyObservedInstanceState("old-b"), "stopped"),
		kv(types.KeyObservedInstanceCreatedAt("old-b"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("old-b"), "true"),

		kv(types.KeyObservedInstance("old-c"), ""),
		kv(types.KeyObservedInstanceService("old-c"), "web"),
		kv(types.KeyObservedInstanceState("old-c"), "stopped"),
		kv(types.KeyObservedInstanceCreatedAt("old-c"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("old-c"), "true"),

		// Three running replacements.
		kv(types.KeyObservedInstance("run-a"), ""),
		kv(types.KeyObservedInstanceService("run-a"), "web"),
		kv(types.KeyObservedInstanceState("run-a"), "running"),

		kv(types.KeyObservedInstance("run-b"), ""),
		kv(types.KeyObservedInstanceService("run-b"), "web"),
		kv(types.KeyObservedInstanceState("run-b"), "running"),

		kv(types.KeyObservedInstance("run-c"), ""),
		kv(types.KeyObservedInstanceService("run-c"), "web"),
		kv(types.KeyObservedInstanceState("run-c"), "running"),

		kv(types.KeyEffectiveServiceInstances("web"), "3"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	// Count distinct instance trees deleted by checking group prefixes.
	deletedInstances := make(map[string]bool)
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "gc-") {
			deletedInstances[change.Group] = true
		}
	}

	if len(deletedInstances) != 2 {
		t.Errorf("expected 2 instance trees deleted (max budget), got %d", len(deletedInstances))
	}
}

// TestGCCollectsDeletedServiceInstances verifies that instances for a service
// with no desired count (service deleted) are garbage collected.
func TestGCCollectsDeletedServiceInstances(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 1 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	oldCreatedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyObservedInstance("orphan-abc"), ""),
		kv(types.KeyObservedInstanceService("orphan-abc"), "deleted-svc"),
		kv(types.KeyObservedInstanceState("orphan-abc"), "stopped"),
		kv(types.KeyObservedInstanceCreatedAt("orphan-abc"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("orphan-abc"), "true"),
		// No effective/service/deleted-svc fact — service is gone.
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) == 0 {
		t.Fatal("expected deletion changes for orphan instance of deleted service")
	}

	foundMarkerDelete := false
	for _, change := range changes {
		if change.Key == types.KeyObservedInstance("orphan-abc") && change.Type == store.OpDelete {
			foundMarkerDelete = true
		}
	}
	if !foundMarkerDelete {
		t.Error("expected observed instance marker to be deleted")
	}
}

// TestGCCollectsOrphanedInstancesWithNoService verifies that instances with
// no service field (orphaned by partial GC or etcd corruption) are collected
// regardless of their state.
func TestGCCollectsOrphanedInstancesWithNoService(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 1 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := buildFacts(
		// Orphaned instance: marker key exists but no service field.
		kv(types.KeyObservedInstance("553f0b9f3b"), ""),
		kv(types.KeyObservedInstanceState("553f0b9f3b"), ""),

		// Second orphan with init progress but no service.
		kv(types.KeyObservedInstance("664a918921"), ""),
		kv(types.KeyObservedInstanceState("664a918921"), "pending"),
		kv(types.KeyObservedInstanceInitProgress("664a918921"), `{"step":0,"totalSteps":3}`),

		// Normal running instance — should NOT be collected.
		kv(types.KeyObservedInstance("running-abc"), ""),
		kv(types.KeyObservedInstanceService("running-abc"), "web"),
		kv(types.KeyObservedInstanceState("running-abc"), "running"),

		kv(types.KeyEffectiveServiceInstances("web"), "1"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	deletedInstances := make(map[string]bool)
	for _, change := range changes {
		if strings.HasPrefix(change.Group, "gc-") {
			deletedInstances[change.Group] = true
		}
	}

	if !deletedInstances["gc-553f0b9f3b"] {
		t.Error("expected orphaned instance 553f0b9f3b to be collected")
	}
	if !deletedInstances["gc-664a918921"] {
		t.Error("expected orphaned instance 664a918921 to be collected")
	}
	if deletedInstances["gc-running-abc"] {
		t.Error("normal running instance should not be collected")
	}
}

// TestGCNoChangesWhenNothingToCollect verifies the controller produces no
// changes when all instances are either running or pending.
func TestGCNoChangesWhenNothingToCollect(t *testing.T) {
	gcController := NewInstanceGCController()
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	facts := buildFacts(
		kv(types.KeyObservedInstance("inst-a"), ""),
		kv(types.KeyObservedInstanceService("inst-a"), "web"),
		kv(types.KeyObservedInstanceState("inst-a"), "running"),

		kv(types.KeyObservedInstance("inst-b"), ""),
		kv(types.KeyObservedInstanceService("inst-b"), "web"),
		kv(types.KeyObservedInstanceState("inst-b"), "pending"),

		kv(types.KeyEffectiveServiceInstances("web"), "2"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes, got %d", len(changes))
	}
}

// TestGCGroupsChangesPerInstance verifies that all deletion changes for a
// single instance share the same group ID, ensuring atomicity.
func TestGCGroupsChangesPerInstance(t *testing.T) {
	gcController := NewInstanceGCController()
	gcController.RetentionPeriod = 1 * time.Minute
	now := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	gcController.NowFunc = fixedTime(now)

	oldCreatedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyObservedInstance("old-abc"), ""),
		kv(types.KeyObservedInstanceService("old-abc"), "web"),
		kv(types.KeyObservedInstanceState("old-abc"), "stopped"),
		kv(types.KeyObservedInstanceNode("old-abc"), "node01"),
		kv(types.KeyObservedInstanceCreatedAt("old-abc"), oldCreatedAt),
		kv(types.KeyDerivedInstanceControllerStopped("old-abc"), "true"),

		kv(types.KeyObservedInstance("running-def"), ""),
		kv(types.KeyObservedInstanceService("running-def"), "web"),
		kv(types.KeyObservedInstanceState("running-def"), "running"),

		kv(types.KeyEffectiveServiceInstances("web"), "1"),
	)

	changes, reconcileError := gcController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	for _, change := range changes {
		if change.Group != "gc-old-abc" {
			t.Errorf("expected group 'gc-old-abc', got %q for key %s", change.Group, change.Key)
		}
	}
}
