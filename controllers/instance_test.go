// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func seqIDGen() types.IDFunc {
	idCounter := 0
	return func() string {
		idCounter++
		return fmt.Sprintf("inst-%03d", idCounter)
	}
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

func TestScaleUpFromZero(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "3"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// 3 instances × 3 keys each (marker, service, state)
	if len(changes) != 9 {
		t.Fatalf("expected 9 changes, got %d", len(changes))
	}

	// Verify the three instances were created with sequential IDs.
	for i := 0; i < 3; i++ {
		base := i * 3
		id := fmt.Sprintf("inst-%03d", i+1)

		if changes[base].Key != types.KeyObservedInstance(id) {
			t.Errorf("change %d: got key %s, want %s", base, changes[base].Key, types.KeyObservedInstance(id))
		}
		if changes[base+1].Key != types.KeyObservedInstanceService(id) {
			t.Errorf("change %d: got key %s, want %s", base+1, changes[base+1].Key, types.KeyObservedInstanceService(id))
		}
		if string(changes[base+1].Value) != "web" {
			t.Errorf("change %d: service = %s, want web", base+1, changes[base+1].Value)
		}
		if string(changes[base+2].Value) != "pending" {
			t.Errorf("change %d: state = %s, want pending", base+2, changes[base+2].Value)
		}
	}
}

func TestAlreadySatisfied(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes when satisfied, got %d", len(changes))
	}
}

func TestScaleUpPartial(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "5"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Need 2 more instances × 3 keys each
	if len(changes) != 6 {
		t.Fatalf("expected 6 changes (2 new instances), got %d", len(changes))
	}
}

func TestScaleDown(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Remove 2 instances (set state to stopped)
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop 2 instances), got %d", len(changes))
	}

	for _, ch := range changes {
		if ch.Type != store.OpPut {
			t.Errorf("expected OpPut (state update), got %d", ch.Type)
		}
		if string(ch.Value) != "stopped" {
			t.Errorf("expected stopped, got %s", ch.Value)
		}
	}
}

func TestScaleDownPrefersPending(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}

	// Both stopped instances should be the pending ones (bbb, ccc), not aaa (running).
	stoppedKeys := make(map[string]bool)
	for _, ch := range changes {
		stoppedKeys[ch.Key] = true
	}
	if stoppedKeys[types.KeyObservedInstanceState("aaa")] {
		t.Error("should not stop running instance aaa when pending instances exist")
	}
	if !stoppedKeys[types.KeyObservedInstanceState("bbb")] {
		t.Error("should stop pending instance bbb")
	}
	if !stoppedKeys[types.KeyObservedInstanceState("ccc")] {
		t.Error("should stop pending instance ccc")
	}
}

func TestStoppedInstancesNotCounted(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "stopped"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// bbb is stopped so not counted → actual=1, desired=2 → create 1
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (1 new instance), got %d", len(changes))
	}
}

func TestMultipleServices(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyEffectiveServiceInstances("api"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// web: need 1 more (3 keys), api: need 1 (3 keys) = 6 total
	if len(changes) != 6 {
		t.Fatalf("expected 6 changes (2 new instances across 2 services), got %d", len(changes))
	}

	services := make(map[string]int)
	for _, ch := range changes {
		if string(ch.Value) == "web" || string(ch.Value) == "api" {
			services[string(ch.Value)]++
		}
	}
	if services["web"] != 1 {
		t.Errorf("expected 1 new web instance, got %d", services["web"])
	}
	if services["api"] != 1 {
		t.Errorf("expected 1 new api instance, got %d", services["api"])
	}
}

func TestDesiredZeroScalesDownAll(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "0"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop all), got %d", len(changes))
	}
	for _, ch := range changes {
		if string(ch.Value) != "stopped" {
			t.Errorf("expected stopped, got %s", ch.Value)
		}
	}
}

func TestControllerInterface(t *testing.T) {
	instanceController := NewInstanceController()

	var _ Controller = instanceController

	if instanceController.Name() != "instance" {
		t.Fatalf("name: got %s, want instance", instanceController.Name())
	}
	if len(instanceController.Watch()) != 3 {
		t.Fatalf("expected 3 watch prefixes, got %d", len(instanceController.Watch()))
	}
}

