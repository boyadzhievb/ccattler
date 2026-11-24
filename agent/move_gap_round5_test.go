// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// manyHostPortsTestPortCount is more host ports than one store transaction
// holds together with the state publish (review round 5 B1).
const manyHostPortsTestPortCount = 130

// manyHostPortsRuntime is a simulator runtime that maps every container port
// 9000..9000+count-1 to a host port.
type manyHostPortsRuntime struct {
	*runtime.SimulatorRuntime     // SimulatorRuntime runs the containers.
	portCount                 int // portCount is how many ports every instance maps.
}

// HostPortsForInstance maps portCount container ports to host ports.
func (portRuntime *manyHostPortsRuntime) HostPortsForInstance(_ string) map[int]int {
	portMappings := make(map[int]int, portRuntime.portCount)
	for portIndex := 0; portIndex < portRuntime.portCount; portIndex++ {
		portMappings[9000+portIndex] = 30000 + portIndex
	}
	return portMappings
}

// TestStartPublishesInstanceWithManyHostPorts: review round 5 B1. With 130
// host ports the start still publishes the instance running and stores every
// port; the ports never share the state's transaction limit.
func TestStartPublishesInstanceWithManyHostPorts(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	portRuntime := &manyHostPortsRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), portCount: manyHostPortsTestPortCount}
	nodeAgent := New("node-1", factStore, portRuntime)
	setupInstanceInStore(ctx, factStore, "web", "web-1", "node-1", "127.0.0.1")
	instanceInfo := scannedInstanceInfo(t, ctx, nodeAgent, "web-1")

	nodeAgent.startInstanceAndPublishState(ctx, instanceInfo, "nginx:1.27", "config-hash",
		runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"})

	if gotState := readFactValue(ctx, factStore, types.KeyObservedInstanceState("web-1")); gotState != string(types.InstanceRunning) {
		t.Errorf("state = %q with %d host ports, want running", gotState, manyHostPortsTestPortCount)
	}
	for portIndex := 0; portIndex < manyHostPortsTestPortCount; portIndex++ {
		if readFactValue(ctx, factStore, types.KeyObservedInstanceHostPortForPort("web-1", 9000+portIndex)) == "" {
			t.Fatalf("host port of container port %d not stored (of %d)", 9000+portIndex, manyHostPortsTestPortCount)
		}
	}
}

// failingStartPublishStore fails the first transaction that writes
// observed/instance/{id}/started_at, as a store error on the start publish.
type failingStartPublishStore struct {
	store.StateStore             // StateStore serves every other call.
	hasFailed        atomic.Bool // hasFailed keeps the failure to one transaction.
}

// Transaction fails once for the start publish, else delegates.
func (failingStore *failingStartPublishStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	writesStartedAt := slices.ContainsFunc(onSuccess, func(operation store.Op) bool {
		return operation.Key == types.KeyObservedInstanceStartedAt("web-1")
	})
	if writesStartedAt && failingStore.hasFailed.CompareAndSwap(false, true) {
		return false, errors.New("etcd: request timed out")
	}
	return failingStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
}

// TestStartFactsCarriedAfterStoreError: review round 5 B2. The start publish
// fails on a store error; the next cycle's running publish still stores the
// start's config hash, image and started_at and clears the old readiness.
func TestStartFactsCarriedAfterStoreError(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	failingStore := &failingStartPublishStore{StateStore: memoryStore}
	nodeAgent := New("node-1", failingStore, runtime.NewSimulatorRuntime())
	setupInstanceInStore(ctx, memoryStore, "web", "web-1", "node-1", "127.0.0.1")
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness), []byte(types.ReadinessProbeReady))
	instanceInfo := scannedInstanceInfo(t, ctx, nodeAgent, "web-1")

	nodeAgent.startInstanceAndPublishState(ctx, instanceInfo, "nginx:1.27", "config-hash-1",
		runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"})
	if !failingStore.hasFailed.Load() {
		t.Fatalf("the start publish was never failed; the case proves nothing")
	}
	nodeAgent.publishRunningInstance(ctx, instanceInfo, "")

	for key, wantValue := range map[string]string{
		types.KeyObservedInstanceConfigHash("web-1"):                       "config-hash-1",
		types.KeyObservedInstanceImage("web-1"):                            "nginx:1.27",
		types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness): "",
	} {
		if gotValue := readFactValue(ctx, memoryStore, key); gotValue != wantValue {
			t.Errorf("%s = %q after the next cycle, want %q", key, gotValue, wantValue)
		}
	}
	if readFactValue(ctx, memoryStore, types.KeyObservedInstanceStartedAt("web-1")) == "" {
		t.Errorf("started_at not stored after the next cycle")
	}
}

// placementMovingRuntime moves web-1's placement to node-2 while it stops the
// container (a move during pre_stop and the grace period, review round 5 P2).
type placementMovingRuntime struct {
	*runtime.SimulatorRuntime                  // SimulatorRuntime runs the containers.
	factStore                 store.StateStore // factStore holds the placement the stop moves.
}

// StopWithGracePeriod moves the placement, then stops the container.
func (movingRuntime *placementMovingRuntime) StopWithGracePeriod(ctx context.Context, instanceID string, gracePeriod time.Duration) error {
	_ = types.DeletePlacement(ctx, movingRuntime.factStore, "node-1", instanceID)
	types.WritePlacement(ctx, movingRuntime.factStore, types.Placement{InstanceID: instanceID, NodeID: "node-2"})
	return movingRuntime.SimulatorRuntime.StopWithGracePeriod(ctx, instanceID, gracePeriod)
}

// Stop moves the placement, then stops the container.
func (movingRuntime *placementMovingRuntime) Stop(ctx context.Context, instanceID string) error {
	_ = types.DeletePlacement(ctx, movingRuntime.factStore, "node-1", instanceID)
	types.WritePlacement(ctx, movingRuntime.factStore, types.Placement{InstanceID: instanceID, NodeID: "node-2"})
	return movingRuntime.SimulatorRuntime.Stop(ctx, instanceID)
}

// TestStoppedNotWrittenWhenPlacementChangesDuringStop: review round 5 P2.
// node-1 removes web-1 (evicted while placed on node-1: not a move); during the
// stop the instance is placed on node-2. stopped is final, so node-1 must not
// write it over an instance that now runs elsewhere.
func TestStoppedNotWrittenWhenPlacementChangesDuringStop(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	movingRuntime := &placementMovingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), factStore: memoryStore}
	nodeAgent := New("node-1", memoryStore, movingRuntime)
	setupInstanceInStore(ctx, memoryStore, "web", "web-1", "node-1", "127.0.0.1")
	if startError := movingRuntime.Start(ctx, runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"}); startError != nil {
		t.Fatal(startError)
	}

	nodeAgent.cleanupUndesiredInstance(ctx, "web-1")
	if gotState := readFactValue(ctx, memoryStore, types.KeyObservedInstanceState("web-1")); gotState == string(types.InstanceStopped) {
		t.Errorf("node-1 wrote stopped for web-1, which was placed on node-2 during its stop")
	}
}
