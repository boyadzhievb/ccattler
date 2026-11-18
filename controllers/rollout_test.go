// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestRolloutControllerImplementsInterface(t *testing.T) {
	var _ Controller = NewRolloutController()
}

func TestRolloutControllerName(t *testing.T) {
	rolloutController := NewRolloutController()
	if rolloutController.Name() != "rollout" {
		t.Errorf("name: got %s, want rollout", rolloutController.Name())
	}
}

func TestRolloutControllerRespectsDisruptionBudget(t *testing.T) {
	rolloutController := NewRolloutController()

	// Service "web" is rolling from nginx:1.27 → nginx:1.28.
	// 5 desired, update policy max_unavailable=3.
	// Disruption budget: min_available 4 → effective max_unavailable = 1.
	// 3 old-image running, 2 new-image running → should stop at most 1 old.
	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("5")},
		{Key: types.KeyDesiredServiceUpdateMaxUnavailable("web"), Value: []byte("3")},
		{Key: types.KeyDesiredServiceUpdateMaxExtra("web"), Value: []byte("1")},
		{Key: types.KeyDesiredServiceDisruptionMinAvailable("web"), Value: []byte("4")},
		// 3 old-image instances.
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-2"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-3"), Value: []byte("nginx:1.27")},
		// 2 new-image instances.
		{Key: types.KeyObservedInstanceState("inst-4"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-4"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-4"), Value: []byte("nginx:1.28")},
		{Key: types.KeyObservedInstanceState("inst-5"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-5"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-5"), Value: []byte("nginx:1.28")},
		// Rollout already tracked.
		{Key: types.KeyDerivedServiceRolloutImage("web"), Value: []byte("nginx:1.27")},
		{Key: types.KeyDerivedServiceRolloutState("web"), Value: []byte("rolling")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	stoppedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			stoppedCount++
		}
	}
	// Budget allows max 1 unavailable (5 desired - 4 min_available = 1).
	if stoppedCount != 1 {
		t.Errorf("expected 1 old instance stopped (disruption budget caps to 1), got %d", stoppedCount)
	}
}

func TestRolloutEmitsSurgeAllowanceOnStart(t *testing.T) {
	rolloutController := NewRolloutController()

	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDesiredServiceUpdateMaxExtra("web"), Value: []byte("2")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-2"), Value: []byte("nginx:1.27")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	foundSurge := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedServiceRolloutSurgeAllowance("web") {
			foundSurge = true
			if string(change.Value) != "2" {
				t.Errorf("surge allowance value: got %s, want 2", string(change.Value))
			}
		}
	}
	if !foundSurge {
		t.Error("expected surge allowance fact to be emitted when max_extra > 0")
	}
}

func TestRolloutNoSurgeWhenMaxExtraZero(t *testing.T) {
	rolloutController := NewRolloutController()

	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedServiceRolloutSurgeAllowance("web") {
			t.Error("surge allowance should not be emitted when max_extra is 0")
		}
	}
}

func TestRolloutDeletesSurgeOnCompletion(t *testing.T) {
	rolloutController := NewRolloutController()

	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDesiredServiceUpdateMaxExtra("web"), Value: []byte("2")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDerivedServiceRolloutState("web"), Value: []byte("rolling")},
		{Key: types.KeyDerivedServiceRolloutImage("web"), Value: []byte("nginx:1.27")},
		{Key: types.KeyDerivedServiceRolloutSurgeAllowance("web"), Value: []byte("2")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	foundComplete := false
	foundSurgeDelete := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedServiceRolloutState("web") && string(change.Value) == "complete" {
			foundComplete = true
		}
		if change.Key == types.KeyDerivedServiceRolloutSurgeAllowance("web") && change.Type == store.OpDelete {
			foundSurgeDelete = true
		}
	}
	if !foundComplete {
		t.Error("expected rollout state to be set to 'complete'")
	}
	if !foundSurgeDelete {
		t.Error("expected surge allowance to be deleted on rollout completion")
	}
}

