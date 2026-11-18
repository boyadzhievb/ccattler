// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// perNodeReplayStep is the simulated time between two passes of the replay.
const perNodeReplayStep = 15 * time.Second

// perNodeReplayNodes are the nodes of the replayed testbed cluster.
var perNodeReplayNodes = []string{"node-1", "node-2", "node-3"}

// perNodeReplayCluster replays the testbed's per-node resource stall (M241
// 2i): the real instance controller, rollout controller and scheduler run on
// its facts; a fake agent per node reads the states at the start of a pass
// and publishes them after the controllers ran, as the real agent's cycle does.
type perNodeReplayCluster struct {
	testingReporter    *testing.T                      // testingReporter reports failures.
	facts              map[string]string               // facts are the store contents, key to value.
	now                time.Time                       // now is the simulated clock.
	agentUsesCAS       bool                            // agentUsesCAS: the agent never overwrites a stopped it did not write (2i.1).
	containers         map[string]string               // containers maps instance ID to the image its container runs.
	instanceController *controllers.InstanceController // instanceController is the real instance controller.
	rolloutController  *controllers.RolloutController  // rolloutController is the real rollout controller.
	placementScheduler *Scheduler                      // placementScheduler is the real scheduler.
	createdCount       int                             // createdCount numbers the created instance IDs.
}

// newPerNodeReplayCluster returns a 3-node cluster (2000m each) running the
// per-node service agentd (1500m per instance) on app:a, desired app:b.
func newPerNodeReplayCluster(t *testing.T, agentUsesCAS bool) *perNodeReplayCluster {
	cluster := &perNodeReplayCluster{testingReporter: t, facts: map[string]string{}, agentUsesCAS: agentUsesCAS,
		now: time.Date(2026, 11, 18, 20, 4, 5, 0, time.UTC), containers: map[string]string{},
		rolloutController: controllers.NewRolloutController(), placementScheduler: newTestScheduler()}
	cluster.instanceController = controllers.NewInstanceController()
	cluster.instanceController.NewID = func() string {
		cluster.createdCount++
		return fmt.Sprintf("agentd-new-%02d", cluster.createdCount)
	}
	cluster.rolloutController.Now = func() time.Time { return cluster.now }
	for factKey, factValue := range map[string]string{
		types.KeyDesiredService("agentd"):                     "",
		types.KeyDesiredServiceImage("agentd"):                "app:b",
		types.KeyDesiredServicePlacementMode("agentd"):        string(types.PlacementModePerNode),
		types.KeyEffectiveServicePlacementMode("agentd"):      string(types.PlacementModePerNode),
		types.KeyEffectiveServiceInstances("agentd"):          "3",
		types.KeyDesiredServiceResourcesCPU("agentd"):         "1500",
		types.KeyDesiredServiceUpdateMinHealthyTime("agentd"): "0s",
	} {
		cluster.facts[factKey] = factValue
	}
	for nodeIndex, nodeID := range perNodeReplayNodes {
		cluster.facts[types.KeyObservedNodeState(nodeID)] = string(types.NodeAlive)
		cluster.facts[types.KeyObservedNodeAvailableCPU(nodeID)] = "2000"
		cluster.facts[types.KeyObservedNodeAvailableMemory(nodeID)] = "4096"
		instanceID := fmt.Sprintf("agentd-old-%d", nodeIndex)
		cluster.facts[types.KeyObservedInstance(instanceID)] = ""
		cluster.facts[types.KeyObservedInstanceService(instanceID)] = "agentd"
		cluster.facts[types.KeyDerivedInstanceImage(instanceID)] = "app:a"
		cluster.facts[types.KeyPlacementInstance(instanceID)] = nodeID
		cluster.facts[types.KeyPlacementNodeInstance(nodeID, instanceID)] = ""
		cluster.startContainer(instanceID, "app:a", time.Hour)
	}
	return cluster
}

