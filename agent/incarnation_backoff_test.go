// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// incarnationTestNewImage is the image the re-created instance is recorded with.
const incarnationTestNewImage = "nginx:1.28"

// recreateInstanceOnNewImage writes what the instance controller's re-creation
// group writes for a stateful ordinal (buildInstanceIncarnationChanges): the
// new recorded image, pending, and no facts of the earlier incarnation.
func recreateInstanceOnNewImage(ctx context.Context, factStore store.StateStore, instanceID string) {
	factStore.Put(ctx, types.KeyDerivedInstanceImage(instanceID), []byte(incarnationTestNewImage))
	for _, staleKey := range []string{
		types.KeyObservedInstanceRestarts(instanceID), types.KeyObservedInstanceStartedAt(instanceID),
		types.KeyObservedInstanceLastExit(instanceID), types.KeyObservedInstanceCreatedAt(instanceID),
	} {
		factStore.Delete(ctx, staleKey)
	}
	factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstancePending)))
}

// waitForStartOnNewImageWithoutRestart waits until the instance runs the new
// recorded image, failing at once if an exit or restart of the old image was
// attributed to the new incarnation.
func waitForStartOnNewImageWithoutRestart(t *testing.T, ctx context.Context, factStore store.StateStore, instanceID string) {
	t.Helper()
	waitFor(t, crashLoopTestTimeout, instanceID+" started at once on "+incarnationTestNewImage+", nothing of the old image counted", func() bool {
		running := readFactValue(ctx, factStore, types.KeyObservedInstanceState(instanceID)) == string(types.InstanceRunning) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceImage(instanceID)) == incarnationTestNewImage
		if !running {
			return false
		}
		if restarts := readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts(instanceID)); restarts != "" {
			t.Fatalf("restarts = %q on the new incarnation, want none (the exit was the old image's)", restarts)
		}
		if lastExit := readFactValue(ctx, factStore, types.KeyObservedInstanceLastExit(instanceID)); lastExit != "" {
			t.Fatalf("last_exit = %q on the new incarnation, want none", lastExit)
		}
		return true
	})
}

// TestAgentStartsNewIncarnationWithoutOldBackoff: testbed rerun after 2e (2f.1,
// 2f.2). A stateful rollback re-creates db-0 on another image while its old
// container has exited or failed to start. The agent starts the recorded image
// at once instead of waiting out the old image's back-off, and counts neither
// the exit nor the start as a restart of the new incarnation.
func TestAgentStartsNewIncarnationWithoutOldBackoff(t *testing.T) {
	t.Run("exit recorded before the re-creation", func(t *testing.T) {
		crashRuntime := newCrashingRuntime()
		crashRuntime.stableFromStartNumber = 2
		factStore := newStoreWithRecordedImage(t, "db-0", pinnedTestImage)
		nodeAgent, _, ctx := startCrashLoopTestAgentOnStore(t, factStore, "db-0", crashLoopLongBaseDelay, crashRuntime)
		waitFor(t, crashLoopTestTimeout, "db-0 waiting in crash-loop-backoff", func() bool {
			return readFactValue(ctx, factStore, types.KeyObservedInstanceState("db-0")) == string(types.InstanceCrashLoopBackoff)
		})
		nodeAgent.RunInAgentLoop(func() { recreateInstanceOnNewImage(ctx, factStore, "db-0") })
		waitForStartOnNewImageWithoutRestart(t, ctx, factStore, "db-0")
	})
	t.Run("exit right after the re-creation", func(t *testing.T) {
		crashRuntime := newCrashingRuntime()
		crashRuntime.stableFromStartNumber = 1
		factStore := newStoreWithRecordedImage(t, "db-0", pinnedTestImage)
		nodeAgent, _, ctx := startCrashLoopTestAgentOnStore(t, factStore, "db-0", crashLoopLongBaseDelay, crashRuntime)
		waitFor(t, crashLoopTestTimeout, "db-0 running on "+pinnedTestImage, func() bool {
			return readFactValue(ctx, factStore, types.KeyObservedInstanceState("db-0")) == string(types.InstanceRunning)
		})
		nodeAgent.RunInAgentLoop(func() {
			recreateInstanceOnNewImage(ctx, factStore, "db-0")
			crashRuntime.KillWorkload("db-0", 1, "")
		})
		waitForStartOnNewImageWithoutRestart(t, ctx, factStore, "db-0")
	})
	t.Run("start failure of the old image", func(t *testing.T) {
		crashRuntime := newCrashingRuntime()
		crashRuntime.failStartsWith(newProcessStartError())
		factStore := newStoreWithRecordedImage(t, "db-0", pinnedTestImage)
		nodeAgent, _, ctx := startCrashLoopTestAgentOnStore(t, factStore, "db-0", crashLoopLongBaseDelay, crashRuntime)
		waitFor(t, crashLoopTestTimeout, "db-0 start failure in crash-loop-backoff", func() bool {
			return readFactValue(ctx, factStore, types.KeyObservedInstanceState("db-0")) == string(types.InstanceCrashLoopBackoff)
		})
		nodeAgent.RunInAgentLoop(func() {
			crashRuntime.failStartsWith(nil)
			crashRuntime.mutex.Lock()
			crashRuntime.stableFromStartNumber = 1
			crashRuntime.mutex.Unlock()
			recreateInstanceOnNewImage(ctx, factStore, "db-0")
		})
		waitForStartOnNewImageWithoutRestart(t, ctx, factStore, "db-0")
	})
}

// TestAgentLogsNewIncarnationOnce: review round 4 (M241 2g.4). While a new
// incarnation cannot start yet, every cycle sees the image mismatch again; it
// is logged the first time only, and again for a later incarnation.
func TestAgentLogsNewIncarnationOnce(t *testing.T) {
	nodeAgent := New("node-1", store.NewMemoryStore(), newCrashingRuntime())
	wantFirstSights := []struct {
		recordedImage string // recordedImage is the incarnation's recorded image.
		wantFirst     bool   // wantFirst says whether it is logged.
	}{
		{recordedImage: "nginx:1.28", wantFirst: true},
		{recordedImage: "nginx:1.28", wantFirst: false},
		{recordedImage: "nginx:1.28", wantFirst: false},
		{recordedImage: "nginx:1.29", wantFirst: true},
	}
	for cycleIndex, wantSight := range wantFirstSights {
		if firstSight := nodeAgent.isFirstSightOfIncarnation("db-0", wantSight.recordedImage); firstSight != wantSight.wantFirst {
			t.Errorf("cycle %d (%s): first sight = %v, want %v", cycleIndex, wantSight.recordedImage, firstSight, wantSight.wantFirst)
		}
	}
}