func TestRolloutEmitsRevisionHistory(t *testing.T) {
	rolloutController := NewRolloutController()

	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	foundRevision := false
	foundHistory := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedServiceRolloutRevision("web") {
			foundRevision = true
			if string(change.Value) != "1" {
				t.Errorf("first revision should be 1, got %s", string(change.Value))
			}
		}
		if change.Key == types.KeyDerivedServiceRolloutHistory("web", 1) {
			foundHistory = true
			if string(change.Value) != "nginx:1.27" {
				t.Errorf("history should record old image nginx:1.27, got %s", string(change.Value))
			}
		}
	}
	if !foundRevision {
		t.Error("expected revision counter to be emitted")
	}
	if !foundHistory {
		t.Error("expected history entry to be emitted")
	}
}

func TestRolloutPausedSkipsStops(t *testing.T) {
	rolloutController := NewRolloutController()

	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDesiredServiceRolloutPaused("web"), Value: []byte("true")},
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-2"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDerivedServiceRolloutState("web"), Value: []byte("rolling")},
		{Key: types.KeyDerivedServiceRolloutImage("web"), Value: []byte("nginx:1.27")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			t.Error("paused rollout should not stop any instances")
		}
	}
}

// rolloutTestNow is the fixed clock of the M241 rollout tests.
var rolloutTestNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// rolloutTestInstance describes one instance of the "web" service for the
// M241 rollout tests.
type rolloutTestInstance struct {
	instanceID   string              // instanceID is the instance's ID.
	image        string              // image is the observed image; "" for none yet.
	state        types.InstanceState // state is the observed state.
	restarts     string              // restarts is the observed restart count; "" for none.
	nodeFailure  bool                // nodeFailure marks the instance as on a lost node.
	statusReason string              // statusReason is the observed status reason.
	startedAgo   time.Duration       // startedAgo is how long before the test clock it started; 0 means rolloutTestSettledAge.
	noStartedAt  bool                // noStartedAt leaves out started_at, as an agent older than M241 D10 does.
	readiness    string              // readiness is the observed readiness probe state; "" for none.
	createdAgo   time.Duration       // createdAgo is how long before the test clock the agent first saw it; 0 for no created-at.
	lastExit     string              // lastExit is the observed last_exit; "" for none.
}

// rolloutTestSettledAge is the default age of a running test instance's
// started_at: well past the default min_healthy_time, so it counts at once.
const rolloutTestSettledAge = time.Minute

// buildRolloutTestFacts returns sorted facts for service "web" with the given
// desired image, extra facts (key → value) and instances. A running instance
// gets a started_at (rolloutTestSettledAge ago unless set), as the agent writes.
func buildRolloutTestFacts(desiredImage string, extraFacts map[string]string, instances ...rolloutTestInstance) []store.Fact {
	inputFacts := []store.Fact{{Key: types.KeyDesiredServiceImage("web"), Value: []byte(desiredImage)}}
	for factKey, factValue := range extraFacts {
		inputFacts = append(inputFacts, store.Fact{Key: factKey, Value: []byte(factValue)})
	}
	for _, instance := range instances {
		inputFacts = append(inputFacts,
			store.Fact{Key: types.KeyObservedInstanceService(instance.instanceID), Value: []byte("web")},
			store.Fact{Key: types.KeyObservedInstanceState(instance.instanceID), Value: []byte(instance.state)},
		)
		if instance.image != "" {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceImage(instance.instanceID), Value: []byte(instance.image)})
		}
		if instance.restarts != "" {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceRestarts(instance.instanceID), Value: []byte(instance.restarts)})
		}
		if instance.nodeFailure {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyDerivedInstanceNodeFailure(instance.instanceID), Value: []byte("true")})
		}
		if instance.statusReason != "" {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceStatusReason(instance.instanceID), Value: []byte(instance.statusReason)})
		}
		if instance.readiness != "" {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceProbeState(instance.instanceID, types.ProbeReadiness), Value: []byte(instance.readiness)})
		}
		if instance.createdAgo != 0 {
			createdAt := rolloutTestNow.Add(-instance.createdAgo).Format(time.RFC3339)
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceCreatedAt(instance.instanceID), Value: []byte(createdAt)})
		}
		if instance.lastExit != "" {
			inputFacts = append(inputFacts, store.Fact{Key: types.KeyObservedInstanceLastExit(instance.instanceID), Value: []byte(instance.lastExit)})
		}
		inputFacts = append(inputFacts, buildStartedAtTestFacts(instance)...)
	}
	store.SortFacts(inputFacts)
	return inputFacts
}

