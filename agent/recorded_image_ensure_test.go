// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestAgentReplacesContainerOnAnotherImageThanRecorded: testbed T1 (2e.1 a).
// A stateful ordinal is recreated under the same ID with a new recorded image
// while its old container still runs, and the agent never sees a stop. The
// agent ensures the container runs the recorded image: it replaces the
// container, records a new start, and does not count the replacement as a
// restart.
func TestAgentReplacesContainerOnAnotherImageThanRecorded(t *testing.T) {
	factStore := newStoreWithRecordedImage(t, "db-0", pinnedTestImage)
	ctx := startImageTestAgent(t, factStore, "db-0")
	waitFor(t, crashLoopTestTimeout, "db-0 running on "+pinnedTestImage, func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("db-0")) == string(types.InstanceRunning) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceImage("db-0")) == pinnedTestImage
	})

	// The instance controller's recreation group (buildInstanceIncarnationChanges).
	const recreatedImage = "nginx:1.28"
	factStore.Put(ctx, types.KeyDerivedInstanceImage("db-0"), []byte(recreatedImage))
	factStore.Delete(ctx, types.KeyObservedInstanceStartedAt("db-0"))
	factStore.Put(ctx, types.KeyObservedInstanceState("db-0"), []byte(string(types.InstancePending)))

	waitFor(t, crashLoopTestTimeout, "db-0 restarted on "+recreatedImage+" with a new started_at and no restart counted", func() bool {
		if restarts := readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("db-0")); restarts != "" {
			t.Fatalf("restarts = %q after replacing the container, want none", restarts)
		}
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("db-0")) == string(types.InstanceRunning) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceImage("db-0")) == recreatedImage &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceStartedAt("db-0")) != ""
	})
}
