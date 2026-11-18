// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// failingRecordedImageStore fails every Get of an instance's recorded image
// while failGets is set, as a store blip would.
type failingRecordedImageStore struct {
	store.StateStore              // StateStore serves every other call.
	failedKey        string       // failedKey is the recorded-image key whose reads fail.
	failGets         atomic.Bool  // failGets makes reads of failedKey fail.
	failedGetCount   atomic.Int64 // failedGetCount counts the reads that failed.
}

// Get fails reads of failedKey while failGets is set.
func (failingStore *failingRecordedImageStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	if failingStore.failGets.Load() && key == failingStore.failedKey {
		failingStore.failedGetCount.Add(1)
		return nil, errors.New("store unavailable")
	}
	return failingStore.StateStore.Get(ctx, key)
}

// TestAgentSkipsStartWhenRecordedImageUnreadable: review B5. Only a missing
// recorded image falls back to the service image; another read error skips the
// start this cycle, and the next cycle starts the recorded image.
func TestAgentSkipsStartWhenRecordedImageUnreadable(t *testing.T) {
	memoryStore := newStoreWithRecordedImage(t, "skip1", pinnedTestImage)
	failingStore := &failingRecordedImageStore{StateStore: memoryStore, failedKey: types.KeyDerivedInstanceImage("skip1")}
	failingStore.failGets.Store(true)
	ctx := startImageTestAgent(t, failingStore, "skip1")

	waitFor(t, crashLoopTestTimeout, "three cycles with an unreadable recorded image and no start", func() bool {
		if startedImage := readFactValue(ctx, memoryStore, types.KeyObservedInstanceImage("skip1")); startedImage != "" {
			t.Fatalf("agent started %q while the recorded image could not be read, want no start", startedImage)
		}
		return failingStore.failedGetCount.Load() >= 3
	})
	failingStore.failGets.Store(false)
	waitFor(t, crashLoopTestTimeout, "instance running with its recorded image", func() bool {
		return readFactValue(ctx, memoryStore, types.KeyObservedInstanceState("skip1")) == string(types.InstanceRunning) &&
			readFactValue(ctx, memoryStore, types.KeyObservedInstanceImage("skip1")) == pinnedTestImage
	})
}

// TestAgentResetsReadinessOnStart: review B4. A readiness result from before a
// start does not survive it: the instance is running without a readiness fact
// until its probe passes again.
func TestAgentResetsReadinessOnStart(t *testing.T) {
	factStore := newStoreWithRecordedImage(t, "ready1", pinnedTestImage)
	readinessKey := types.KeyObservedInstanceProbeState("ready1", types.ProbeReadiness)
	if _, putError := factStore.Put(context.Background(), readinessKey, []byte(string(types.ReadinessProbeReady))); putError != nil {
		t.Fatal(putError)
	}
	ctx := startImageTestAgent(t, factStore, "ready1")
	waitFor(t, crashLoopTestTimeout, "instance running without the readiness result from before its start", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("ready1")) == string(types.InstanceRunning) &&
			readFactValue(ctx, factStore, readinessKey) == ""
	})
}

// TestAgentKeepsLastExitAfterRestart: testbed 4.1 finding (a). The last exit
// is still readable once the instance restarted and runs again.
func TestAgentKeepsLastExitAfterRestart(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	crashRuntime.stableFromStartNumber = 2
	_, factStore, ctx := startCrashLoopTestAgent(t, "exit1", crashLoopTestBaseDelay, crashRuntime)
	waitFor(t, crashLoopTestTimeout, "instance running again after one exit, last_exit still \"exited with code 1\"", func() bool {
		return len(crashRuntime.startsOf("exit1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("exit1")) == string(types.InstanceRunning) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceLastExit("exit1")) == "exited with code 1"
	})
}
