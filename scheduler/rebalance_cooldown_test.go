// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rebalanceCooldownTestNow is the fixed clock of the cooldown tests.
var rebalanceCooldownTestNow = time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)

// reconcileAtCooldownTestTime runs one scheduler cycle on facts with the clock
// fixed at rebalanceCooldownTestNow and returns its changes.
func reconcileAtCooldownTestTime(t *testing.T, facts []store.Fact) []controllers.Change {
	t.Helper()
	placementScheduler := newTestScheduler()
	placementScheduler.currentTime = func() time.Time { return rebalanceCooldownTestNow }
	changes, reconcileError := placementScheduler.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	return changes
}

// TestRebalanceCooldownKeepsMovedInstanceInPlace: spec instance-rebalancing, "A
// moved instance is not moved again within the cooldown". web-00 was moved 1
// minute ago (stays) or 11 minutes ago (may move again); every move records
// its time in derived/scheduler/rebalanced/{id} in the move's change group.
func TestRebalanceCooldownKeepsMovedInstanceInPlace(t *testing.T) {
	testCases := []struct {
		name          string        // name describes how long ago web-00 was moved
		movedAgo      time.Duration // movedAgo is the age of web-00's rebalanced fact
		wantWeb00Move bool          // wantWeb00Move is whether web-00 may be moved in this cycle
	}{
		{name: "within cooldown", movedAgo: time.Minute, wantWeb00Move: false},
		{name: "after cooldown", movedAgo: rebalanceMoveCooldown + time.Minute, wantWeb00Move: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			movedAt := rebalanceCooldownTestNow.Add(-testCase.movedAgo).Format(time.RFC3339)
			facts := buildRebalanceFitFacts(6, "8192", kv(types.KeyDerivedSchedulerRebalanced("web-00"), movedAt))
			changes := reconcileAtCooldownTestTime(t, facts)
			movedInstanceIDs := rebalanceMovesTo(changes, "node-2")
			if len(movedInstanceIDs) == 0 {
				t.Fatalf("no instance moved to node-2; the test setup has no imbalance")
			}
			if movedWeb00 := slices.Contains(movedInstanceIDs, "web-00"); movedWeb00 != testCase.wantWeb00Move {
				t.Errorf("moved %v: web-00 moved = %v, want %v (moved %v ago, cooldown %v)",
					movedInstanceIDs, movedWeb00, testCase.wantWeb00Move, testCase.movedAgo, rebalanceMoveCooldown)
			}
			for _, movedInstanceID := range movedInstanceIDs {
				wantChange := controllers.Change{
					Type: store.OpPut, Key: types.KeyDerivedSchedulerRebalanced(movedInstanceID),
					Value: []byte(rebalanceCooldownTestNow.Format(time.RFC3339)), Group: "rebalance/" + movedInstanceID,
				}
				recorded := slices.ContainsFunc(changes, func(change controllers.Change) bool {
					return change.Type == wantChange.Type && change.Key == wantChange.Key &&
						string(change.Value) == string(wantChange.Value) && change.Group == wantChange.Group
				})
				if !recorded {
					t.Errorf("move of %s does not record %s = %s in group %s", movedInstanceID, wantChange.Key, wantChange.Value, wantChange.Group)
				}
			}
		})
	}
}

// TestRebalanceCooldownFactDeletedWithInstance: spec instance-rebalancing, "A
// moved instance is not moved again within the cooldown". The fact of an
// instance that is gone is deleted (M262-style cleanup); a live instance's
// fact stays, also without an imbalance; the scheduler watches the prefix.
func TestRebalanceCooldownFactDeletedWithInstance(t *testing.T) {
	if !slices.Contains(newTestScheduler().Watch(), types.ScanDerivedSchedulerRebalanced) {
		t.Errorf("scheduler does not watch %s, so it never reads its cooldown facts", types.ScanDerivedSchedulerRebalanced)
	}
	movedAt := rebalanceCooldownTestNow.Add(-time.Minute).Format(time.RFC3339)
	facts := buildRebalanceFitFacts(1, "8192",
		kv(types.KeyDerivedSchedulerRebalanced("web-00"), movedAt),
		kv(types.KeyDerivedSchedulerRebalanced("gone-1"), movedAt))
	changes := reconcileAtCooldownTestTime(t, facts)
	deletedKeys := make(map[string]bool)
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deletedKeys[change.Key] = true
		}
	}
	if !deletedKeys[types.KeyDerivedSchedulerRebalanced("gone-1")] {
		t.Errorf("the cooldown fact of gone-1, an instance that no longer exists, was not deleted")
	}
	if deletedKeys[types.KeyDerivedSchedulerRebalanced("web-00")] {
		t.Errorf("the cooldown fact of web-00, a live instance, was deleted")
	}
}