// buildStartedAtTestFacts returns the started_at fact of a running test
// instance, none for other states or when noStartedAt is set.
func buildStartedAtTestFacts(instance rolloutTestInstance) []store.Fact {
	if instance.state != types.InstanceRunning || instance.noStartedAt {
		return nil
	}
	startedAgo := instance.startedAgo
	if startedAgo == 0 {
		startedAgo = rolloutTestSettledAge
	}
	startedAt := rolloutTestNow.Add(-startedAgo).Format(time.RFC3339Nano)
	return []store.Fact{{Key: types.KeyObservedInstanceStartedAt(instance.instanceID), Value: []byte(startedAt)}}
}

// rollingFacts returns the rollout facts of a "web" rollout from previousImage
// to targetImage whose last progress was progressAge ago.
func rollingFacts(rolloutState types.RolloutState, previousImage string, targetImage string, progressAge time.Duration) map[string]string {
	return map[string]string{
		types.KeyDerivedServiceRolloutState("web"):       string(rolloutState),
		types.KeyDerivedServiceRolloutImage("web"):       previousImage,
		types.KeyDerivedServiceRolloutTargetImage("web"): targetImage,
		types.KeyDerivedServiceRolloutRevision("web"):    "1",
		types.KeyDerivedServiceRolloutProgressAt("web"):  rolloutTestNow.Add(-progressAge).Format(time.RFC3339),
	}
}

// mergeFactMaps returns one map holding the entries of all given maps; later maps win.
func mergeFactMaps(factMaps ...map[string]string) map[string]string {
	merged := make(map[string]string)
	for _, factMap := range factMaps {
		maps.Copy(merged, factMap)
	}
	return merged
}

// reconcileRolloutTestFacts runs a rollout controller with the test clock and
// returns its changes and the last change per key.
func reconcileRolloutTestFacts(t *testing.T, inputFacts []store.Fact) ([]Change, map[string]Change) {
	t.Helper()
	return reconcileRolloutTestFactsAt(t, inputFacts, rolloutTestNow)
}

// reconcileRolloutTestFactsAt runs a rollout controller whose clock reads
// passNow and returns its changes and the last change per key.
func reconcileRolloutTestFactsAt(t *testing.T, inputFacts []store.Fact, passNow time.Time) ([]Change, map[string]Change) {
	t.Helper()
	rolloutController := NewRolloutController()
	rolloutController.Now = func() time.Time { return passNow }
	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	changesByKey := make(map[string]Change, len(proposedChanges))
	for _, change := range proposedChanges {
		if _, duplicate := changesByKey[change.Key]; duplicate {
			t.Errorf("key %s proposed twice in one pass", change.Key)
		}
		changesByKey[change.Key] = change
	}
	return proposedChanges, changesByKey
}

// stoppedInstanceIDs returns the IDs of the instances the changes stop, in change order.
func stoppedInstanceIDs(proposedChanges []Change) []string {
	var stoppedIDs []string
	for _, change := range proposedChanges {
		if change.Type == store.OpPut && string(change.Value) == string(types.InstanceStopped) &&
			strings.HasPrefix(change.Key, types.ScanObservedInstances) {
			instanceID := strings.SplitN(strings.TrimPrefix(change.Key, types.ScanObservedInstances), "/", 2)[0]
			stoppedIDs = append(stoppedIDs, instanceID)
		}
	}
	return stoppedIDs
}

// putValue returns the value a change puts at factKey and whether one does.
func putValue(changesByKey map[string]Change, factKey string) (string, bool) {
	change, hasChange := changesByKey[factKey]
	if !hasChange || change.Type != store.OpPut {
		return "", false
	}
	return string(change.Value), true
}

// isDeleted reports whether the changes delete factKey.
func isDeleted(changesByKey map[string]Change, factKey string) bool {
	change, hasChange := changesByKey[factKey]
	return hasChange && change.Type == store.OpDelete
}

// assertNoRollback fails when the changes revert the desired image of "web".
func assertNoRollback(t *testing.T, changesByKey map[string]Change) {
	t.Helper()
	if revertedImage, reverted := putValue(changesByKey, types.KeyDesiredServiceImage("web")); reverted {
		t.Errorf("desired image reverted to %q, want no rollback", revertedImage)
	}
}

// assertContainsAll fails when text lacks one of the fragments.
func assertContainsAll(t *testing.T, what string, text string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("%s %q does not contain %q", what, text, fragment)
		}
	}
}

