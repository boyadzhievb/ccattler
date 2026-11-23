// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// unplacedIDRange is a half-open range [first, end) of zero-padded instance
// indexes with a name prefix, e.g. {"stopped", 0, 20} is stopped-00..stopped-19.
type unplacedIDRange struct {
	prefix string // prefix is the instance name before the index.
	first  int    // first is the lowest index in the range.
	end    int    // end is one past the highest index.
}

// unplacedKeysOf returns the reason and requirements keys of every instance in
// the range, as a set.
func unplacedKeysOf(idRange unplacedIDRange) map[string]bool {
	keys := make(map[string]bool)
	for index := idRange.first; index < idRange.end; index++ {
		instanceID := fmt.Sprintf("%s-%02d", idRange.prefix, index)
		keys[types.KeyDerivedSchedulerUnplacedReason(instanceID)] = true
		keys[types.KeyDerivedSchedulerUnplacedRequirements(instanceID)] = true
	}
	return keys
}

// unplacedBudgetFacts returns web (cpu 2000m) and one alive node, stoppedCount
// stopped instances with stored unplaced facts, newCount pending instances
// with none, and, when placeable, a node large enough for one pending web-1.
func unplacedBudgetFacts(stoppedCount int, newCount int, placeable bool) []store.Fact {
	nodeAvailableCPU := "1000"
	if placeable {
		nodeAvailableCPU = "4000"
	}
	facts := buildFacts(
		kv(types.KeyDesiredService("web"), ""),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "2000m"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), nodeAvailableCPU),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	)
	appendInstance := func(instanceID string, state types.InstanceState) {
		facts = append(facts,
			factOf(types.KeyObservedInstanceService(instanceID), "web"),
			factOf(types.KeyObservedInstanceState(instanceID), string(state)))
	}
	if placeable {
		appendInstance("web-1", types.InstancePending)
	}
	for index := 0; index < stoppedCount; index++ {
		instanceID := fmt.Sprintf("stopped-%02d", index)
		appendInstance(instanceID, types.InstanceStopped)
		facts = append(facts,
			factOf(types.KeyDerivedSchedulerUnplacedReason(instanceID), string(types.UnplacedInsufficientCapacity)),
			factOf(types.KeyDerivedSchedulerUnplacedRequirements(instanceID), storedUnplacedRequirementsJSON))
	}
	for index := 0; index < newCount; index++ {
		appendInstance(fmt.Sprintf("new-%02d", index), types.InstancePending)
	}
	store.SortFacts(facts)
	return facts
}

// unplacedKeysChanged returns the keys under the unplaced prefix that the
// changes put, and those they delete, as two sets.
func unplacedKeysChanged(changes []controllers.Change) (map[string]bool, map[string]bool) {
	putKeys, deleteKeys := make(map[string]bool), make(map[string]bool)
	for _, change := range changes {
		if !strings.HasPrefix(change.Key, types.ScanDerivedSchedulerUnplaced) {
			continue
		}
		if change.Type == store.OpDelete {
			deleteKeys[change.Key] = true
		} else {
			putKeys[change.Key] = true
		}
	}
	return putKeys, deleteKeys
}

// applyChangesToFacts returns the facts with the changes' puts and deletes
// applied, sorted: the store a following cycle reads.
func applyChangesToFacts(facts []store.Fact, changes []controllers.Change) []store.Fact {
	valueByKey := make(map[string][]byte, len(facts))
	for _, fact := range facts {
		valueByKey[fact.Key] = fact.Value
	}
	for _, change := range changes {
		if change.Type == store.OpDelete {
			delete(valueByKey, change.Key)
		} else {
			valueByKey[change.Key] = change.Value
		}
	}
	appliedFacts := make([]store.Fact, 0, len(valueByKey))
	for key, value := range valueByKey {
		appliedFacts = append(appliedFacts, store.Fact{Key: key, Value: value})
	}
	store.SortFacts(appliedFacts)
	return appliedFacts
}

// assertUnplacedKeySet fails the test unless got and want hold the same keys.
func assertUnplacedKeySet(t *testing.T, label string, got map[string]bool, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d unplaced keys, want %d", label, len(got), len(want))
	}
	for key := range want {
		if !got[key] {
			t.Errorf("%s: missing %s", label, key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("%s: unexpected %s", label, key)
		}
	}
}

// marshalWebRequirementsJSON returns the requirements value the scheduler
// writes for a web instance (cpu 2000m), so a stored copy reads as unchanged.
func marshalWebRequirementsJSON(t *testing.T) string {
	t.Helper()
	requirementsJSON, marshalError := json.Marshal(unplacedDemandRequirements{Service: "web", CPU: 2000})
	if marshalError != nil {
		t.Fatal(marshalError)
	}
	return string(requirementsJSON)
}

