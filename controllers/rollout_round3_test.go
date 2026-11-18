// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// rolloutRound3Step is the pass interval of the round-3 loop tests: shorter
// than the default min_healthy_time, so a started instance is running but
// not yet healthy for a pass before it counts.
const rolloutRound3Step = 5 * time.Second

// TestRolloutRecreatedOrdinalCountsOnlyOnceStartedOnNewImage: testbed T1
// (2e.1). A 1-instance stateful rollout stops db-0 and the instance controller
// recreates it under the same ID before the agent sees the stop. The rollout
// must not end until the agent runs db-0 on the new image.
func TestRolloutRecreatedOrdinalCountsOnlyOnceStartedOnNewImage(t *testing.T) {
	statefulFacts := map[string]string{types.KeyEffectiveServiceStateful("db"): "true"}
	t.Run("agent replaces the container", func(t *testing.T) {
		cluster := newRolloutLoopCluster(t, "db", 1, true, statefulFacts)
		cluster.stepDuration = rolloutRound3Step
		cluster.runPasses(30, types.RolloutComplete)
		if cluster.rolloutState() != types.RolloutComplete {
			t.Errorf("rollout/state = %q after 30 passes, want complete", cluster.rolloutState())
		}
		cluster.assertAllActiveOnImage(rolloutLoopNewImage, 1)
	})
	t.Run("agent keeps the old container", func(t *testing.T) {
		cluster := newRolloutLoopCluster(t, "db", 1, true, statefulFacts)
		cluster.stepDuration = rolloutRound3Step
		cluster.agentEnsuresImage = false
		cluster.runPasses(20, types.RolloutComplete)
		if cluster.rolloutState() == types.RolloutComplete {
			t.Errorf("rollout complete while db-0's container runs %q, want it to keep waiting", cluster.containers["db-0"].image)
		}
		for _, prematureFinish := range cluster.prematureFinishes {
			t.Error(prematureFinish)
		}
		reason := cluster.facts[types.KeyDerivedServiceRolloutReason("db")]
		for _, wantText := range []string{"db-0", rolloutLoopOldImage, rolloutLoopNewImage} {
			if !strings.Contains(reason, wantText) {
				t.Errorf("rollout/reason %q does not contain %q", reason, wantText)
			}
		}
	})
}

// TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy: 2e.1 (b) as a unit
// test. A running instance whose observed image differs from its recorded one
// was not started by the agent on its image yet and is not healthy, with or
// without started_at; without a recorded image (older server) or with a
// matching observed image (older agent, no started_at) it counts at once.
func TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy(t *testing.T) {
	testCases := []struct {
		name          string // name describes the case.
		recordedImage string // recordedImage is derived/instance/{id}/image; "" for none.
		observedImage string // observedImage is observed/instance/{id}/image.
		hasStartedAt  bool   // hasStartedAt gives the instance a settled started_at.
		wantHealthy   bool   // wantHealthy is the expected rule outcome.
	}{
		{name: "recreated, old container kept", recordedImage: "app:b", observedImage: "app:a", wantHealthy: false},
		{name: "recreated, old container kept, stale started_at", recordedImage: "app:b", observedImage: "app:a", hasStartedAt: true, wantHealthy: false},
		{name: "older agent, image matches", recordedImage: "app:b", observedImage: "app:b", wantHealthy: true},
		{name: "older server, no recorded image", observedImage: "app:b", wantHealthy: true},
		{name: "started on its image", recordedImage: "app:b", observedImage: "app:b", hasStartedAt: true, wantHealthy: true},
	}
	healthRule := rolloutHealthRule{minHealthyTime: rolloutDefaultMinHealthyTime, now: rolloutTestNow}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			instanceInfo := rolloutInstanceInfo{id: "db-0", state: types.InstanceRunning,
				recordedImage: testCase.recordedImage, observedImage: testCase.observedImage}
			if testCase.hasStartedAt {
				instanceInfo.startedAt = rolloutTestNow.Add(-time.Hour)
			}
			if healthy, healthWait := healthRule.evaluate(instanceInfo); healthy != testCase.wantHealthy {
				t.Errorf("healthy = %v (%q), want %v", healthy, healthWait, testCase.wantHealthy)
			}
		})
	}
}

// TestInstanceControllerSurgesOnlyForRemainingOldInstances: review round 3 M1
// (2e.3). The surge never exceeds the old instances left, so a rollout
// creates exactly one new instance per old one and scales none down.
func TestInstanceControllerSurgesOnlyForRemainingOldInstances(t *testing.T) {
	testCases := []struct {
		name          string            // name describes the case.
		instanceCount int               // instanceCount is the expected instance count.
		updateFacts   map[string]string // updateFacts set the update block.
	}{
		{name: "5 instances on defaults", instanceCount: 5},
		{name: "3 instances with max_extra 2", instanceCount: 3,
			updateFacts: map[string]string{types.KeyDesiredServiceUpdateMaxExtra("web"): "2"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cluster := newRolloutLoopCluster(t, "web", testCase.instanceCount, false, testCase.updateFacts)
			cluster.stepDuration = rolloutRound3Step
			cluster.runPasses(60, types.RolloutComplete)
			cluster.runPasses(4, "")
			if cluster.rolloutState() != types.RolloutComplete {
				t.Errorf("rollout/state = %q, want complete", cluster.rolloutState())
			}
			if len(cluster.createdInstanceIDs) != testCase.instanceCount {
				t.Errorf("instance controller created %d instances %v, want exactly %d", len(cluster.createdInstanceIDs),
					cluster.createdInstanceIDs, testCase.instanceCount)
			}
			for _, scaledDownID := range cluster.scaledDownIDs {
				if slices.Contains(cluster.createdInstanceIDs, scaledDownID) {
					t.Errorf("new instance %s scaled down", scaledDownID)
				}
			}
			cluster.assertAllActiveOnImage(rolloutLoopNewImage, testCase.instanceCount)
			if maxAllowed := testCase.instanceCount + rolloutLoopMaxSurge(testCase.instanceCount, testCase.updateFacts); cluster.maxActiveCount > maxAllowed {
				t.Errorf("up to %d active instances, want at most %d", cluster.maxActiveCount, maxAllowed)
			}
		})
	}
}

