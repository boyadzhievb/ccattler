// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// orderRecordingRuntime is a simulator runtime that records every start and
// stop as "start <id>" / "stop <id>", in call order.
type orderRecordingRuntime struct {
	*runtime.SimulatorRuntime            // SimulatorRuntime runs the workloads.
	mutex                     sync.Mutex // mutex guards calls.
	calls                     []string   // calls lists the starts and stops in order.
}

// Start records the start and starts the workload.
func (recordingRuntime *orderRecordingRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	recordingRuntime.record("start " + spec.ID)
	return recordingRuntime.SimulatorRuntime.Start(ctx, spec)
}

// Stop records the stop and stops the workload.
func (recordingRuntime *orderRecordingRuntime) Stop(ctx context.Context, instanceID string) error {
	recordingRuntime.record("stop " + instanceID)
	return recordingRuntime.SimulatorRuntime.Stop(ctx, instanceID)
}

// record appends one call.
func (recordingRuntime *orderRecordingRuntime) record(call string) {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	recordingRuntime.calls = append(recordingRuntime.calls, call)
}

// recordedCalls returns a copy of the calls so far.
func (recordingRuntime *orderRecordingRuntime) recordedCalls() []string {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	return slices.Clone(recordingRuntime.calls)
}

// TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance: M241 2g.3 (decided
// order). With the old instance stopped and its replacement placed on the same
// node in one step, the agent starts the new instance before it stops the old,
// so the node never lacks the service during the pull, start and grace period.
func TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance(t *testing.T) {
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { factStore.Close() })
	recordingRuntime := &orderRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nodeAgent := New("node-1", factStore, recordingRuntime)
	nodeAgent.SetInterval(crashLoopTestInterval)
	nodeAgent.EnableTestHooks()
	factStore.Put(ctx, types.KeyDesiredServiceImage("agentd"), []byte("monitor:2.0"))
	factStore.Put(ctx, types.KeyDerivedInstanceImage("agentd-old"), []byte("monitor:1.0"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "agentd-old", Service: "agentd", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "agentd-old", NodeID: "node-1"})
	go nodeAgent.Run(ctx)
	waitFor(t, crashLoopTestTimeout, "agentd-old running", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("agentd-old")) == string(types.InstanceRunning)
	})

	nodeAgent.RunInAgentLoop(func() {
		factStore.Put(ctx, types.KeyObservedInstanceState("agentd-old"), []byte(string(types.InstanceStopped)))
		factStore.Put(ctx, types.KeyDerivedInstanceImage("agentd-new"), []byte("monitor:2.0"))
		types.WriteInstance(ctx, factStore, types.Instance{ID: "agentd-new", Service: "agentd", State: types.InstancePending})
		types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "agentd-new", NodeID: "node-1"})
	})
	waitFor(t, crashLoopTestTimeout, "agentd-new started before agentd-old stopped", func() bool {
		calls := recordingRuntime.recordedCalls()
		startIndex, stopIndex := slices.Index(calls, "start agentd-new"), slices.Index(calls, "stop agentd-old")
		if startIndex < 0 || stopIndex < 0 {
			return false
		}
		if stopIndex < startIndex {
			t.Fatalf("calls %v: agentd-old stopped before agentd-new started, want start-new-then-stop-old", calls)
		}
		return true
	})
}