// TestRolloutStopsOldInstancesInIDOrder: spec service-rollout, "Paced and deterministic".
func TestRolloutStopsOldInstancesInIDOrder(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		map[string]string{types.KeyDesiredServiceUpdateMaxUnavailable("web"): "1"},
		rolloutTestInstance{instanceID: "web-d", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-b", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-c", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-a", image: "app:a", state: types.InstanceRunning},
	)
	for attempt := range 20 {
		proposedChanges, _ := reconcileRolloutTestFacts(t, inputFacts)
		stoppedIDs := stoppedInstanceIDs(proposedChanges)
		if len(stoppedIDs) != 1 || stoppedIDs[0] != "web-a" {
			t.Fatalf("attempt %d stopped %v, want exactly [web-a]", attempt, stoppedIDs)
		}
	}
}

// TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable: spec
// service-rollout, "Unavailable instances use up the budget".
func TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceUpdateMaxUnavailable("web"): "2",
			types.KeyDesiredServiceInstances("web"):            "5",
		}),
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceCrashLoopBackoff},
		rolloutTestInstance{instanceID: "new-2", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-3", image: "app:a", state: types.InstanceRunning},
	)
	proposedChanges, _ := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 1 {
		t.Errorf("stopped %v, want exactly one healthy old instance (max_unavailable 2, one new instance unavailable)", stoppedIDs)
	}
}

// TestRolloutStopsUnhealthyOldInstancesFirst: spec service-rollout,
// "Crash-looping old instance goes first".
func TestRolloutStopsUnhealthyOldInstancesFirst(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceUpdateMaxUnavailable("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "old-2", image: "app:a", state: types.InstanceCrashLoopBackoff, restarts: "4"},
		rolloutTestInstance{instanceID: "old-3", image: "app:a", state: types.InstanceRunning},
	)
	for attempt := range 20 {
		proposedChanges, _ := reconcileRolloutTestFacts(t, inputFacts)
		if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 1 || stoppedIDs[0] != "old-2" {
			t.Fatalf("attempt %d stopped %v, want exactly [old-2]", attempt, stoppedIDs)
		}
	}
}

// TestRolloutCountsNewImageRestartsAndFailures: spec service-rollout,
// "Restarts and failed instances count".
func TestRolloutCountsNewImageRestartsAndFailures(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "10",
		}),
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, restarts: "2"},
		rolloutTestInstance{instanceID: "new-2", image: "app:b", state: types.InstanceFailed},
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning, restarts: "7"},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if failureCount, written := putValue(changesByKey, types.KeyDerivedServiceRolloutFailures("web")); !written || failureCount != "3" {
		t.Errorf("rollout/failures = %q (written %v), want 3", failureCount, written)
	}
	assertNoRollback(t, changesByKey)
}

// TestRolloutIgnoresNodeFailureForRollback: spec service-rollout, "Node loss
// is not a failure of the image".
func TestRolloutIgnoresNodeFailureForRollback(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "4",
		}),
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceRunning, nodeFailure: true},
		rolloutTestInstance{instanceID: "new-2", image: "app:b", state: types.InstanceRunning, nodeFailure: true},
		rolloutTestInstance{instanceID: "new-3", image: "app:b", state: types.InstanceRunning, nodeFailure: true},
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if failureCount, written := putValue(changesByKey, types.KeyDerivedServiceRolloutFailures("web")); !written || failureCount != "0" {
		t.Errorf("rollout/failures = %q (written %v), want 0", failureCount, written)
	}
	assertNoRollback(t, changesByKey)
}

// TestRolloutRollsBackSmallServiceOnRestarts: spec service-rollout, "One-instance
// service rolls back" (and instance-restart's ended interim scenario).
func TestRolloutRollsBackSmallServiceOnRestarts(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceStopped},
		rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceCrashLoopBackoff, restarts: "3",
			statusReason: "exited with code 1; restarting in 40s"},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if revertedImage, _ := putValue(changesByKey, types.KeyDesiredServiceImage("web")); revertedImage != "app:a" {
		t.Errorf("desired image = %q, want app:a", revertedImage)
	}
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutRollback) {
		t.Errorf("rollout/state = %q, want rollback", rolloutState)
	}
	rollbackReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web"))
	assertContainsAll(t, "rollback_reason", rollbackReason, "3 failures", "≥ 3", "app:b", "exited with code 1")
	if rolledBackImage, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRolledBackImage("web")); rolledBackImage != "app:b" {
		t.Errorf("rolled_back_image = %q, want app:b", rolledBackImage)
	}
	if rolledBackAt, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRolledBackAt("web")); rolledBackAt != rolloutTestNow.Format(time.RFC3339) {
		t.Errorf("rolled_back_at = %q, want %s", rolledBackAt, rolloutTestNow.Format(time.RFC3339))
	}
}

