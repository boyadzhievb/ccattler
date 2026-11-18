// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// creationChangesOf returns the changes of the creation group of instanceID.
func creationChangesOf(proposedChanges []Change, instanceID string) map[string]Change {
	groupChanges := make(map[string]Change)
	for _, change := range proposedChanges {
		if change.Group == "instance-create/"+instanceID {
			groupChanges[change.Key] = change
		}
	}
	return groupChanges
}

// reconcileInstanceTestFacts runs an instance controller with sequential IDs.
func reconcileInstanceTestFacts(t *testing.T, inputFacts []store.Fact) []Change {
	t.Helper()
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()
	proposedChanges, reconcileError := instanceController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	return proposedChanges
}

// TestInstanceControllerRecordsImageOnCreate: spec instance-restart, "New
// instance gets the desired image", on the stateless and stateful paths.
func TestInstanceControllerRecordsImageOnCreate(t *testing.T) {
	testCases := []struct {
		name       string       // name describes the creation path.
		facts      []store.Fact // facts are the controller input.
		instanceID string       // instanceID is the instance the controller creates.
	}{
		{
			name: "stateless",
			facts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("web"), "1"),
				kv(types.KeyDesiredServiceImage("web"), "app:b"),
			),
			instanceID: "inst-001",
		},
		{
			name: "stateful ordinal",
			facts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("web"), "1"),
				kv(types.KeyEffectiveServiceStateful("web"), "true"),
				kv(types.KeyDesiredServiceImage("web"), "app:b"),
			),
			instanceID: "web-0",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			groupChanges := creationChangesOf(reconcileInstanceTestFacts(t, testCase.facts), testCase.instanceID)
			if len(groupChanges) == 0 {
				t.Fatalf("no creation group for %s", testCase.instanceID)
			}
			imageChange, hasImage := groupChanges[types.KeyDerivedInstanceImage(testCase.instanceID)]
			if !hasImage || imageChange.Type != store.OpPut || string(imageChange.Value) != "app:b" {
				t.Errorf("creation group of %s does not record image app:b: %+v", testCase.instanceID, groupChanges)
			}
		})
	}
}

// TestInstanceControllerClearsRestartsOnRecreate: spec instance-restart,
// "Stateful ordinal recreated" (and a stateless ID that has a stale count).
func TestInstanceControllerClearsRestartsOnRecreate(t *testing.T) {
	testCases := []struct {
		name       string       // name describes the creation path.
		facts      []store.Fact // facts are the controller input.
		instanceID string       // instanceID is the instance the controller creates again.
	}{
		{
			name: "stateful ordinal",
			facts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("db"), "1"),
				kv(types.KeyEffectiveServiceStateful("db"), "true"),
				kv(types.KeyDesiredServiceImage("db"), "postgres:17"),
				kv(types.KeyObservedInstance("db-0"), ""),
				kv(types.KeyObservedInstanceService("db-0"), "db"),
				kv(types.KeyObservedInstanceState("db-0"), string(types.InstanceStopped)),
				kv(types.KeyObservedInstanceOrdinal("db-0"), "0"),
				kv(types.KeyObservedInstanceRestarts("db-0"), "7"),
			),
			instanceID: "db-0",
		},
		{
			name: "stateless",
			facts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("web"), "1"),
				kv(types.KeyDesiredServiceImage("web"), "app:b"),
				kv(types.KeyObservedInstanceRestarts("inst-001"), "7"),
			),
			instanceID: "inst-001",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			groupChanges := creationChangesOf(reconcileInstanceTestFacts(t, testCase.facts), testCase.instanceID)
			if len(groupChanges) == 0 {
				t.Fatalf("no creation group for %s", testCase.instanceID)
			}
			restartsChange, hasRestarts := groupChanges[types.KeyObservedInstanceRestarts(testCase.instanceID)]
			if !hasRestarts || restartsChange.Type != store.OpDelete {
				t.Errorf("creation group of %s does not delete the restart count: %+v", testCase.instanceID, groupChanges)
			}
		})
	}
}
