// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"slices"
	"sync"
	"testing"

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
	if stateHistory := recordingStore.writtenStateHistory(); slices.Contains(stateHistory, string(types.InstanceFailed)) {
		t.Errorf("state failed was published for an instant exit; state writes: %v", stateHistory)
	}
}
