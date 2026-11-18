// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// readStartedAt returns the instance's observed started_at, or the zero time
// when it is missing or not RFC 3339.
func readStartedAt(ctx context.Context, factStore store.StateStore, instanceID string) time.Time {
	startedAt, parseError := time.Parse(time.RFC3339, readFactValue(ctx, factStore, types.KeyObservedInstanceStartedAt(instanceID)))
	if parseError != nil {
		return time.Time{}
	}
	return startedAt
}

// TestAgentRecordsStartedAtOnEveryStart: spec service-rollout, "Agent records
// every start" (M241 D10). The first start crashes and waits an hour; a config
// change restarts the instance in place, and that start sets a later started_at.
func TestAgentRecordsStartedAtOnEveryStart(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	crashRuntime.stableFromStartNumber = 2
	_, factStore, ctx := startCrashLoopTestAgent(t, "start1", crashLoopLongBaseDelay, crashRuntime)

	var firstStartedAt time.Time
	waitFor(t, crashLoopTestTimeout, "first start recorded, instance waiting in crash-loop-backoff", func() bool {
		firstStartedAt = readStartedAt(ctx, factStore, "start1")
		return !firstStartedAt.IsZero() && len(crashRuntime.startsOf("start1")) == 1 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("start1")) == string(types.InstanceCrashLoopBackoff)
	})
	if firstStart := crashRuntime.startsOf("start1")[0]; firstStartedAt.Before(firstStart.Truncate(time.Second)) {
		t.Errorf("first started_at %v is before the first start %v", firstStartedAt, firstStart)
	}

	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.new.local"))
	waitFor(t, crashLoopTestTimeout, "in-place restart running with a later started_at", func() bool {
		return len(crashRuntime.startsOf("start1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("start1")) == string(types.InstanceRunning) &&
			readStartedAt(ctx, factStore, "start1").After(firstStartedAt)
	})
}
