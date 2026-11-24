// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"strconv"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestInstanceMovedDuringStopIsMarkedPending: review round 6 R6-3. node-1
// removes web-1, which is placed on node-2 during the stop: stopped is not
// written (3f P2) and the stopped container must not stay running and ready,
// so node-1 marks it pending without readiness until node-2 publishes it.
func TestInstanceMovedDuringStopIsMarkedPending(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	movingRuntime := &placementMovingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), factStore: memoryStore}
	nodeAgent := New("node-1", newAuditedInstanceStore(t, memoryStore, "node-1", injectedFault{}), movingRuntime)
	setupInstanceInStore(ctx, memoryStore, "web", "web-1", "node-1", "127.0.0.1")
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(types.InstanceRunning))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceNode("web-1"), []byte("node-1"))
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness), []byte(types.ReadinessProbeReady))
	if startError := movingRuntime.Start(ctx, runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"}); startError != nil {
		t.Fatal(startError)
	}

	nodeAgent.cleanupUndesiredInstance(ctx, "web-1")
	gotState := readFactValue(ctx, memoryStore, types.KeyObservedInstanceState("web-1"))
	gotReadiness := readFactValue(ctx, memoryStore, types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness))
	if gotState != string(types.InstancePending) || gotReadiness != "" {
		t.Errorf("after the removal of web-1, moved to node-2 during the stop: state=%q readiness=%q, want pending without readiness",
			gotState, gotReadiness)
	}
}

// TestUncommittedStartFactsDroppedWhenInstanceLeaves: review round 6 R6-4,
// R6-5. The start publish fails, so web-1's start facts are kept; then web-1
// leaves node-1. With its container still there the teardown drops them; with
// the container gone (removed outside the agent) no teardown runs, so the
// cycle's sweep must.
func TestUncommittedStartFactsDroppedWhenInstanceLeaves(t *testing.T) {
	for _, containerGone := range []bool{false, true} {
		t.Run("container gone "+strconv.FormatBool(containerGone), func(t *testing.T) {
			ctx := context.Background()
			memoryStore, simulatorRuntime := store.NewMemoryStore(), runtime.NewSimulatorRuntime()
			t.Cleanup(func() { memoryStore.Close() })
			failingStore := &failingStartPublishStore{StateStore: memoryStore}
			nodeAgent := New("node-1", newAuditedInstanceStore(t, failingStore, "node-1", injectedFault{}), simulatorRuntime)
			setupInstanceInStore(ctx, memoryStore, "web", "web-1", "node-1", "127.0.0.1")
			instanceInfo := scannedInstanceInfo(t, ctx, nodeAgent, "web-1")
			nodeAgent.startInstanceAndPublishState(ctx, instanceInfo, "nginx:1.27", "config-hash",
				runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"})
			if _, kept := nodeAgent.uncommittedStartFacts["web-1"]; !kept {
				t.Fatalf("the failed start publish kept no start facts; the case proves nothing")
			}

			if containerGone {
				_ = simulatorRuntime.Stop(ctx, "web-1")
			}
			_ = types.DeletePlacement(ctx, memoryStore, "node-1", "web-1")
			if cycleError := nodeAgent.executeReconciliationCycle(ctx); cycleError != nil {
				t.Fatal(cycleError)
			}
			if _, kept := nodeAgent.uncommittedStartFacts["web-1"]; kept {
				t.Errorf("uncommittedStartFacts still holds web-1 after it left node-1")
			}
		})
	}
}
