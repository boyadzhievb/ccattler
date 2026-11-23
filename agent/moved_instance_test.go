// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// movedInstanceTestInstanceID is the instance every moved-instance case cleans up.
const movedInstanceTestInstanceID = "web-1"

// movedInstanceTestCase describes the facts around a container that left
// node-1's desired set and what node-1's agent must do with it.
type movedInstanceTestCase struct {
	name                 string // name identifies the sub-test.
	placementNodeID      string // placementNodeID is the node in placement/instance/{id}; "" means no placement.
	controllerStopped    bool   // controllerStopped writes derived/instance/{id}/controller_stopped = true.
	containerExited      bool   // containerExited kills the container first, so cleanupStoppedStaleInstance runs.
	placementReadFails   bool   // placementReadFails makes the agent's read of the placement fail in the first cycle.
	wantContainerRemoved bool   // wantContainerRemoved is whether the first cycle removes the container.
	wantStoppedWritten   bool   // wantStoppedWritten is whether the first cycle writes observed state stopped.
}

// TestAgentDoesNotMarkMovedInstanceStopped: spec instance-rebalancing, "Source
// agent after a move" and "Scale-down still stops". node-1's agent removes the
// container, and writes stopped only when the instance is not placed on another
// node or a controller stopped it. A failed placement read leaves container and
// store untouched; the next cycle, with a readable placement, does the cleanup.
func TestAgentDoesNotMarkMovedInstanceStopped(t *testing.T) {
	testCases := []movedInstanceTestCase{
		{name: "moved running", placementNodeID: "node-2", wantContainerRemoved: true},
		{name: "moved exited", placementNodeID: "node-2", containerExited: true, wantContainerRemoved: true},
		{name: "controller stop", placementNodeID: "node-1", controllerStopped: true, wantContainerRemoved: true, wantStoppedWritten: true},
		{name: "controller stop after move", placementNodeID: "node-2", controllerStopped: true, wantContainerRemoved: true, wantStoppedWritten: true},
		{name: "no placement", wantContainerRemoved: true, wantStoppedWritten: true},
		{name: "no placement exited", containerExited: true, wantContainerRemoved: true, wantStoppedWritten: true},
		{name: "placement read error", placementReadFails: true},
		{name: "placement read error exited", containerExited: true, placementReadFails: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runMovedInstanceTestCase(t, testCase)
		})
	}
}

// runMovedInstanceTestCase starts a container for web-1 on node-1, writes the
// case's facts, runs the matching cleanup directly (synchronously, so the
// absence of a removal or write is checked after it returned) and checks both.
// A read-error case then runs a second cycle with the read working.
func runMovedInstanceTestCase(t *testing.T, testCase movedInstanceTestCase) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	failingStore := &failingKeyStore{StateStore: memoryStore, failedKey: types.KeyPlacementInstance(movedInstanceTestInstanceID)}
	simulatorRuntime := runtime.NewSimulatorRuntime()

	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	types.WriteInstance(ctx, memoryStore, types.Instance{ID: movedInstanceTestInstanceID, Service: "web", State: types.InstanceRunning})
	if testCase.placementNodeID != "" {
		types.WritePlacement(ctx, memoryStore, types.Placement{InstanceID: movedInstanceTestInstanceID, NodeID: testCase.placementNodeID})
	}
	if testCase.controllerStopped {
		_, _ = memoryStore.Put(ctx, types.KeyDerivedInstanceControllerStopped(movedInstanceTestInstanceID), []byte("true"))
	}
	if startError := simulatorRuntime.Start(ctx, runtime.Spec{ID: movedInstanceTestInstanceID, ServiceName: "web", Image: "nginx:1.27"}); startError != nil {
		t.Fatal(startError)
	}
	if testCase.containerExited {
		simulatorRuntime.KillWorkload(movedInstanceTestInstanceID, 1, "crash")
	}
	failingStore.failGets.Store(testCase.placementReadFails)

	nodeAgent := New("node-1", failingStore, simulatorRuntime)
	runStaleContainerCleanup(ctx, nodeAgent, testCase.containerExited)
	checkStaleContainerCleanup(t, ctx, "first cycle", memoryStore, simulatorRuntime, testCase.wantContainerRemoved, testCase.wantStoppedWritten)
	if !testCase.placementReadFails {
		return
	}
	if failingStore.failedGetCount.Load() == 0 {
		t.Fatalf("the placement was never read; the read-error case proves nothing")
	}
	failingStore.failGets.Store(false)
	runStaleContainerCleanup(ctx, nodeAgent, testCase.containerExited)
	checkStaleContainerCleanup(t, ctx, "next cycle, placement readable", memoryStore, simulatorRuntime, true, true)
}

// runStaleContainerCleanup runs the cleanup the agent's cycle picks for a
// container no longer desired on the node: exited or still running.
func runStaleContainerCleanup(ctx context.Context, nodeAgent *Agent, containerExited bool) {
	if containerExited {
		nodeAgent.cleanupStoppedStaleInstance(ctx, movedInstanceTestInstanceID)
		return
	}
	nodeAgent.cleanupUndesiredInstance(ctx, movedInstanceTestInstanceID)
}

// checkStaleContainerCleanup checks whether the container is gone from the
// runtime and whether observed state stopped was written.
func checkStaleContainerCleanup(t *testing.T, ctx context.Context, cycleName string, factStore store.StateStore,
	simulatorRuntime *runtime.SimulatorRuntime, wantContainerRemoved bool, wantStoppedWritten bool) {
	t.Helper()
	_, statusError := simulatorRuntime.Status(ctx, movedInstanceTestInstanceID)
	if containerRemoved := statusError != nil; containerRemoved != wantContainerRemoved {
		t.Errorf("%s: container removed = %v, want %v", cycleName, containerRemoved, wantContainerRemoved)
	}
	stoppedWritten := readFactValue(ctx, factStore, types.KeyObservedInstanceState(movedInstanceTestInstanceID)) == string(types.InstanceStopped)
	if stoppedWritten != wantStoppedWritten {
		t.Errorf("%s: stopped written = %v, want %v", cycleName, stoppedWritten, wantStoppedWritten)
	}
}
