// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rolloutLoopStep is the simulated time between two passes of the loop tests:
// longer than the default min_healthy_time, so an instance started in one step
// is healthy in the next.
const rolloutLoopStep = 15 * time.Second

// rolloutLoopOldImage and rolloutLoopNewImage are the images the loop tests
// roll from and to.
const (
	rolloutLoopOldImage = "app:a"
	rolloutLoopNewImage = "app:b"
)

// rolloutLoopCluster is a small simulated cluster for the M241 D14 loop tests:
// the real instance and rollout controllers run on its facts, and a fake agent
// starts pending instances while the placement capacity allows.
type rolloutLoopCluster struct {
	t                  *testing.T          // t reports failures.
	serviceName        string              // serviceName is the one service of the cluster.
	facts              map[string]string   // facts are the store contents, key to value.
	now                time.Time           // now is the simulated clock.
	placementCapacity  int                 // placementCapacity caps running instances (per-node: the node count); 0 = unlimited.
	createdInstanceIDs []string            // createdInstanceIDs lists the IDs the instance controller created, in order.
	maxActiveCount     int                 // maxActiveCount is the most active (not stopped) instances seen after a pass.
	instanceController *InstanceController // instanceController is the real instance controller, with test IDs.
	rolloutController  *RolloutController  // rolloutController is the real rollout controller on the simulated clock.
}

// newRolloutLoopCluster returns a cluster whose service runs expectedCount
// instances on the old image, with the desired image already changed to the
// new one. stateful uses ordinal IDs; extraFacts add kind facts.
func newRolloutLoopCluster(t *testing.T, serviceName string, expectedCount int, stateful bool, extraFacts map[string]string) *rolloutLoopCluster {
	cluster := &rolloutLoopCluster{t: t, serviceName: serviceName, facts: map[string]string{}, now: rolloutTestNow}
	cluster.facts[types.KeyDesiredServiceImage(serviceName)] = rolloutLoopNewImage
	cluster.facts[types.KeyDesiredServiceInstances(serviceName)] = strconv.Itoa(expectedCount)
	cluster.facts[types.KeyEffectiveServiceInstances(serviceName)] = strconv.Itoa(expectedCount)
	for factKey, factValue := range extraFacts {
		cluster.facts[factKey] = factValue
	}
	for instanceIndex := range expectedCount {
		instanceID := fmt.Sprintf("%s-old-%d", serviceName, instanceIndex)
		if stateful {
			instanceID = fmt.Sprintf("%s-%d", serviceName, instanceIndex)
			cluster.facts[types.KeyObservedInstanceOrdinal(instanceID)] = strconv.Itoa(instanceIndex)
		}
		cluster.addRunningInstance(instanceID, rolloutLoopOldImage, time.Hour)
	}
	cluster.instanceController = NewInstanceController()
	cluster.instanceController.NewID = func() string {
		instanceID := fmt.Sprintf("%s-new-%02d", serviceName, len(cluster.createdInstanceIDs))
		cluster.createdInstanceIDs = append(cluster.createdInstanceIDs, instanceID)
		return instanceID
	}
	cluster.rolloutController = NewRolloutController()
	cluster.rolloutController.Now = func() time.Time { return cluster.now }
	return cluster
}

// addRunningInstance adds a running instance on image that started age ago.
func (cluster *rolloutLoopCluster) addRunningInstance(instanceID string, image string, age time.Duration) {
	startedAt := cluster.now.Add(-age).UTC().Format(time.RFC3339)
	cluster.facts[types.KeyObservedInstance(instanceID)] = ""
	cluster.facts[types.KeyObservedInstanceService(instanceID)] = cluster.serviceName
	cluster.facts[types.KeyObservedInstanceState(instanceID)] = string(types.InstanceRunning)
	cluster.facts[types.KeyObservedInstanceImage(instanceID)] = image
	cluster.facts[types.KeyDerivedInstanceImage(instanceID)] = image
	cluster.facts[types.KeyObservedInstanceStartedAt(instanceID)] = startedAt
	cluster.facts[types.KeyObservedInstanceCreatedAt(instanceID)] = startedAt
}

// snapshot returns the facts sorted by key, as a controller pass reads them.
func (cluster *rolloutLoopCluster) snapshot() []store.Fact {
	snapshotFacts := make([]store.Fact, 0, len(cluster.facts))
	for factKey, factValue := range cluster.facts {
		snapshotFacts = append(snapshotFacts, store.Fact{Key: factKey, Value: []byte(factValue)})
	}
	store.SortFacts(snapshotFacts)
	return snapshotFacts
}

// reconcileAndApply runs one controller on a snapshot and applies its changes.
func (cluster *rolloutLoopCluster) reconcileAndApply(controller Controller) {
	cluster.t.Helper()
	proposedChanges, reconcileError := controller.Reconcile(context.Background(), cluster.snapshot())
	if reconcileError != nil {
		cluster.t.Fatalf("%s: %v", controller.Name(), reconcileError)
	}
	for _, change := range proposedChanges {
		if change.Type == store.OpDelete {
			delete(cluster.facts, change.Key)
		} else {
			cluster.facts[change.Key] = string(change.Value)
		}
	}
}

