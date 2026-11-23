// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rebalanceFitTestMemoryRequest is the memory request of the web service in
// the rebalance fit tests (512 MiB per instance).
const rebalanceFitTestMemoryRequest = "512Mi"

// rebalanceFitTestSourceMemoryMiB is node-1's memory, enough for every web
// instance the tests place on it.
const rebalanceFitTestSourceMemoryMiB = "8192"

// buildRebalanceFitFacts returns facts with sourceInstanceCount running web
// instances (512Mi each, opted in to rebalancing) on node-1 and an empty
// node-2 with targetMemoryMiB of memory, plus any extra facts.
func buildRebalanceFitFacts(sourceInstanceCount int, targetMemoryMiB string, extraFacts ...struct{ k, v string }) []store.Fact {
	entries := []struct{ k, v string }{
		kv(types.KeyDesiredServiceResourcesMemory("web"), rebalanceFitTestMemoryRequest),
		kv(types.KeyDesiredServicePlacementRebalance("web"), "true"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "8000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), rebalanceFitTestSourceMemoryMiB),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "8000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), targetMemoryMiB),
	}
	for instanceIndex := 0; instanceIndex < sourceInstanceCount; instanceIndex++ {
		instanceID := fmt.Sprintf("web-%02d", instanceIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyPlacementInstance(instanceID), "node-1"))
	}
	entries = append(entries, extraFacts...)
	return buildFacts(entries...)
}

// rebalanceMovesTo returns the IDs of the instances whose placement a
// rebalance change moves to targetNodeID.
func rebalanceMovesTo(changes []controllers.Change, targetNodeID string) []string {
	var movedInstanceIDs []string
	for _, change := range changes {
		if change.Type != store.OpPut || string(change.Value) != targetNodeID {
			continue
		}
		instanceID := strings.TrimPrefix(change.Key, types.ScanPlacements)
		if change.Key == types.KeyPlacementInstance(instanceID) && change.Group == "rebalance/"+instanceID {
			movedInstanceIDs = append(movedInstanceIDs, instanceID)
		}
	}
	return movedInstanceIDs
}

// reconcileRebalanceFitFacts runs one scheduler cycle on facts and returns the
// IDs of the instances moved to node-2.
func reconcileRebalanceFitFacts(t *testing.T, facts []store.Fact) []string {
	t.Helper()
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	return rebalanceMovesTo(changes, "node-2")
}

// TestRebalanceSkipsTargetWithoutRoom: spec instance-rebalancing, "Count-
// imbalanced but memory-full target". node-2 has 256 MiB left, less than one
// 512Mi instance, so nothing moves there despite the count imbalance.
func TestRebalanceSkipsTargetWithoutRoom(t *testing.T) {
	movedInstanceIDs := reconcileRebalanceFitFacts(t, buildRebalanceFitFacts(6, "256"))
	if len(movedInstanceIDs) != 0 {
		t.Errorf("moved %v to node-2, which has no room for a %s request", movedInstanceIDs, rebalanceFitTestMemoryRequest)
	}
}

// TestRebalanceSkipsTargetUnderPressure: spec instance-rebalancing, "Target
// under memory pressure". node-2 has room by requests but carries eviction's
// pressure marker, which the scheduler watches.
func TestRebalanceSkipsTargetUnderPressure(t *testing.T) {
	if !slices.Contains(newTestScheduler().Watch(), types.ScanDerivedNodes) {
		t.Errorf("scheduler does not watch %s, so a pressure marker never triggers a cycle", types.ScanDerivedNodes)
	}
	facts := buildRebalanceFitFacts(6, "8192", kv(types.KeyDerivedNodePressure("node-2"), "memory"))
	if movedInstanceIDs := reconcileRebalanceFitFacts(t, facts); len(movedInstanceIDs) != 0 {
		t.Errorf("moved %v to node-2, which is under memory pressure", movedInstanceIDs)
	}
}

// TestRebalanceCountsRoomUsedByEarlierMoves: spec instance-rebalancing, "Two
// moves share one target's room". node-2 has 768 MiB, room for one 512Mi
// instance; the imbalance (8 to 0) would move two in the cycle.
func TestRebalanceCountsRoomUsedByEarlierMoves(t *testing.T) {
	movedInstanceIDs := reconcileRebalanceFitFacts(t, buildRebalanceFitFacts(8, "768"))
	if len(movedInstanceIDs) != 1 {
		t.Errorf("moved %v to node-2, want exactly one (room for one %s instance)", movedInstanceIDs, rebalanceFitTestMemoryRequest)
	}
}