// TestSchedulerBudgetsUnplacedFactChanges: M262 3a.1 (D4), spec requirement
// "Unplaced-fact changes are budgeted per scheduler cycle". Puts and deletes
// share maxUnplacedFactChangesPerCycle, puts first, whole instances in ID
// order; a following cycle (first cycle's changes applied) does the next part.
func TestSchedulerBudgetsUnplacedFactChanges(t *testing.T) {
	testCases := []struct {
		name              string          // name is the spec scenario's sub-case.
		stoppedCount      int             // stoppedCount is how many stopped instances keep stored facts.
		newCount          int             // newCount is how many new pending instances fit no node.
		placeable         bool            // placeable adds a pending web-1 that fits node-1.
		wantPuts          unplacedIDRange // wantPuts are the instances the first cycle writes.
		wantDeletes       unplacedIDRange // wantDeletes are the instances the first cycle cleans.
		wantSecondPuts    unplacedIDRange // wantSecondPuts are written by the second cycle.
		wantSecondDeletes unplacedIDRange // wantSecondDeletes are cleaned by the second cycle.
		storedFacts       []store.Fact    // storedFacts are unplaced facts a previous cycle wrote for new instances.
		wantExtraPuts     []string        // wantExtraPuts are single keys the first cycle writes beyond wantPuts.
	}{
		{
			name: "deletes only", stoppedCount: 50, placeable: true,
			wantDeletes:       unplacedIDRange{"stopped", 0, 20},
			wantSecondDeletes: unplacedIDRange{"stopped", 20, 40},
		},
		{
			name: "puts first", stoppedCount: 30, newCount: 15,
			wantPuts:          unplacedIDRange{"new", 0, 15},
			wantDeletes:       unplacedIDRange{"stopped", 0, 5},
			wantSecondDeletes: unplacedIDRange{"stopped", 5, 25},
		},
		{
			name: "puts over budget", newCount: 30,
			wantPuts:       unplacedIDRange{"new", 0, 20},
			wantSecondPuts: unplacedIDRange{"new", 20, 30},
		},
		{
			// new-00 costs 1 (only its reason changed), new-01..19 cost 2:
			// 39 puts; new-20 (2 more) would exceed 40, so neither of its
			// puts is written until the second cycle. A per-change budget
			// would write new-20's reason in the first cycle.
			name: "whole instances", newCount: 21,
			storedFacts: []store.Fact{
				factOf(types.KeyDerivedSchedulerUnplacedReason("new-00"), string(types.UnplacedNoNodes)),
				factOf(types.KeyDerivedSchedulerUnplacedRequirements("new-00"), marshalWebRequirementsJSON(t)),
			},
			wantExtraPuts:  []string{types.KeyDerivedSchedulerUnplacedReason("new-00")},
			wantPuts:       unplacedIDRange{"new", 1, 20},
			wantSecondPuts: unplacedIDRange{"new", 20, 21},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			facts := append(unplacedBudgetFacts(testCase.stoppedCount, testCase.newCount, testCase.placeable),
				testCase.storedFacts...)
			store.SortFacts(facts)
			firstChanges, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
			if reconcileError != nil {
				t.Fatal(reconcileError)
			}
			if _, placed := putValueOf(firstChanges, types.KeyPlacementInstance("web-1")); placed != testCase.placeable {
				t.Errorf("placement of web-1 proposed = %v, want %v", placed, testCase.placeable)
			}
			putKeys, deleteKeys := unplacedKeysChanged(firstChanges)
			wantFirstPutKeys := unplacedKeysOf(testCase.wantPuts)
			for _, extraPutKey := range testCase.wantExtraPuts {
				wantFirstPutKeys[extraPutKey] = true
			}
			assertUnplacedKeySet(t, "first cycle puts", putKeys, wantFirstPutKeys)
			assertUnplacedKeySet(t, "first cycle deletes", deleteKeys, unplacedKeysOf(testCase.wantDeletes))

			secondChanges, reconcileError := newTestScheduler().Reconcile(context.Background(),
				applyChangesToFacts(facts, firstChanges))
			if reconcileError != nil {
				t.Fatal(reconcileError)
			}
			putKeys, deleteKeys = unplacedKeysChanged(secondChanges)
			assertUnplacedKeySet(t, "second cycle puts", putKeys, unplacedKeysOf(testCase.wantSecondPuts))
			assertUnplacedKeySet(t, "second cycle deletes", deleteKeys, unplacedKeysOf(testCase.wantSecondDeletes))
		})
	}
}
