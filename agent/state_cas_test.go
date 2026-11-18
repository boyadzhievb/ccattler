// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// concurrentStopRuntime is a simulator runtime whose Status, once armed,
// writes observed state stopped for one instance: another component's stop
// landing between the agent's read and its publish in one cycle.
type concurrentStopRuntime struct {
	*runtime.SimulatorRuntime                  // SimulatorRuntime runs the workloads.
	factStore                 store.StateStore // factStore receives the concurrent stop.
	mutex                     sync.Mutex       // mutex guards armedInstanceID.
	armedInstanceID           string           // armedInstanceID is the instance to stop on the next Status; "" when disarmed.
}

// Status writes the armed stop once, then reports the workload's status.
func (stopRuntime *concurrentStopRuntime) Status(ctx context.Context, instanceID string) (runtime.Status, error) {
	stopRuntime.mutex.Lock()
	if stopRuntime.armedInstanceID == instanceID {
		stopRuntime.armedInstanceID = ""
		stopRuntime.factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceStopped)))
	}
	stopRuntime.mutex.Unlock()
	return stopRuntime.SimulatorRuntime.Status(ctx, instanceID)
}

// TestAgentNeverOverwritesConcurrentStop: testbed stall after 2g (M241 2i.1).
// A rollout writes stopped while the agent's running-path cycle is between its
// read and its publish; the stop survives the publish, and the agent then
// stops the container instead of reporting it running again.
func TestAgentNeverOverwritesConcurrentStop(t *testing.T) {
	factStore := newStoreWithRecordedImage(t, "web-1", pinnedTestImage)
	stopRuntime := &concurrentStopRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), factStore: factStore}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nodeAgent := New("node-1", factStore, stopRuntime)
	nodeAgent.SetInterval(crashLoopTestInterval)
	nodeAgent.EnableTestHooks()
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte(pinnedTestImage))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "web-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "web-1", NodeID: "node-1"})
	go nodeAgent.Run(ctx)
	waitFor(t, crashLoopTestTimeout, "web-1 running", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("web-1")) == string(types.InstanceRunning)
	})

	nodeAgent.RunInAgentLoop(func() {
		stopRuntime.mutex.Lock()
		stopRuntime.armedInstanceID = "web-1"
		stopRuntime.mutex.Unlock()
	})
	waitFor(t, crashLoopTestTimeout, "the concurrent stop kept and web-1's container stopped", func() bool {
		if _, statusError := stopRuntime.SimulatorRuntime.Status(ctx, "web-1"); statusError == nil {
			return false
		}
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("web-1")) == string(types.InstanceStopped)
	})
}