// TestRebalanceMovesToTargetWithRoom: spec instance-rebalancing, "Room
// available". node-2 has room and no pressure marker, so an existing instance
// is moved there under its own ID.
func TestRebalanceMovesToTargetWithRoom(t *testing.T) {
	facts := buildRebalanceFitFacts(6, "8192")
	existingInstanceIDs := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		existingInstanceIDs[strings.TrimPrefix(fact.Key, types.ScanPlacements)] = true
	}
	movedInstanceIDs := reconcileRebalanceFitFacts(t, facts)
	if len(movedInstanceIDs) == 0 {
		t.Fatalf("no instance moved to node-2, which has room")
	}
	for _, movedInstanceID := range movedInstanceIDs {
		if !existingInstanceIDs[movedInstanceID] {
			t.Errorf("moved instance %q is not one of the existing web instances", movedInstanceID)
		}
	}
}

// TestRebalanceCountsRoomTakenByThisCyclePlacements: spec instance-rebalancing,
// "A placement and a move in one cycle share the target's room". node-1 holds 8
// web instances; node-2's remaining room is taken in this cycle by a placement
// (pending api, 512Mi, node-2 has 768 MiB) or by a preemption's requester (api,
// 4864Mi, takes node-2's room after its batch victim stops), so no web moves.
func TestRebalanceCountsRoomTakenByThisCyclePlacements(t *testing.T) {
	testCases := []struct {
		name             string                  // name describes how node-2's room is taken
		targetMemoryMiB  string                  // targetMemoryMiB is node-2's memory
		apiMemoryRequest string                  // apiMemoryRequest is the pending api instance's memory request
		extraFacts       []struct{ k, v string } // extraFacts add the preemption's victim and priority
		wantChangeKey    string                  // wantChangeKey is the change proving the room was taken in this cycle
	}{
		{
			name: "placement", targetMemoryMiB: "768", apiMemoryRequest: rebalanceFitTestMemoryRequest,
			wantChangeKey: types.KeyPlacementInstance("api-00"),
		},
		{
			name: "preemption", targetMemoryMiB: "5120", apiMemoryRequest: "4864Mi",
			extraFacts: []struct{ k, v string }{
				kv(types.KeyDesiredServicePriority("api"), "100"),
				kv(types.KeyDesiredServiceResourcesMemory("batch"), rebalanceFitTestMemoryRequest),
				kv(types.KeyObservedInstanceService("batch-00"), "batch"),
				kv(types.KeyObservedInstanceState("batch-00"), "running"),
				kv(types.KeyPlacementInstance("batch-00"), "node-2"),
			},
			wantChangeKey: types.KeyDerivedInstanceControllerStopped("batch-00"),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			extraFacts := append([]struct{ k, v string }{
				kv(types.KeyDesiredServiceResourcesMemory("api"), testCase.apiMemoryRequest),
				kv(types.KeyObservedInstanceService("api-00"), "api"),
				kv(types.KeyObservedInstanceState("api-00"), "pending"),
			}, testCase.extraFacts...)
			facts := buildRebalanceFitFacts(8, testCase.targetMemoryMiB, extraFacts...)
			changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
			if reconcileError != nil {
				t.Fatal(reconcileError)
			}
			roomTaken := slices.ContainsFunc(changes, func(change controllers.Change) bool {
				return change.Type == store.OpPut && change.Key == testCase.wantChangeKey
			})
			if !roomTaken {
				t.Fatalf("no %s change in this cycle; the test setup does not take node-2's room", testCase.wantChangeKey)
			}
			if movedInstanceIDs := rebalanceMovesTo(changes, "node-2"); len(movedInstanceIDs) != 0 {
				t.Errorf("moved %v to node-2, whose room this cycle's %s already took", movedInstanceIDs, testCase.name)
			}
		})
	}
}

// TestRebalanceDoesNotMovePreemptedVictim: spec instance-rebalancing, "A
// placement and a move in one cycle share the target's room". web-00 on node-1
// is this cycle's preemption victim for api (4608Mi); a move would only churn an
// instance the scheduler already stopped, so another web instance moves instead.
func TestRebalanceDoesNotMovePreemptedVictim(t *testing.T) {
	facts := buildRebalanceFitFacts(8, "768",
		kv(types.KeyDesiredServicePriority("api"), "100"),
		kv(types.KeyDesiredServiceResourcesMemory("api"), "4608Mi"),
		kv(types.KeyObservedInstanceService("api-00"), "api"),
		kv(types.KeyObservedInstanceState("api-00"), "pending"))
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	victimStopped := slices.ContainsFunc(changes, func(change controllers.Change) bool {
		return change.Type == store.OpPut && change.Key == types.KeyDerivedInstanceControllerStopped("web-00")
	})
	if !victimStopped {
		t.Fatalf("web-00 was not preempted; the test setup does not exercise a victim")
	}
	movedInstanceIDs := rebalanceMovesTo(changes, "node-2")
	if slices.Contains(movedInstanceIDs, "web-00") || len(movedInstanceIDs) != 1 {
		t.Errorf("moved %v to node-2, want one instance other than the preempted web-00", movedInstanceIDs)
	}
}
