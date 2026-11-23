// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// storedUnplacedRequirementsJSON is the requirements value a previous cycle
// stored for web-1 (cpu 2000m, the service's request).
const storedUnplacedRequirementsJSON = `{"cpu":2000,"memory":0,"service":"web"}`

// unplacedLifetimeFacts returns web's cpu 2000m request, one alive node with
// the given cpu, web-1's stored unplaced reason and requirements, and the
// given instance facts (none: web-1 is gone from the store).
func unplacedLifetimeFacts(nodeAvailableCPU string, instanceFacts ...store.Fact) []store.Fact {
	facts := buildFacts(
		kv(types.KeyDesiredService("web"), ""),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "2000m"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), nodeAvailableCPU),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
		kv(types.KeyDerivedSchedulerUnplacedReason("web-1"), string(types.UnplacedInsufficientCapacity)),
		kv(types.KeyDerivedSchedulerUnplacedRequirements("web-1"), storedUnplacedRequirementsJSON),
	)
	facts = append(facts, instanceFacts...)
	store.SortFacts(facts)
	return facts
}

// factOf builds one store fact from a key and a string value.
func factOf(key string, value string) store.Fact {
	return store.Fact{Key: key, Value: []byte(value)}
}

// assertUnplacedFactsDeleted fails the test unless the changes delete both
// of the instance's unplaced-demand facts and put neither of them.
func assertUnplacedFactsDeleted(t *testing.T, changes []controllers.Change, instanceID string) {
	t.Helper()
	unplacedKeys := []string{
		types.KeyDerivedSchedulerUnplacedReason(instanceID),
		types.KeyDerivedSchedulerUnplacedRequirements(instanceID),
	}
	for _, unplacedKey := range unplacedKeys {
		deleted := false
		for _, change := range changes {
			if change.Key != unplacedKey {
				continue
			}
			if change.Type == store.OpPut {
				t.Errorf("cycle put %s=%q, want a delete", unplacedKey, change.Value)
			}
			deleted = deleted || change.Type == store.OpDelete
		}
		if !deleted {
			t.Errorf("cycle proposed no delete of %s", unplacedKey)
		}
	}
}

// TestSchedulerDeletesUnplacedFactsOfStoppedInstance: M262 1.1. web-1 has
// stored unplaced facts but is stopped, or pending with the controller's stop
// marker; one cycle deletes its reason and requirements.
func TestSchedulerDeletesUnplacedFactsOfStoppedInstance(t *testing.T) {
	testCases := []struct {
		name          string       // name describes how web-1 stopped waiting.
		instanceFacts []store.Fact // instanceFacts are web-1's observed and derived facts.
	}{
		{
			name: "state stopped",
			instanceFacts: []store.Fact{
				factOf(types.KeyObservedInstanceService("web-1"), "web"),
				factOf(types.KeyObservedInstanceState("web-1"), string(types.InstanceStopped)),
			},
		},
		{
			name: "pending with controller_stopped",
			instanceFacts: []store.Fact{
				factOf(types.KeyObservedInstanceService("web-1"), "web"),
				factOf(types.KeyObservedInstanceState("web-1"), string(types.InstancePending)),
				factOf(types.KeyDerivedInstanceControllerStopped("web-1"), "true"),
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			changes, reconcileError := newTestScheduler().Reconcile(context.Background(),
				unplacedLifetimeFacts("1000", testCase.instanceFacts...))
			if reconcileError != nil {
				t.Fatal(reconcileError)
			}
			assertUnplacedFactsDeleted(t, changes, "web-1")
		})
	}
}

// TestSchedulerDeletesUnplacedFactsOfRemovedInstance: M262 1.2. web-1's
// unplaced facts are stored but no observed/instance/web-1/* fact exists; one
// cycle deletes both facts.
func TestSchedulerDeletesUnplacedFactsOfRemovedInstance(t *testing.T) {
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), unplacedLifetimeFacts("1000"))
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	assertUnplacedFactsDeleted(t, changes, "web-1")
}

// TestSchedulerDeletesUnplacedFactsOfPlacedInstance: M262 1.3, pins the
// behaviour before M262. web-1 has stored unplaced facts and a stored
// placement, or is placed in this cycle; both facts are deleted.
func TestSchedulerDeletesUnplacedFactsOfPlacedInstance(t *testing.T) {
	pendingWebInstance := []store.Fact{
		factOf(types.KeyObservedInstanceService("web-1"), "web"),
		factOf(types.KeyObservedInstanceState("web-1"), string(types.InstancePending)),
	}
	testCases := []struct {
		name             string       // name describes where web-1's placement comes from.
		nodeAvailableCPU string       // nodeAvailableCPU is node-1's cpu in millicores.
		instanceFacts    []store.Fact // instanceFacts are web-1's facts, a stored placement included.
		wantPlacement    bool         // wantPlacement is whether the cycle must place web-1.
	}{
		{
			name:             "stored placement",
			nodeAvailableCPU: "1000",
			instanceFacts:    append([]store.Fact{factOf(types.KeyPlacementInstance("web-1"), "node-1")}, pendingWebInstance...),
		},
		{
			name:             "placed in this cycle",
			nodeAvailableCPU: "4000",
			instanceFacts:    pendingWebInstance,
			wantPlacement:    true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			changes, reconcileError := newTestScheduler().Reconcile(context.Background(),
				unplacedLifetimeFacts(testCase.nodeAvailableCPU, testCase.instanceFacts...))
			if reconcileError != nil {
				t.Fatal(reconcileError)
			}
			if _, placed := putValueOf(changes, types.KeyPlacementInstance("web-1")); placed != testCase.wantPlacement {
				t.Fatalf("placement of web-1 in this cycle = %v, want %v", placed, testCase.wantPlacement)
			}
			assertUnplacedFactsDeleted(t, changes, "web-1")
		})
	}
}