// startContainer starts instanceID's container on image, started age ago.
func (cluster *perNodeReplayCluster) startContainer(instanceID string, image string, age time.Duration) {
	cluster.containers[instanceID] = image
	cluster.facts[types.KeyObservedInstanceState(instanceID)] = string(types.InstanceRunning)
	cluster.facts[types.KeyObservedInstanceImage(instanceID)] = image
	cluster.facts[types.KeyObservedInstanceStartedAt(instanceID)] = cluster.now.Add(-age).UTC().Format(time.RFC3339)
	cluster.facts[types.KeyObservedInstanceCreatedAt(instanceID)] = cluster.now.Add(-age).UTC().Format(time.RFC3339)
}

// snapshot returns the facts sorted by key, as a controller pass reads them.
func (cluster *perNodeReplayCluster) snapshot() []store.Fact {
	snapshotFacts := make([]store.Fact, 0, len(cluster.facts))
	for factKey, factValue := range cluster.facts {
		snapshotFacts = append(snapshotFacts, store.Fact{Key: factKey, Value: []byte(factValue)})
	}
	store.SortFacts(snapshotFacts)
	return snapshotFacts
}

// reconcileAndApply runs one controller on a snapshot and applies its changes.
func (cluster *perNodeReplayCluster) reconcileAndApply(controller controllers.Controller) {
	cluster.testingReporter.Helper()
	proposedChanges, reconcileError := controller.Reconcile(context.Background(), cluster.snapshot())
	if reconcileError != nil {
		cluster.testingReporter.Fatalf("%s: %v", controller.Name(), reconcileError)
	}
	for _, change := range proposedChanges {
		if change.Type == store.OpDelete {
			delete(cluster.facts, change.Key)
		} else {
			cluster.facts[change.Key] = string(change.Value)
		}
	}
}

// isStopped reports whether the instance is stopped by its observed state or
// the controller_stopped marker.
func (cluster *perNodeReplayCluster) isStopped(instanceID string) bool {
	return cluster.facts[types.KeyObservedInstanceState(instanceID)] == string(types.InstanceStopped) ||
		cluster.facts[types.KeyDerivedInstanceControllerStopped(instanceID)] == "true"
}

// publishRunningFromStaleRead is the agent's running-path publish of a cycle
// that read the states before the controllers ran: without CAS it writes
// running over a stop written meanwhile (the testbed's .777 overwrite).
func (cluster *perNodeReplayCluster) publishRunningFromStaleRead(statesReadAtCycleStart map[string]string) {
	for _, instanceID := range sortedKeys(cluster.containers) {
		if statesReadAtCycleStart[instanceID] != string(types.InstanceRunning) {
			continue
		}
		if cluster.agentUsesCAS && cluster.facts[types.KeyObservedInstanceState(instanceID)] != statesReadAtCycleStart[instanceID] {
			continue
		}
		cluster.facts[types.KeyObservedInstanceState(instanceID)] = string(types.InstanceRunning)
	}
}

// applyAgentCycle stops the containers of stopped instances (publishing
// stopped) and starts placed pending instances on their recorded image; a
// controller_stopped instance without a container is skipped, its observed
// state left as it is, as the real agent does.
func (cluster *perNodeReplayCluster) applyAgentCycle() {
	for _, instanceID := range sortedKeys(cluster.containers) {
		if cluster.isStopped(instanceID) {
			delete(cluster.containers, instanceID)
			cluster.facts[types.KeyObservedInstanceState(instanceID)] = string(types.InstanceStopped)
		}
	}
	for _, instanceID := range cluster.placedInstanceIDs() {
		_, hasContainer := cluster.containers[instanceID]
		if hasContainer || cluster.isStopped(instanceID) ||
			cluster.facts[types.KeyObservedInstanceState(instanceID)] != string(types.InstancePending) {
			continue
		}
		cluster.startContainer(instanceID, cluster.facts[types.KeyDerivedInstanceImage(instanceID)], 0)
	}
}