// rolloutLoopMaxSurge returns the surge a stateless test service may use: its
// explicit max_extra, else the 25 % default rounded up.
func rolloutLoopMaxSurge(instanceCount int, updateFacts map[string]string) int {
	if explicitMaxExtra, isSet := updateFacts[types.KeyDesiredServiceUpdateMaxExtra("web")]; isSet {
		parsedMaxExtra, _ := strconv.Atoi(explicitMaxExtra)
		return parsedMaxExtra
	}
	return types.ResolveRolloutPacing(types.WorkloadStateless, instanceCount, nil, nil).MaxExtra
}

// TestRolloutReasonNamesWaitingPerNodeInstance: testbed T2 (2e.2, D7). A
// per-node rollout whose replacement is pending names it in the reason, with
// the scheduler's unplaced reason when there is one and its state otherwise.
func TestRolloutReasonNamesWaitingPerNodeInstance(t *testing.T) {
	testCases := []struct {
		name          string            // name describes the case.
		unplacedFacts map[string]string // unplacedFacts are the scheduler's facts of the pending instance.
		wantTexts     []string          // wantTexts must all be in the reason.
	}{
		{name: "not yet seen by the scheduler", wantTexts: []string{"web-new", "pending"}},
		{name: "unplaced", wantTexts: []string{"web-new", "unplaced", "insufficient_capacity"},
			unplacedFacts: map[string]string{types.KeyDerivedSchedulerUnplacedReason("web-new"): string(types.UnplacedInsufficientCapacity)}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			kindFacts := mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
				types.KeyEffectiveServicePlacementMode("web"): string(types.PlacementModePerNode),
				types.KeyEffectiveServiceInstances("web"):     "3",
			})
			inputFacts := buildRolloutTestFacts("app:b", mergeFactMaps(kindFacts, testCase.unplacedFacts),
				rolloutTestInstance{instanceID: "web-old-1", image: "app:a", state: types.InstanceStopped},
				rolloutTestInstance{instanceID: "web-old-2", image: "app:a", state: types.InstanceRunning},
				rolloutTestInstance{instanceID: "web-old-3", image: "app:a", state: types.InstanceRunning},
				rolloutTestInstance{instanceID: "web-new", state: types.InstancePending},
			)
			_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
			rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
			assertContainsAll(t, "rollout/reason", rolloutReason, testCase.wantTexts...)
		})
	}
}

// TestRolloutNeverCountsOldImageExitsAgainstTarget: testbed rerun after 2e
// (2f.2). A re-created ordinal whose container still ran (and exited on) its
// previous image carries that image's restarts and state until the agent
// starts the recorded image. They are not failures of the recorded image:
// no "previous image also fails" during a rollback, no failures while rolling.
func TestRolloutNeverCountsOldImageExitsAgainstTarget(t *testing.T) {
	testCases := []struct {
		name          string            // name describes the case.
		desiredImage  string            // desiredImage is the service's desired (and the ordinal's recorded) image.
		rolloutFacts  map[string]string // rolloutFacts are the stored rollout facts.
		previousImage string            // previousImage is the image the ordinal's container ran and exited on.
	}{
		{name: "rollback from b to a", desiredImage: "app:a", previousImage: "app:b",
			rolloutFacts: rollingFacts(types.RolloutRollback, "app:a", "app:a", time.Minute)},
		{name: "rolling from a to b", desiredImage: "app:b", previousImage: "app:a",
			rolloutFacts: rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			inputFacts := buildRolloutTestFacts(testCase.desiredImage,
				mergeFactMaps(testCase.rolloutFacts, map[string]string{
					types.KeyDerivedInstanceImage("web-0"):     testCase.desiredImage,
					types.KeyDerivedServiceRolloutState("web"): testCase.rolloutFacts[types.KeyDerivedServiceRolloutState("web")],
				}),
				rolloutTestInstance{instanceID: "web-0", image: testCase.previousImage, state: types.InstanceCrashLoopBackoff,
					restarts: "3", lastExit: "exited with code 1"},
			)
			_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
			if revertedImage, reverted := putValue(changesByKey, types.KeyDesiredServiceImage("web")); reverted {
				t.Errorf("desired image reverted to %q, want no rollback for the old image's exits", revertedImage)
			}
			if failures, written := putValue(changesByKey, types.KeyDerivedServiceRolloutFailures("web")); written && failures != "0" {
				t.Errorf("rollout/failures = %q, want 0", failures)
			}
			rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
			if strings.Contains(rolloutReason, "also fails") {
				t.Errorf("rollout/reason %q counts the old image's exit against %s", rolloutReason, testCase.desiredImage)
			}
		})
	}
}