// TestRolloutRollbackThresholdScalesWithDesiredCount: spec service-rollout,
// "Threshold scales with the service".
func TestRolloutRollbackThresholdScalesWithDesiredCount(t *testing.T) {
	testCases := []struct {
		name          string // name describes the case.
		restarts      string // restarts is the new instance's restart count.
		wantRollback  bool   // wantRollback says whether the rollout rolls back.
		desiredCount  string // desiredCount is the service's desired instance count.
		wantThreshold string // wantThreshold is the threshold the rollback reason names.
	}{
		{name: "10 instances, 4 failures", restarts: "4", desiredCount: "10", wantRollback: false},
		{name: "10 instances, 5 failures", restarts: "5", desiredCount: "10", wantRollback: true, wantThreshold: "≥ 5"},
		{name: "2 instances, 2 failures (lower bound 3)", restarts: "2", desiredCount: "2", wantRollback: false},
		{name: "1000 instances, 200 failures (upper bound 200)", restarts: "200", desiredCount: "1000", wantRollback: true, wantThreshold: "≥ 200"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			inputFacts := buildRolloutTestFacts("app:b",
				mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
					types.KeyDesiredServiceInstances("web"): testCase.desiredCount,
				}),
				rolloutTestInstance{instanceID: "new-1", image: "app:b", state: types.InstanceCrashLoopBackoff, restarts: testCase.restarts},
				rolloutTestInstance{instanceID: "old-1", image: "app:a", state: types.InstanceRunning},
			)
			_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
			revertedImage, reverted := putValue(changesByKey, types.KeyDesiredServiceImage("web"))
			if reverted != testCase.wantRollback {
				t.Fatalf("rollback = %v (image %q), want %v", reverted, revertedImage, testCase.wantRollback)
			}
			if testCase.wantRollback {
				rollbackReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web"))
				assertContainsAll(t, "rollback_reason", rollbackReason, testCase.wantThreshold)
			}
		})
	}
}

// TestRolloutTracksSecondRollout: spec service-rollout, "Second rollout is tracked".
func TestRolloutTracksSecondRollout(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:c",
		map[string]string{
			types.KeyDerivedServiceRolloutState("web"):          string(types.RolloutComplete),
			types.KeyDerivedServiceRolloutImage("web"):          "app:a", // stale, left by an older server
			types.KeyDerivedServiceRolloutRevision("web"):       "1",
			types.KeyDerivedServiceRolloutRollbackReason("web"): "old reason",
		},
		rolloutTestInstance{instanceID: "web-1", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:b", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	wantPuts := map[string]string{
		types.KeyDerivedServiceRolloutState("web"):       string(types.RolloutRolling),
		types.KeyDerivedServiceRolloutImage("web"):       "app:b",
		types.KeyDerivedServiceRolloutTargetImage("web"): "app:c",
		types.KeyDerivedServiceRolloutRevision("web"):    "2",
		types.KeyDerivedServiceRolloutHistory("web", 2):  "app:b",
	}
	for factKey, wantValue := range wantPuts {
		if gotValue, _ := putValue(changesByKey, factKey); gotValue != wantValue {
			t.Errorf("%s = %q, want %q", factKey, gotValue, wantValue)
		}
	}
	if !isDeleted(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web")) {
		t.Error("rollback_reason of the earlier rollback not deleted when the next rollout starts")
	}
}