// placedInstanceIDs returns the IDs of all placed instances, sorted.
func (cluster *perNodeReplayCluster) placedInstanceIDs() []string {
	var placedIDs []string
	for factKey := range cluster.facts {
		if instanceID, isPlacement := strings.CutPrefix(factKey, types.ScanPlacements); isPlacement && !strings.Contains(instanceID, "/") {
			placedIDs = append(placedIDs, instanceID)
		}
	}
	sort.Strings(placedIDs)
	return placedIDs
}

// runReplayPass runs one pass in the testbed order: the agent reads, rollout
// stops, instance creates, the agent publishes from its stale read, instance
// marks the surplus, rollout re-stops, instance creates, the scheduler places
// (the surplus first by ID), the agent applies.
func (cluster *perNodeReplayCluster) runReplayPass() {
	statesReadAtCycleStart := make(map[string]string)
	for instanceID := range cluster.containers {
		statesReadAtCycleStart[instanceID] = cluster.facts[types.KeyObservedInstanceState(instanceID)]
	}
	cluster.reconcileAndApply(cluster.rolloutController)
	cluster.reconcileAndApply(cluster.instanceController)
	cluster.publishRunningFromStaleRead(statesReadAtCycleStart)
	cluster.reconcileAndApply(cluster.instanceController)
	cluster.reconcileAndApply(cluster.rolloutController)
	cluster.reconcileAndApply(cluster.instanceController)
	cluster.reconcileAndApply(cluster.placementScheduler)
	cluster.applyAgentCycle()
	cluster.now = cluster.now.Add(perNodeReplayStep)
}

// activeInstancesByNode maps each node to its placed instances that are not stopped.
func (cluster *perNodeReplayCluster) activeInstancesByNode() map[string][]string {
	activeByNode := make(map[string][]string)
	for _, instanceID := range cluster.placedInstanceIDs() {
		if !cluster.isStopped(instanceID) {
			nodeID := cluster.facts[types.KeyPlacementInstance(instanceID)]
			activeByNode[nodeID] = append(activeByNode[nodeID], instanceID)
		}
	}
	return activeByNode
}

// sortedKeys returns the keys of a string map, sorted.
func sortedKeys(stringMap map[string]string) []string {
	keys := make([]string, 0, len(stringMap))
	for key := range stringMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestPerNodeResourceRolloutSurvivesStaleStateOverwrite: testbed stall after
// 2g (M241 2i). A per-node rollout whose instances request 1500m of 2000m
// nodes, with the agent overwriting a stop from a stale read (pre-2i agent) or
// not (CAS), ends complete with exactly one running app:b instance per node.
func TestPerNodeResourceRolloutSurvivesStaleStateOverwrite(t *testing.T) {
	for _, agentUsesCAS := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent CAS %v", agentUsesCAS), func(t *testing.T) {
			cluster := newPerNodeReplayCluster(t, agentUsesCAS)
			for passIndex := 0; passIndex < 30 && cluster.facts[types.KeyDerivedServiceRolloutState("agentd")] != string(types.RolloutComplete); passIndex++ {
				cluster.runReplayPass()
			}
			if rolloutState := cluster.facts[types.KeyDerivedServiceRolloutState("agentd")]; rolloutState != string(types.RolloutComplete) {
				t.Errorf("rollout/state = %q after 30 passes, want complete (reason %q)", rolloutState,
					cluster.facts[types.KeyDerivedServiceRolloutReason("agentd")])
			}
			activeByNode := cluster.activeInstancesByNode()
			for _, nodeID := range perNodeReplayNodes {
				activeIDs := activeByNode[nodeID]
				if len(activeIDs) != 1 {
					t.Errorf("%s hosts active instances %v, want exactly one", nodeID, activeIDs)
					continue
				}
				if image := cluster.containers[activeIDs[0]]; image != "app:b" ||
					cluster.facts[types.KeyObservedInstanceState(activeIDs[0])] != string(types.InstanceRunning) {
					t.Errorf("%s: %s is %s with container %q, want running app:b", nodeID, activeIDs[0],
						cluster.facts[types.KeyObservedInstanceState(activeIDs[0])], image)
				}
			}
		})
	}
}