func TestStatefulOrdinalReusedAfterControllerStopped(t *testing.T) {
	instanceController := NewInstanceController()

	// Setup: stateful service "db" wants 2 instances. db-0 is running.
	// db-1 was replaced (controller_stopped=true) and its observed state is
	// still "running" (agent hasn't cleaned it up yet). The ordinal gap at 1
	// should trigger recreation, and the stale controller_stopped marker
	// must be deleted in the same atomic group.
	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("db"), "2"),
		kv(types.KeyEffectiveServiceStateful("db"), "true"),
		// db-0: running and healthy
		kv(types.KeyObservedInstance("db-0"), ""),
		kv(types.KeyObservedInstanceService("db-0"), "db"),
		kv(types.KeyObservedInstanceState("db-0"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceOrdinal("db-0"), "0"),
		// db-1: observed running but controller_stopped marker makes effective state "stopped"
		kv(types.KeyObservedInstance("db-1"), ""),
		kv(types.KeyObservedInstanceService("db-1"), "db"),
		kv(types.KeyObservedInstanceState("db-1"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceOrdinal("db-1"), "1"),
		kv(types.KeyDerivedInstanceControllerStopped("db-1"), "true"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatalf("unexpected error: %v", reconcileErr)
	}

	// Expect: recreation of db-1 (4 put keys) + deletion of stale marker (1 delete).
	if len(changes) == 0 {
		t.Fatal("expected changes to recreate db-1, got none")
	}

	// All changes must share the same group.
	groupID := changes[0].Group
	if groupID == "" {
		t.Fatal("expected grouped changes, got ungrouped")
	}
	for _, change := range changes {
		if change.Group != groupID {
			t.Fatalf("expected all changes in group %s, got %s", groupID, change.Group)
		}
	}

	// Must include a delete for the stale controller_stopped marker.
	hasMarkerDelete := false
	hasStateWrite := false
	for _, change := range changes {
		if change.Type == store.OpDelete && change.Key == types.KeyDerivedInstanceControllerStopped("db-1") {
			hasMarkerDelete = true
		}
		if change.Key == types.KeyObservedInstanceState("db-1") && string(change.Value) == string(types.InstancePending) {
			hasStateWrite = true
		}
	}
	if !hasMarkerDelete {
		t.Fatal("expected delete of stale controller_stopped marker for db-1")
	}
	if !hasStateWrite {
		t.Fatal("expected state=pending write for db-1 recreation")
	}
}

func TestStatefulOrdinalReusedClearsBothMarkers(t *testing.T) {
	instanceController := NewInstanceController()

	// db-1 was on a failed node: NodeFailureController wrote node_failure=true,
	// FailureController wrote controller_stopped=true. Both markers exist.
	// The InstanceController should recreate db-1 and delete both markers
	// atomically.
	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("db"), "2"),
		kv(types.KeyEffectiveServiceStateful("db"), "true"),
		kv(types.KeyObservedInstance("db-0"), ""),
		kv(types.KeyObservedInstanceService("db-0"), "db"),
		kv(types.KeyObservedInstanceState("db-0"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceOrdinal("db-0"), "0"),
		kv(types.KeyObservedInstance("db-1"), ""),
		kv(types.KeyObservedInstanceService("db-1"), "db"),
		kv(types.KeyObservedInstanceState("db-1"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceOrdinal("db-1"), "1"),
		kv(types.KeyDerivedInstanceControllerStopped("db-1"), "true"),
		kv(types.KeyDerivedInstanceNodeFailure("db-1"), "true"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatalf("unexpected error: %v", reconcileErr)
	}

	hasStoppedDelete := false
	hasFailureDelete := false
	for _, change := range changes {
		if change.Type == store.OpDelete && change.Key == types.KeyDerivedInstanceControllerStopped("db-1") {
			hasStoppedDelete = true
		}
		if change.Type == store.OpDelete && change.Key == types.KeyDerivedInstanceNodeFailure("db-1") {
			hasFailureDelete = true
		}
	}
	if !hasStoppedDelete {
		t.Fatal("expected delete of stale controller_stopped marker for db-1")
	}
	if !hasFailureDelete {
		t.Fatal("expected delete of stale node_failure marker for db-1")
	}
}

func TestStatelessCreationsAreGrouped(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "3"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatalf("unexpected error: %v", reconcileErr)
	}

	// 3 instances × 3 keys each = 9 changes.
	if len(changes) != 9 {
		t.Fatalf("expected 9 changes, got %d", len(changes))
	}

	// Each group of 3 should share a group ID.
	groups := make(map[string]int)
	for _, change := range changes {
		if change.Group == "" {
			t.Fatal("expected all changes to have a group ID")
		}
		groups[change.Group]++
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	for groupID, keyCount := range groups {
		if keyCount != 3 {
			t.Fatalf("group %s has %d keys, expected 3", groupID, keyCount)
		}
	}
}

func TestFairShareCreationAcrossServices(testHandle *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()
	instanceController.MaxCreationsPerCycle = 6

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("svc-a"), "10"),
		kv(types.KeyEffectiveServiceInstances("svc-b"), "10"),
		kv(types.KeyEffectiveServiceInstances("svc-c"), "10"),
	)

	changes, reconcileError := instanceController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testHandle.Fatalf("unexpected error: %v", reconcileError)
	}

	creationsByService := make(map[string]int)
	for _, change := range changes {
		if change.Type == store.OpPut && string(change.Value) == string(types.InstancePending) {
			serviceName := extractServiceFromInstanceChanges(change.Key, changes)
			creationsByService[serviceName]++
		}
	}

	for _, serviceName := range []string{"svc-a", "svc-b", "svc-c"} {
		if creationsByService[serviceName] != 2 {
			testHandle.Errorf("expected 2 creations for %s, got %d (fair share of 6 across 3 services)",
				serviceName, creationsByService[serviceName])
		}
	}
}

// extractServiceFromInstanceChanges finds the service association change for
// an instance whose state change key is provided.
func extractServiceFromInstanceChanges(stateKey string, changes []Change) string {
	for _, change := range changes {
		if change.Type == store.OpPut && change.Group != "" {
			for _, other := range changes {
				if other.Group == change.Group && other.Key == stateKey {
					if len(change.Value) > 0 && change.Key != stateKey {
						keyParts := change.Key
						if idx := len(types.ScanObservedInstances); idx < len(keyParts) {
							remainder := keyParts[idx:]
							slashIndex := 0
							for i, char := range remainder {
								if char == '/' {
									slashIndex = i
									break
								}
							}
							if slashIndex > 0 {
								suffix := remainder[slashIndex+1:]
								if suffix == "service" {
									return string(change.Value)
								}
							}
						}
					}
				}
			}
		}
	}
	return ""
}