// activeInstanceFields returns the fields of the service's instances that are
// not stopped, by ID.
func (cluster *rolloutLoopCluster) activeInstanceFields() map[string]map[string]string {
	activeFields := make(map[string]map[string]string)
	for instanceID, fields := range parseInstanceFieldsFromFacts(cluster.snapshot()) {
		if fields["service"] == cluster.serviceName && effectiveInstanceState(fields) != types.InstanceStopped {
			activeFields[instanceID] = fields
		}
	}
	return activeFields
}

// startPendingInstances is the fake agent: it starts pending instances on their
// recorded image, in ID order, while running instances stay within capacity.
func (cluster *rolloutLoopCluster) startPendingInstances() {
	activeFields := cluster.activeInstanceFields()
	runningCount := 0
	for _, fields := range activeFields {
		if effectiveInstanceState(fields) == types.InstanceRunning {
			runningCount++
		}
	}
	for _, instanceID := range sortedMapKeys(activeFields) {
		fields := activeFields[instanceID]
		if effectiveInstanceState(fields) != types.InstancePending {
			continue
		}
		if cluster.placementCapacity > 0 && runningCount >= cluster.placementCapacity {
			continue
		}
		runningCount++
		cluster.facts[types.KeyObservedInstanceState(instanceID)] = string(types.InstanceRunning)
		cluster.facts[types.KeyObservedInstanceImage(instanceID)] = fields["image"]
		cluster.facts[types.KeyObservedInstanceStartedAt(instanceID)] = cluster.now.UTC().Format(time.RFC3339)
		if fields["created-at"] == "" {
			cluster.facts[types.KeyObservedInstanceCreatedAt(instanceID)] = cluster.now.UTC().Format(time.RFC3339)
		}
	}
}

// runPasses runs instance controller, rollout controller and fake agent passes
// until the rollout state is finalState or passCount passes ran; it returns
// the number of passes run.
func (cluster *rolloutLoopCluster) runPasses(passCount int, finalState types.RolloutState) int {
	for passIndex := range passCount {
		cluster.reconcileAndApply(cluster.instanceController)
		cluster.reconcileAndApply(cluster.rolloutController)
		cluster.maxActiveCount = max(cluster.maxActiveCount, len(cluster.activeInstanceFields()))
		if cluster.rolloutState() == finalState {
			return passIndex + 1
		}
		cluster.startPendingInstances()
		cluster.now = cluster.now.Add(rolloutLoopStep)
	}
	return passCount
}

// rolloutState returns the service's stored rollout state.
func (cluster *rolloutLoopCluster) rolloutState() types.RolloutState {
	return types.RolloutState(cluster.facts[types.KeyDerivedServiceRolloutState(cluster.serviceName)])
}

// assertAllActiveOnImage fails unless every active instance runs image.
func (cluster *rolloutLoopCluster) assertAllActiveOnImage(image string, wantCount int) {
	cluster.t.Helper()
	activeFields := cluster.activeInstanceFields()
	if len(activeFields) != wantCount {
		cluster.t.Errorf("%d active instances at the end, want %d", len(activeFields), wantCount)
	}
	for instanceID, fields := range activeFields {
		if fields["image"] != image || effectiveInstanceState(fields) != types.InstanceRunning {
			cluster.t.Errorf("instance %s is %s on %q at the end, want running on %s", instanceID, effectiveInstanceState(fields), fields["image"], image)
		}
	}
}

// TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace: spec service-rollout,
// "Stateful and per-node services do not surge" (D14).
func TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace(t *testing.T) {
	t.Run("stateful, 1 instance", func(t *testing.T) {
		cluster := newRolloutLoopCluster(t, "db", 1, true, map[string]string{types.KeyEffectiveServiceStateful("db"): "true"})
		cluster.runPasses(20, types.RolloutComplete)
		if cluster.rolloutState() != types.RolloutComplete {
			t.Errorf("rollout/state = %q after 20 passes, want complete", cluster.rolloutState())
		}
		if cluster.maxActiveCount > 1 {
			t.Errorf("up to %d active instances, want at most 1 (no extra ordinal)", cluster.maxActiveCount)
		}
		if _, hasExtraOrdinal := cluster.facts[types.KeyObservedInstance("db-1")]; hasExtraOrdinal {
			t.Error("extra ordinal db-1 created, want db-0 replaced in place")
		}
		cluster.assertAllActiveOnImage(rolloutLoopNewImage, 1)
	})
	t.Run("per-node, 3 nodes", func(t *testing.T) {
		cluster := newRolloutLoopCluster(t, "agentd", 3, false, map[string]string{types.KeyEffectiveServicePlacementMode("agentd"): types.PlacementModePerNode})
		cluster.placementCapacity = 3
		cluster.runPasses(20, types.RolloutComplete)
		if cluster.rolloutState() != types.RolloutComplete {
			t.Errorf("rollout/state = %q after 20 passes, want complete", cluster.rolloutState())
		}
		if cluster.maxActiveCount > 3 {
			t.Errorf("up to %d active instances, want at most 3 (no surge instance)", cluster.maxActiveCount)
		}
		cluster.assertAllActiveOnImage(rolloutLoopNewImage, 3)
	})
}

