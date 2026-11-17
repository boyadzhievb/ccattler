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

// stateWriteRecordingStore is a store that records every value written to the
// observed state key of one instance, so a test can prove a state was never
// published even for a moment (M238).
type stateWriteRecordingStore struct {
	store.StateStore            // StateStore holds the facts.
	watchedStateKey  string     // watchedStateKey is observed/instance/{id}/state of the watched instance.
	mutex            sync.Mutex // mutex guards writtenStates.
	writtenStates    []string   // writtenStates lists every value written to watchedStateKey, in order.
}

// newStateWriteRecordingStore wraps a memory store and records the state writes of instanceID.
func newStateWriteRecordingStore(t *testing.T, instanceID string) *stateWriteRecordingStore {
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	return &stateWriteRecordingStore{
		StateStore:      memoryStore,
		watchedStateKey: types.KeyObservedInstanceState(instanceID),
	}
}

// Put records a write to the watched state key, then writes the fact.
func (recordingStore *stateWriteRecordingStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	if key == recordingStore.watchedStateKey {
		recordingStore.mutex.Lock()
		recordingStore.writtenStates = append(recordingStore.writtenStates, string(value))
		recordingStore.mutex.Unlock()
	}
	return recordingStore.StateStore.Put(ctx, key, value)
}

// writtenStateHistory returns a copy of every value written to the watched state key.
func (recordingStore *stateWriteRecordingStore) writtenStateHistory() []string {
	recordingStore.mutex.Lock()
	defer recordingStore.mutex.Unlock()
	return append([]string(nil), recordingStore.writtenStates...)
}

// TestAgentBacksOffInstantExitInPlace checks that a container which has
// already exited with code 1 when the agent first looks at it after the start
// is restarted in place with back-off and never published failed (spec
// instance-restart, M238).
func TestAgentBacksOffInstantExitInPlace(t *testing.T) {
	recordingStore := newStateWriteRecordingStore(t, "instant1")
	crashRuntime := newCrashingRuntime()
	crashRuntime.exitBeforeFirstLook = true
	_, factStore, ctx := startCrashLoopTestAgentOnStore(t, recordingStore, "instant1", crashLoopTestBaseDelay, crashRuntime)

	wantReason := "exited with code 1; restarting in " + (2 * crashLoopTestBaseDelay).String()
	waitFor(t, crashLoopTestTimeout, "second exit waiting in crash-loop-backoff with one restart counted", func() bool {
		return len(crashRuntime.startsOf("instant1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceStatusReason("instant1")) == wantReason &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("instant1")) == string(types.InstanceCrashLoopBackoff) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("instant1")) == "1"
	})
	assertStateFailedNeverWritten(t, recordingStore, "an instant exit")
}

// assertStateFailedNeverWritten fails the test when the watched instance was
// ever published as failed.
func assertStateFailedNeverWritten(t *testing.T, recordingStore *stateWriteRecordingStore, situation string) {
	t.Helper()
	if stateHistory := recordingStore.writtenStateHistory(); slices.Contains(stateHistory, string(types.InstanceFailed)) {
		t.Errorf("state failed was published for %s; state writes: %v", situation, stateHistory)
	}
}

// processStartErrorReason is the start error of a workload whose entrypoint
// does not exist, as the containerd runtime reports it with runc 1.1.15.
const processStartErrorReason = `create task: failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "/no/such/binary": stat /no/such/binary: no such file or directory: unknown`

// TestAgentBacksOffProcessStartFailureInPlace checks that a start whose
// workload process cannot start (StartError with ProcessFailed) is retried in
// place after the back-off, counted, shown as crash-loop-backoff with the start
// error as the reason, and never published failed (spec instance-restart, M238).
func TestAgentBacksOffProcessStartFailureInPlace(t *testing.T) {
	recordingStore := newStateWriteRecordingStore(t, "nobinary1")
	crashRuntime := newCrashingRuntime()
	crashRuntime.failingStartError = &runtime.StartError{Reason: processStartErrorReason, ProcessFailed: true}
	_, factStore, ctx := startCrashLoopTestAgentOnStore(t, recordingStore, "nobinary1", crashLoopTestBaseDelay, crashRuntime)

	wantReason := "start failed: " + processStartErrorReason + "; restarting in " + (2 * crashLoopTestBaseDelay).String()
	waitFor(t, crashLoopTestTimeout, "second failed start waiting in crash-loop-backoff with one restart counted", func() bool {
		return len(crashRuntime.startsOf("nobinary1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceStatusReason("nobinary1")) == wantReason &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("nobinary1")) == string(types.InstanceCrashLoopBackoff) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("nobinary1")) == "1" &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceExitReason("nobinary1")) == processStartErrorReason
	})
	startTimes := crashRuntime.startsOf("nobinary1")
	if gap := startTimes[1].Sub(startTimes[0]); gap < crashLoopTestBaseDelay {
		t.Errorf("second start came %v after the first, want at least the base delay %v", gap, crashLoopTestBaseDelay)
	}
	assertStateFailedNeverWritten(t, recordingStore, "a workload process that cannot start")
}

// TestAgentPublishesFailedForInfrastructureStartError checks that a start
// error that is not the workload's own (no ProcessFailed: network setup) is
// still published failed, so the failure controller replaces the instance
// (spec instance-restart, M238).
func TestAgentPublishesFailedForInfrastructureStartError(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	crashRuntime.failingStartError = &runtime.StartError{Reason: "CNI setup: no IP addresses available"}
	_, factStore, ctx := startCrashLoopTestAgent(t, "cni1", crashLoopTestBaseDelay, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "instance failed with the start error as the reason", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("cni1")) == string(types.InstanceFailed) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceStatusReason("cni1")) ==
				"start failed: failed to start cni1: CNI setup: no IP addresses available"
	})
	if restarts := readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("cni1")); restarts != "" {
		t.Errorf("restarts = %q after infrastructure start failures, want none", restarts)
	}
}

// TestAgentRunningPathRaceDoesNotPublishFailed checks that a container which
// exits after List reported it running but before the agent reads its status
// is not published failed; the next cycle restarts it in place (spec
// instance-restart, M238 D4).
func TestAgentRunningPathRaceDoesNotPublishFailed(t *testing.T) {
	recordingStore := newStateWriteRecordingStore(t, "race1")
	crashRuntime := newCrashingRuntime()
	crashRuntime.stableFromStartNumber = 1 // workloads run on until the test makes them exit
	_, factStore, ctx := startCrashLoopTestAgentOnStore(t, recordingStore, "race1", crashLoopTestBaseDelay, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "instance running", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("race1")) == string(types.InstanceRunning)
	})
	crashRuntime.exitAfterNextListing("race1")

	waitFor(t, crashLoopTestTimeout, "exited instance restarted in place with one restart counted", func() bool {
		return len(crashRuntime.startsOf("race1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("race1")) == "1" &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("race1")) == string(types.InstanceRunning)
	})
	assertStateFailedNeverWritten(t, recordingStore, "a container that exited between List and Status")
}
