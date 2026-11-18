// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// assertRolloutStateNotChangedTo fails when the changes move the "web"
// rollout to the given state.
func assertRolloutStateNotChangedTo(t *testing.T, changesByKey map[string]Change, unwantedState types.RolloutState) {
	t.Helper()
	if rolloutState, changed := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); changed && rolloutState == string(unwantedState) {
		t.Errorf("rollout/state = %q, want the rollout to stay rolling", rolloutState)
	}
}

// TestRolloutDoesNotStopOldBeforeMinHealthyTime: spec service-rollout, "Image
// that exits after a moment does not replace the old instance" (M241 D10).
func TestRolloutDoesNotStopOldBeforeMinHealthyTime(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, startedAgo: 2 * time.Second},
	)
	proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 0 {
		t.Errorf("stopped %v while the new instance has run only 2s, want none", stoppedIDs)
	}
	assertRolloutStateNotChangedTo(t, changesByKey, types.RolloutComplete)
	rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
	assertContainsAll(t, "rollout/reason", rolloutReason, "waiting for new-1 to stay healthy", "(8s)")
}

// TestRolloutDoesNotFinishBeforeMinHealthyTime: spec service-rollout, "Rollout
// does not finish before the window": the same facts 6 seconds later finish it.
func TestRolloutDoesNotFinishBeforeMinHealthyTime(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"):            "1",
			types.KeyDesiredServiceUpdateMinHealthyTime("web"): "10s",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceStopped},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, startedAgo: 5 * time.Second},
	)
	_, firstChangesByKey := reconcileRolloutTestFacts(t, inputFacts)
	assertRolloutStateNotChangedTo(t, firstChangesByKey, types.RolloutComplete)

	_, laterChangesByKey := reconcileRolloutTestFactsAt(t, inputFacts, rolloutTestNow.Add(6*time.Second))
	if rolloutState, _ := putValue(laterChangesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutComplete) {
		t.Errorf("rollout/state 6s later = %q, want complete", rolloutState)
	}
}

// TestRolloutRequiresReadinessWhenProbed: spec service-rollout, "Readiness
// probe must pass"; the ready case shows the probe is the only thing missing.
func TestRolloutRequiresReadinessWhenProbed(t *testing.T) {
	testCases := []struct {
		name          string // name describes the case.
		readiness     string // readiness is the new instance's readiness probe state.
		wantStopCount int    // wantStopCount is how many old instances the pass stops.
	}{
		{name: "not ready", readiness: string(types.ReadinessProbeNotReady), wantStopCount: 0},
		{name: "ready", readiness: string(types.ReadinessProbeReady), wantStopCount: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			inputFacts := buildRolloutTestFacts("app:b",
				mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
					types.KeyDesiredServiceInstances("web"):                         "2",
					types.KeyDesiredServiceProbeMethod("web", types.ProbeReadiness): "http",
					types.KeyDesiredServiceProbePath("web", types.ProbeReadiness):   "/ready",
				}),
				rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, readiness: testCase.readiness},
				rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
				rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceRunning},
			)
			proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
			if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != testCase.wantStopCount {
				t.Errorf("stopped %v with readiness %s, want %d", stoppedIDs, testCase.readiness, testCase.wantStopCount)
			}
			if testCase.wantStopCount == 0 {
				rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
				assertContainsAll(t, "rollout/reason", rolloutReason, "new-1", "not-ready")
			}
		})
	}
}

// TestRolloutCountsInstanceWithoutStartedAtAsHealthy: spec service-rollout,
// "Instance from an older agent".
func TestRolloutCountsInstanceWithoutStartedAtAsHealthy(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "2",
		}),
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, noStartedAt: true},
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceRunning},
	)
	proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 1 || stoppedIDs[0] != "old-1" {
		t.Errorf("stopped %v, want [old-1]: a running instance without started_at is healthy", stoppedIDs)
	}
	if runningNew, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRunningNew("web")); runningNew != "1" {
		t.Errorf("running_new = %q, want 1", runningNew)
	}
}
