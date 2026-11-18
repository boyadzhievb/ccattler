// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// TestRolloutDeletesSurgeWhenLastOldInstanceStops: review round 2 M1 (D14).
// The pass that stops the last old instance deletes surge_allowance.
func TestRolloutDeletesSurgeWhenLastOldInstanceStops(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"):             "1",
			types.KeyDerivedServiceRolloutSurgeAllowance("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning},
	)
	proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 1 || stoppedIDs[0] != "old-1" {
		t.Fatalf("stopped %v, want [old-1]", stoppedIDs)
	}
	if !isDeleted(changesByKey, types.KeyDerivedServiceRolloutSurgeAllowance("web")) {
		t.Error("surge_allowance kept when the last old instance is stopped, want it deleted")
	}
}

// TestRolloutBudgetReasonWhenMaxUnavailableIsZero: review round 2 L5. With
// max_unavailable 0 the rollout waits for an extra instance, and says so.
func TestRolloutBudgetReasonWhenMaxUnavailableIsZero(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"):            "3",
			types.KeyDesiredServiceUpdateMaxUnavailable("web"): "0",
			types.KeyDesiredServiceUpdateMaxExtra("web"):       "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
	assertContainsAll(t, "rollout/reason", rolloutReason, "max_unavailable 0", "waiting for an extra instance to become healthy")
}

// TestRolloutRetargetAfterRevertKeepsTargetAsKnownGood: review round 2 L6. A
// revert to a (no previous_image) followed by c keeps a as the known-good
// image, even though most old instances still run the bad b.
func TestRolloutRetargetAfterRevertKeepsTargetAsKnownGood(t *testing.T) {
	revertFacts := mergeFactMaps(rollingFacts(types.RolloutRolling, "", "app:a", time.Minute), map[string]string{
		types.KeyDesiredServiceInstances("web"): "3",
	})
	delete(revertFacts, types.KeyDerivedServiceRolloutImage("web"))
	inputFacts := buildRolloutTestFacts("app:c", revertFacts,
		rolloutTestInstance{instanceID: "web-1", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", image: "app:a", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if previousImage, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutImage("web")); previousImage != "app:a" {
		t.Errorf("previous_image = %q after a retarget from the revert target app:a, want app:a", previousImage)
	}
}

// TestInstanceControllerSurgesOnlyStatelessWithOldInstances: review round 2
// H2, H3 and M1 (D14). The surge allowance adds instances only to stateless
// services that still have old-image instances.
func TestInstanceControllerSurgesOnlyStatelessWithOldInstances(t *testing.T) {
	testCases := []struct {
		name         string            // name describes the case.
		kindFacts    map[string]string // kindFacts make the service stateful or per-node.
		currentImage string            // currentImage is the image of the running instances.
		wantCreated  bool              // wantCreated says whether a surge instance is created.
	}{
		{name: "stateless with old instances surges", currentImage: "app:a", wantCreated: true},
		{name: "stateless without old instances", currentImage: "app:b", wantCreated: false},
		{name: "per-node never surges", currentImage: "app:a", wantCreated: false,
			kindFacts: map[string]string{types.KeyEffectiveServicePlacementMode("web"): types.PlacementModePerNode}},
		{name: "stateful never surges", currentImage: "app:a", wantCreated: false,
			kindFacts: map[string]string{types.KeyEffectiveServiceStateful("web"): "true", types.KeyObservedInstanceOrdinal("web-0"): "0"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cluster := newRolloutLoopCluster(t, "web", 0, false, testCase.kindFacts)
			cluster.facts[types.KeyDesiredServiceInstances("web")] = "1"
			cluster.facts[types.KeyEffectiveServiceInstances("web")] = "1"
			cluster.facts[types.KeyDerivedServiceRolloutSurgeAllowance("web")] = "1"
			cluster.addRunningInstance("web-0", testCase.currentImage, time.Hour)
			cluster.reconcileAndApply(cluster.instanceController)
			if created := len(cluster.createdInstanceIDs) > 0 || hasFact(cluster.facts, types.KeyObservedInstance("web-1")); created != testCase.wantCreated {
				t.Errorf("surge instance created = %v (%v), want %v", created, cluster.createdInstanceIDs, testCase.wantCreated)
			}
		})
	}
}

// hasFact reports whether facts hold factKey.
func hasFact(facts map[string]string, factKey string) bool {
	_, isStored := facts[factKey]
	return isStored
}