// TestRolloutRollbackMovesToRolledBack: spec service-rollout, "Rollback finishes".
func TestRolloutRollbackMovesToRolledBack(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:a",
		mergeFactMaps(rollingFacts(types.RolloutRollback, "app:a", "app:a", time.Minute), map[string]string{
			types.KeyDerivedServiceRolloutRollbackReason("web"): "3 failures ≥ 3 on app:b: exited with code 1",
			types.KeyDerivedServiceRolloutFailures("web"):       "3",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:b", state: types.InstanceStopped},
		rolloutTestInstance{instanceID: "web-2", image: "app:a", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutRolledBack) {
		t.Errorf("rollout/state = %q, want rolled-back", rolloutState)
	}
	for _, deletedKey := range []string{types.KeyDerivedServiceRolloutImage("web"), types.KeyDerivedServiceRolloutFailures("web")} {
		if !isDeleted(changesByKey, deletedKey) {
			t.Errorf("%s not deleted when the rollback finished", deletedKey)
		}
	}
	if isDeleted(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web")) {
		t.Error("rollback_reason deleted when the rollback finished, want it kept until the next rollout")
	}
}

// TestRolloutNewImageMidRolloutKeepsKnownGood: spec service-rollout, "Image
// changed mid-rollout".
func TestRolloutNewImageMidRolloutKeepsKnownGood(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:c",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "2",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:b", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if rolloutState, changed := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); changed && rolloutState != string(types.RolloutRolling) {
		t.Errorf("rollout/state = %q, want it to stay rolling", rolloutState)
	}
	if targetImage, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutTargetImage("web")); targetImage != "app:c" {
		t.Errorf("target_image = %q, want app:c", targetImage)
	}
	if revision, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRevision("web")); revision != "2" {
		t.Errorf("revision = %q, want 2", revision)
	}
	if previousImage, overwritten := putValue(changesByKey, types.KeyDerivedServiceRolloutImage("web")); overwritten && previousImage != "app:a" {
		t.Errorf("previous_image overwritten with %q, want it to stay app:a", previousImage)
	}

	laterFacts := buildRolloutTestFacts("app:c",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:c", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "2",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", image: "app:c", state: types.InstanceCrashLoopBackoff, restarts: "3"},
	)
	_, laterChangesByKey := reconcileRolloutTestFacts(t, laterFacts)
	if revertedImage, _ := putValue(laterChangesByKey, types.KeyDesiredServiceImage("web")); revertedImage != "app:a" {
		t.Errorf("later rollback reverted to %q, want app:a", revertedImage)
	}
}

// TestRolloutReasonNamesWaitingInstance: spec service-rollout, "New image cannot be pulled".
func TestRolloutReasonNamesWaitingInstance(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", state: types.InstanceImagePullBackoff, statusReason: "pull failed: not found"},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
	assertContainsAll(t, "rollout/reason", rolloutReason, "web-3", "image-pull-backoff", "pull failed: not found")
}

// TestRolloutReasonForPauseAndBudget: spec service-rollout, "Paused and budget-bound rollouts".
func TestRolloutReasonForPauseAndBudget(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		inputFacts := buildRolloutTestFacts("app:b",
			mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
				types.KeyDesiredServiceRolloutPaused("web"): "true",
			}),
			rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
			rolloutTestInstance{instanceID: "web-2", image: "app:b", state: types.InstanceRunning},
		)
		_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
		rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
		assertContainsAll(t, "rollout/reason", rolloutReason, "paused")
	})
	t.Run("max_unavailable used up", func(t *testing.T) {
		inputFacts := buildRolloutTestFacts("app:b",
			mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
				types.KeyDesiredServiceUpdateMaxUnavailable("web"): "1",
				types.KeyDesiredServiceInstances("web"):            "4",
			}),
			rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
			rolloutTestInstance{instanceID: "web-2", image: "app:a", state: types.InstanceRunning},
			rolloutTestInstance{instanceID: "web-3", image: "app:b", state: types.InstanceRunning, nodeFailure: true},
			rolloutTestInstance{instanceID: "web-4", image: "app:b", state: types.InstanceRunning},
		)
		proposedChanges, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
		if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 0 {
			t.Errorf("stopped %v with the budget used up, want none", stoppedIDs)
		}
		rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
		assertContainsAll(t, "rollout/reason", rolloutReason, "max_unavailable", "web-3")
	})
}

// unplacedTestFacts returns the scheduler facts of an unplaced instance that
// needs 2000m CPU and 1024 MiB memory.
func unplacedTestFacts(instanceID string) map[string]string {
	return map[string]string{
		types.KeyDerivedSchedulerUnplacedReason(instanceID):       string(types.UnplacedInsufficientCapacity),
		types.KeyDerivedSchedulerUnplacedRequirements(instanceID): `{"service":"web","cpu":2000,"memory":1024,"architecture":""}`,
	}
}