// TestRolloutPerNodeEightNodesCompletes: review round 2 H2. An 8-node per-node
// rollout finishes before the 10-minute progress deadline instead of being
// rolled back for want of room for a surge instance.
func TestRolloutPerNodeEightNodesCompletes(t *testing.T) {
	cluster := newRolloutLoopCluster(t, "agentd", 8, false, map[string]string{types.KeyEffectiveServicePlacementMode("agentd"): types.PlacementModePerNode})
	cluster.placementCapacity = 8
	cluster.runPasses(60, types.RolloutComplete)
	if cluster.rolloutState() != types.RolloutComplete {
		t.Errorf("rollout/state = %q, desired image %q, want complete on %s", cluster.rolloutState(),
			cluster.facts[types.KeyDesiredServiceImage("agentd")], rolloutLoopNewImage)
	}
	cluster.assertAllActiveOnImage(rolloutLoopNewImage, 8)
}

// TestRolloutRollbackWithDefaultPacingCompletes: spec service-rollout, "A
// rollback can always move" (D14). A 3-instance service with default pacing
// (max_unavailable 0, no surge after the rollback) rolls back to a.
func TestRolloutRollbackWithDefaultPacingCompletes(t *testing.T) {
	cluster := newRolloutLoopCluster(t, "web", 1, false, nil)
	cluster.facts[types.KeyDesiredServiceInstances("web")] = "3"
	cluster.facts[types.KeyEffectiveServiceInstances("web")] = "3"
	cluster.facts[types.KeyDesiredServiceImage("web")] = rolloutLoopOldImage
	cluster.addRunningInstance("web-b-1", rolloutLoopNewImage, 10*time.Minute)
	cluster.addRunningInstance("web-b-2", rolloutLoopNewImage, 10*time.Minute)
	for factKey, factValue := range map[string]string{
		types.KeyDerivedServiceRolloutState("web"):          string(types.RolloutRollback),
		types.KeyDerivedServiceRolloutImage("web"):          rolloutLoopOldImage,
		types.KeyDerivedServiceRolloutTargetImage("web"):    rolloutLoopOldImage,
		types.KeyDerivedServiceRolloutRevision("web"):       "1",
		types.KeyDerivedServiceRolloutRollbackReason("web"): "3 failures ≥ 3 on app:b: exited with code 1",
		types.KeyDerivedServiceRolloutProgressAt("web"):     cluster.now.Format(time.RFC3339),
		types.KeyDerivedServiceRolloutStartedAt("web"):      cluster.now.Format(time.RFC3339),
	} {
		cluster.facts[factKey] = factValue
	}
	cluster.runPasses(20, types.RolloutRolledBack)
	if cluster.rolloutState() != types.RolloutRolledBack {
		t.Errorf("rollout/state = %q after 20 passes, want rolled-back", cluster.rolloutState())
	}
	cluster.assertAllActiveOnImage(rolloutLoopOldImage, 3)
}

// TestRolloutSurgeEndsWithLastOldInstance: spec service-rollout, "No surplus
// instance at the end" (D14). A 1-instance stateless service surges one new
// instance, and no other is created or scaled down after the old one stops.
func TestRolloutSurgeEndsWithLastOldInstance(t *testing.T) {
	cluster := newRolloutLoopCluster(t, "web", 1, false, nil)
	cluster.runPasses(20, types.RolloutComplete)
	cluster.runPasses(4, "")
	if cluster.rolloutState() != types.RolloutComplete {
		t.Errorf("rollout/state = %q, want complete", cluster.rolloutState())
	}
	if len(cluster.createdInstanceIDs) != 1 {
		t.Errorf("instance controller created %v, want exactly one new instance", cluster.createdInstanceIDs)
	}
	for _, createdID := range cluster.createdInstanceIDs {
		if cluster.facts[types.KeyDerivedInstanceControllerStopped(createdID)] == "true" {
			t.Errorf("new instance %s scaled down after the rollout", createdID)
		}
	}
	if surgeAllowance, hasSurge := cluster.facts[types.KeyDerivedServiceRolloutSurgeAllowance("web")]; hasSurge {
		t.Errorf("surge_allowance = %q after the last old instance stopped, want none", surgeAllowance)
	}
	cluster.assertAllActiveOnImage(rolloutLoopNewImage, 1)
}
