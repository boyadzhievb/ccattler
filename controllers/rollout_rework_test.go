// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable: spec
// service-rollout, "Stopped and not-yet-healthy instances count against the
// budget" (D11). Two of four old instances are stopped already, one new
// instance is healthy and one pending: 3 of 4 expected are healthy, so with
// max_unavailable 1 no further old instance may be stopped.
func TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"):            "4",
			types.KeyDesiredServiceUpdateMaxUnavailable("web"): "1",
			types.KeyDesiredServiceUpdateMaxExtra("web"):       "0",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceStopped},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceStopped},
		rolloutTestInstance{instanceID: "old-3", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-4", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-2", image: "app:b", state: types.InstancePending},
	)
	proposedChanges, _ := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 0 {
		t.Errorf("stopped %v with 1 of 4 expected instances not healthy and max_unavailable 1, want none", stoppedIDs)
	}
}

// TestRolloutDefaultsSurgeSingleInstanceService: spec service-rollout,
// "Defaults surge a one-instance service" (D12).
func TestRolloutDefaultsSurgeSingleInstanceService(t *testing.T) {
	startFacts := buildRolloutTestFacts("app:b",
		map[string]string{types.KeyDesiredServiceInstances("web"): "1"},
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
	)
	proposedChanges, changesByKey := reconcileRolloutTestFacts(t, startFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 0 {
		t.Errorf("stopped %v before any new instance exists, want none (default max_unavailable 0)", stoppedIDs)
	}
	if surgeAllowance, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutSurgeAllowance("web")); surgeAllowance != "1" {
		t.Errorf("surge_allowance = %q, want 1 (default max_extra 25%% rounded up)", surgeAllowance)
	}

	notHealthyFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, startedAgo: 3 * time.Second},
	)
	notHealthyChanges, _ := reconcileRolloutTestFacts(t, notHealthyFacts)
	if stoppedIDs := stoppedInstanceIDs(notHealthyChanges); len(stoppedIDs) != 0 {
		t.Errorf("stopped %v while the surge instance is not healthy yet, want none", stoppedIDs)
	}

	healthyFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning},
	)
	healthyChanges, _ := reconcileRolloutTestFacts(t, healthyFacts)
	if stoppedIDs := stoppedInstanceIDs(healthyChanges); len(stoppedIDs) != 1 || stoppedIDs[0] != "old-1" {
		t.Errorf("stopped %v once the surge instance is healthy, want [old-1]", stoppedIDs)
	}
}

// TestRolloutRevertToPreviousImageNeverRollsBack: spec service-rollout,
// "Restarts from before the rollout do not count" (D13). The desired image goes
// back to the stored previous image a during a rollout to b.
func TestRolloutRevertToPreviousImageNeverRollsBack(t *testing.T) {
	revertFacts := buildRolloutTestFacts("app:a",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "3",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning, restarts: "2", createdAgo: time.Hour},
		rolloutTestInstance{instanceID: "web-2", image: "app:a", state: types.InstanceRunning, restarts: "1", createdAgo: time.Hour},
		rolloutTestInstance{instanceID: "web-3", image: "app:b", state: types.InstanceRunning, createdAgo: time.Minute},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, revertFacts)
	assertNoRollback(t, changesByKey)
	if failureCount, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutFailures("web")); failureCount != "0" {
		t.Errorf("rollout/failures = %q, want 0: restarts from before the rollout do not count", failureCount)
	}
	if !isDeleted(changesByKey, types.KeyDerivedServiceRolloutImage("web")) {
		t.Error("previous_image kept on a revert to it, want it deleted (nothing to roll back to)")
	}
	if startedAt, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutStartedAt("web")); startedAt != rolloutTestNow.Format(time.RFC3339) {
		t.Errorf("rollout/started_at = %q, want %s", startedAt, rolloutTestNow.Format(time.RFC3339))
	}

	crashingFacts := buildRolloutTestFacts("app:a",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "", "app:a", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"):        "3",
			types.KeyDerivedServiceRolloutStartedAt("web"): rolloutTestNow.Add(-time.Minute).Format(time.RFC3339),
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning, createdAgo: time.Hour},
		rolloutTestInstance{instanceID: "web-4", image: "app:a", state: types.InstanceCrashLoopBackoff, restarts: "5", createdAgo: 30 * time.Second},
		rolloutTestInstance{instanceID: "web-3", image: "app:b", state: types.InstanceRunning, createdAgo: 2 * time.Minute},
	)
	_, crashingChangesByKey := reconcileRolloutTestFacts(t, crashingFacts)
	assertNoRollback(t, crashingChangesByKey)
	if failureCount, _ := putValue(crashingChangesByKey, types.KeyDerivedServiceRolloutFailures("web")); failureCount != "5" {
		t.Errorf("rollout/failures = %q, want 5 (the instance created during the revert)", failureCount)
	}
}

// TestRolloutLeavesNodeFailedOldInstanceToFailureController: review B2. An old
// instance on a lost node is not stopped by the rollout (the failure controller
// replaces it), so it is no progress and the deadline still rolls back.
func TestRolloutLeavesNodeFailedOldInstanceToFailureController(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", 11*time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "2",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceStopped, nodeFailure: true},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstancePending},
	)
	proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	for _, stoppedID := range stoppedInstanceIDs(proposedChanges) {
		if stoppedID == "old-1" {
			t.Error("old-1 on a lost node proposed as stopped again, want it left to the failure controller")
		}
	}
	if revertedImage, _ := putValue(changesByKey, types.KeyDesiredServiceImage("web")); revertedImage != "app:a" {
		t.Errorf("desired image = %q, want app:a (deadline passed without progress)", revertedImage)
	}
}

// TestRolloutRollbackReasonNamesLastExit: testbed 4.1 finding (a). The failing
// instance has just restarted and runs, so its status reason is "running"; the
// rollback reason names its last exit instead.
func TestRolloutRollbackReasonNamesLastExit(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, restarts: "3",
			startedAgo: time.Second, statusReason: "running", lastExit: "exited with code 1"},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	rollbackReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web"))
	assertContainsAll(t, "rollback_reason", rollbackReason, "3 failures", "exited with code 1")
}

// TestRolloutLegacyRollbackWithoutTargetFinishesComplete: review L1. A
// `rollback` stored by an older server (no target_image, no rollback_reason)
// finishes as complete, not as rolled-back with no reason.
func TestRolloutLegacyRollbackWithoutTargetFinishesComplete(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:a",
		map[string]string{
			types.KeyDerivedServiceRolloutState("web"): string(types.RolloutRollback),
			types.KeyDerivedServiceRolloutImage("web"): "app:a",
			types.KeyDesiredServiceInstances("web"):    "1",
		},
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutComplete) {
		t.Errorf("rollout/state = %q, want complete", rolloutState)
	}
}