// TestRolloutRollsBackAfterProgressDeadlineWithReason: spec service-rollout,
// "No room for the new instance".
func TestRolloutRollsBackAfterProgressDeadlineWithReason(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", 11*time.Minute), unplacedTestFacts("web-3")),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", state: types.InstancePending},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if revertedImage, _ := putValue(changesByKey, types.KeyDesiredServiceImage("web")); revertedImage != "app:a" {
		t.Errorf("desired image = %q, want app:a", revertedImage)
	}
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutRollback) {
		t.Errorf("rollout/state = %q, want rollback", rolloutState)
	}
	rollbackReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRollbackReason("web"))
	assertContainsAll(t, "rollback_reason", rollbackReason, "no progress for 10m", "web-3", "insufficient_capacity", "cpu 2000m", "memory 1024Mi")
}

// TestRolloutProgressRestartsDeadline: spec service-rollout, "Progress restarts the deadline".
func TestRolloutProgressRestartsDeadline(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", 9*time.Minute), map[string]string{
			types.KeyDerivedServiceRolloutRunningNew("web"): "0",
			types.KeyDesiredServiceInstances("web"):         "3",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", image: "app:b", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	assertNoRollback(t, changesByKey)
	if progressAt, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutProgressAt("web")); progressAt != rolloutTestNow.Format(time.RFC3339) {
		t.Errorf("progress_at = %q, want now (%s)", progressAt, rolloutTestNow.Format(time.RFC3339))
	}
	if runningNew, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutRunningNew("web")); runningNew != "1" {
		t.Errorf("running_new = %q, want 1", runningNew)
	}
}

// TestRolloutPausedIgnoresProgressDeadline: spec service-rollout, "Paused
// rollout does not time out".
func TestRolloutPausedIgnoresProgressDeadline(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", 30*time.Minute), unplacedTestFacts("web-3"), map[string]string{
			types.KeyDesiredServiceRolloutPaused("web"): "true",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", state: types.InstancePending},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	assertNoRollback(t, changesByKey)
	if rolloutState, changed := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); changed {
		t.Errorf("paused rollout moved to %q, want it to stay rolling", rolloutState)
	}
}

// TestRolloutRollbackStallsWithReason: spec service-rollout, "Rollback that
// cannot progress".
func TestRolloutRollbackStallsWithReason(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:a",
		mergeFactMaps(rollingFacts(types.RolloutRollback, "app:a", "app:a", 11*time.Minute), unplacedTestFacts("web-3")),
		rolloutTestInstance{instanceID: "web-2", image: "app:b", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", state: types.InstancePending},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, inputFacts)
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutStalled) {
		t.Errorf("rollout/state = %q, want stalled", rolloutState)
	}
	rolloutReason, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutReason("web"))
	assertContainsAll(t, "rollout/reason", rolloutReason, "web-3", "insufficient_capacity")
	assertNoRollback(t, changesByKey)
}

// TestRolloutStartWhilePausedProposesEachKeyOnce checks that a rollout that
// starts paused, or starts and rolls back in the same pass, proposes every key
// once (a transaction never holds a key twice).
func TestRolloutStartWhilePausedProposesEachKeyOnce(t *testing.T) {
	pausedFacts := buildRolloutTestFacts("app:b",
		map[string]string{types.KeyDesiredServiceRolloutPaused("web"): "true"},
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
	)
	_, changesByKey := reconcileRolloutTestFacts(t, pausedFacts)
	if rolloutState, _ := putValue(changesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutRolling) {
		t.Errorf("rollout/state = %q, want rolling", rolloutState)
	}

	retargetFacts := buildRolloutTestFacts("app:c",
		mergeFactMaps(rollingFacts(types.RolloutRolling, "app:a", "app:b", time.Minute), map[string]string{
			types.KeyDesiredServiceInstances("web"): "1",
		}),
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:c", state: types.InstanceCrashLoopBackoff, restarts: "3"},
	)
	_, retargetChangesByKey := reconcileRolloutTestFacts(t, retargetFacts)
	if rolloutState, _ := putValue(retargetChangesByKey, types.KeyDerivedServiceRolloutState("web")); rolloutState != string(types.RolloutRollback) {
		t.Errorf("rollout/state = %q after retarget and failures in one pass, want rollback", rolloutState)
	}
}
