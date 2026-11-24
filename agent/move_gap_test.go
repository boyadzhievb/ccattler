// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// moveGapTestPort is the port the web service exposes in the move-gap tests.
const moveGapTestPort = 8080

// moveGapCluster is a source agent (node-1) running web instances and a target
// agent (node-2) that publishes a moved one, on one store; the endpoint
// controller's view shows which instances are backends.
type moveGapCluster struct {
	factStore     *store.MemoryStore        // factStore is the shared store the checks read.
	sourceRuntime *runtime.SimulatorRuntime // sourceRuntime holds node-1's containers.
	sourceAgent   *Agent                    // sourceAgent is node-1's agent, which removes the moved container.
	targetAgent   *Agent                    // targetAgent is node-2's agent, which publishes the moved instance.
}

// newMoveGapCluster runs each instance on node-1 (container, running, ready)
// of service web (port 8080, readiness probe). wrapAgentStore, when not nil,
// wraps the store both agents use; each agent's writes are audited (3g.0).
func newMoveGapCluster(t *testing.T, wrapAgentStore func(store.StateStore) store.StateStore, instanceIDs ...string) *moveGapCluster {
	t.Helper()
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	var agentStore store.StateStore = memoryStore
	if wrapAgentStore != nil {
		agentStore = wrapAgentStore(memoryStore)
	}
	cluster := &moveGapCluster{factStore: memoryStore, sourceRuntime: runtime.NewSimulatorRuntime()}
	cluster.sourceAgent = New("node-1", newAuditedInstanceStore(t, agentStore, "node-1", injectedFault{}), cluster.sourceRuntime)
	cluster.sourceAgent.SetNetworkProvider(network.NewSimulatorNetworkProvider())
	cluster.targetAgent = New("node-2", newAuditedInstanceStore(t, agentStore, "node-2", injectedFault{}), runtime.NewSimulatorRuntime())
	cluster.targetAgent.SetNetworkProvider(network.NewSimulatorNetworkProviderWithCIDR(10, 101)) // another range: the agents' IPs differ

	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceExpose("web", moveGapTestPort), []byte(""))
	_, _ = memoryStore.Put(ctx, types.ScanDesiredServices+"web/probe/readiness/method", []byte("http"))
	for _, instanceID := range instanceIDs {
		types.WritePlacement(ctx, memoryStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})
		if startError := cluster.sourceRuntime.Start(ctx, runtime.Spec{ID: instanceID, ServiceName: "web", Image: "nginx:1.27"}); startError != nil {
			t.Fatal(startError)
		}
		cluster.sourceAgent.publishInstanceStateToStore(ctx, scannedInstanceInfo(t, ctx, cluster.sourceAgent, instanceID), types.InstanceRunning, "", nil)
		cluster.passReadiness(ctx, instanceID)
	}
	return cluster
}

// passReadiness records a passing readiness probe for the instance.
func (cluster *moveGapCluster) passReadiness(ctx context.Context, instanceID string) {
	_, _ = cluster.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness), []byte(types.ReadinessProbeReady))
}

// moveToTarget moves the instance's placement from node-1 to node-2 as the
// scheduler's rebalance move does (buildRebalanceMoveChanges).
func (cluster *moveGapCluster) moveToTarget(ctx context.Context, instanceID string) {
	_ = types.DeletePlacement(ctx, cluster.factStore, "node-1", instanceID)
	types.WritePlacement(ctx, cluster.factStore, types.Placement{InstanceID: instanceID, NodeID: "node-2"})
}

// moveAndRemoveOnSource moves the instance's placement to node-2 and runs
// node-1's cleanup of the container that is no longer desired there.
func (cluster *moveGapCluster) moveAndRemoveOnSource(ctx context.Context, instanceID string) {
	cluster.moveToTarget(ctx, instanceID)
	cluster.sourceAgent.cleanupUndesiredInstance(ctx, instanceID)
}

// scannedInstanceInfo returns the instance as nodeAgent's placement scan sees
// it (with the placement revision its publish compares).
func scannedInstanceInfo(t *testing.T, ctx context.Context, nodeAgent *Agent, instanceID string) placedInstanceInfo {
	t.Helper()
	placedInstances, scanError := nodeAgent.findInstancesPlacedOnThisNode(ctx)
	if scanError != nil {
		t.Fatal(scanError)
	}
	for _, instanceInfo := range placedInstances {
		if instanceInfo.id == instanceID {
			return instanceInfo
		}
	}
	t.Fatalf("%s is not placed on %s", instanceID, nodeAgent.nodeID)
	return placedInstanceInfo{}
}

// publishOnTarget publishes the instance running on node-2, as node-2's agent
// does after it started the container (placement moved to node-2 first).
func (cluster *moveGapCluster) publishOnTarget(t *testing.T, ctx context.Context, instanceID string) {
	t.Helper()
	cluster.targetAgent.publishInstanceStateToStore(ctx, scannedInstanceInfo(t, ctx, cluster.targetAgent, instanceID), types.InstanceRunning, "", nil)
}

// endpointInstanceIDs returns the sorted IDs of the web instances the endpoint
// controller makes backends from the current facts.
func (cluster *moveGapCluster) endpointInstanceIDs(t *testing.T, ctx context.Context) []string {
	t.Helper()
	facts, scanError := cluster.factStore.Scan(ctx, "")
	if scanError != nil {
		t.Fatal(scanError)
	}
	changes, reconcileError := controllers.NewEndpointController().Reconcile(ctx, facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	var instanceIDs []string
	for _, change := range changes {
		endpointPath, isWebEndpoint := strings.CutPrefix(change.Key, types.ScanEndpoints+"web/")
		if change.Type == store.OpPut && isWebEndpoint {
			instanceIDs = append(instanceIDs, strings.SplitN(endpointPath, "/", 2)[0])
		}
	}
	slices.Sort(instanceIDs)
	return instanceIDs
}

// checkMovedInstanceFacts checks the instance's observed state, node and
// readiness.
func (cluster *moveGapCluster) checkMovedInstanceFacts(t *testing.T, ctx context.Context, step string, instanceID string,
	wantState types.InstanceState, wantNode string, wantReadiness string) {
	t.Helper()
	gotState := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceState(instanceID))
	gotNode := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceNode(instanceID))
	gotReadiness := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceProbeState(instanceID, types.ProbeReadiness))
	if gotState != string(wantState) || gotNode != wantNode || gotReadiness != wantReadiness {
		t.Errorf("%s: %s state=%q node=%q readiness=%q, want state=%q node=%q readiness=%q",
			step, instanceID, gotState, gotNode, gotReadiness, wantState, wantNode, wantReadiness)
	}
}

// checkEndpointInstanceIDs checks which web instances are backends.
func (cluster *moveGapCluster) checkEndpointInstanceIDs(t *testing.T, ctx context.Context, step string, want ...string) {
	t.Helper()
	if got := cluster.endpointInstanceIDs(t, ctx); !slices.Equal(got, want) {
		t.Errorf("%s: endpoints for %v, want %v", step, got, want)
	}
}

// targetPublishingStore runs the target agent's state publish just before the
// source's first transaction that compares observed/instance/{id}/node: the
// target publishes between the source's reads and its write.
type targetPublishingStore struct {
	store.StateStore                             // StateStore serves every call.
	instanceID         string                    // instanceID is the moved instance.
	publishOnTarget    func(ctx context.Context) // publishOnTarget is the target's publish, run once.
	targetHasPublished atomic.Bool               // targetHasPublished keeps the publish to one run.
}

// Transaction runs the target's publish once before a transaction that
// compares the instance's node key, then the transaction itself.
func (racingStore *targetPublishingStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	comparesNode := slices.ContainsFunc(compares, func(compare store.Compare) bool {
		return compare.Key == types.KeyObservedInstanceNode(racingStore.instanceID)
	})
	if comparesNode && racingStore.targetHasPublished.CompareAndSwap(false, true) {
		racingStore.publishOnTarget(ctx)
	}
	return racingStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
}

// TestSourceAgentMarksMovedInstancePending: spec instance-rebalancing, "The
// source backend leaves the VIP, the target's publish wins". The source sets
// the moved instance pending without readiness, so its backend leaves; the
// target's publish, before, during or after the removal, always wins.
func TestSourceAgentMarksMovedInstancePending(t *testing.T) {
	ctx := context.Background()
	t.Run("removal, then target publish", func(t *testing.T) {
		cluster := newMoveGapCluster(t, nil, "web-1")
		cluster.checkEndpointInstanceIDs(t, ctx, "before the move", "web-1")
		cluster.moveAndRemoveOnSource(ctx, "web-1")
		cluster.checkMovedInstanceFacts(t, ctx, "after the source removal", "web-1", types.InstancePending, "node-1", "")
		cluster.checkEndpointInstanceIDs(t, ctx, "after the source removal")
		cluster.publishOnTarget(t, ctx, "web-1")
		cluster.passReadiness(ctx, "web-1")
		cluster.checkMovedInstanceFacts(t, ctx, "after the target publish", "web-1", types.InstanceRunning, "node-2", string(types.ReadinessProbeReady))
		cluster.checkEndpointInstanceIDs(t, ctx, "after the target publish", "web-1")
	})
	t.Run("target published first", func(t *testing.T) {
		cluster := newMoveGapCluster(t, nil, "web-1")
		cluster.moveToTarget(ctx, "web-1")
		cluster.publishOnTarget(t, ctx, "web-1")
		cluster.passReadiness(ctx, "web-1")
		cluster.sourceAgent.cleanupUndesiredInstance(ctx, "web-1")
		cluster.checkMovedInstanceFacts(t, ctx, "after the source removal", "web-1", types.InstanceRunning, "node-2", string(types.ReadinessProbeReady))
		cluster.checkEndpointInstanceIDs(t, ctx, "after the source removal", "web-1")
	})
	// Review round 2 R2-1: the source scanned before the move, is still in that cycle
	// (long pull or init) and publishes after the target did; its next cycle cleans up.
	t.Run("source behind: scans, move, target publishes, source publishes, source cleanup", func(t *testing.T) {
		cluster := newMoveGapCluster(t, nil, "web-1")
		sourceScannedInfo := scannedInstanceInfo(t, ctx, cluster.sourceAgent, "web-1")
		cluster.moveToTarget(ctx, "web-1")
		cluster.publishOnTarget(t, ctx, "web-1")
		cluster.passReadiness(ctx, "web-1")
		cluster.sourceAgent.publishInstanceStateToStore(ctx, sourceScannedInfo, types.InstanceRunning, "", nil)
		cluster.checkMovedInstanceFacts(t, ctx, "after the behind source's publish", "web-1", types.InstanceRunning, "node-2", string(types.ReadinessProbeReady))
		cluster.sourceAgent.cleanupUndesiredInstance(ctx, "web-1")
		cluster.checkMovedInstanceFacts(t, ctx, "after the source cleanup", "web-1", types.InstanceRunning, "node-2", string(types.ReadinessProbeReady))
		cluster.checkEndpointInstanceIDs(t, ctx, "after the source cleanup", "web-1")
	})
	t.Run("target publishes between the source's read and write", func(t *testing.T) {
		var cluster *moveGapCluster
		var racingStore *targetPublishingStore
		cluster = newMoveGapCluster(t, func(memoryStore store.StateStore) store.StateStore {
			racingStore = &targetPublishingStore{StateStore: memoryStore, instanceID: "web-1"}
			return racingStore
		}, "web-1")
		// Only the state transaction: the target is between its state write and the rest of its publish.
		// The target's info comes from its scan (the audit, 3g.0, found the unscanned info unguarded).
		racingStore.publishOnTarget = func(ctx context.Context) {
			cluster.targetAgent.publishInstanceStateUnlessStopped(ctx, scannedInstanceInfo(t, ctx, cluster.targetAgent, "web-1"), types.InstanceRunning, nil)
		}
		cluster.moveAndRemoveOnSource(ctx, "web-1")
		if !racingStore.targetHasPublished.Load() {
			t.Fatalf("the source never compared the node key; the race case proves nothing")
		}
		gotState := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceState("web-1"))
		gotNode := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceNode("web-1"))
		if gotState != string(types.InstanceRunning) || gotNode != "node-2" {
			t.Errorf("after the race: state=%q node=%q, want the target's running on node-2", gotState, gotNode)
		}
	})
}

// TestMoveGapMultiInstanceKeepsReadyBackend: spec instance-rebalancing, "The
// gap per service size". web-1 moves while web-2 stays: one backend serves
// during the gap, both once the target runs web-1 and its probe passes.
func TestMoveGapMultiInstanceKeepsReadyBackend(t *testing.T) {
	ctx := context.Background()
	cluster := newMoveGapCluster(t, nil, "web-1", "web-2")
	cluster.checkEndpointInstanceIDs(t, ctx, "before the move", "web-1", "web-2")
	cluster.moveAndRemoveOnSource(ctx, "web-1")
	cluster.checkEndpointInstanceIDs(t, ctx, "during the gap", "web-2")
	cluster.publishOnTarget(t, ctx, "web-1")
	cluster.checkEndpointInstanceIDs(t, ctx, "target running, probe not passed yet", "web-2")
	cluster.passReadiness(ctx, "web-1")
	cluster.checkEndpointInstanceIDs(t, ctx, "target ready", "web-1", "web-2")
}

// TestMoveGapSingleInstanceShowsNotReady: spec instance-rebalancing, "The gap
// per service size". The only instance moves: during the gap it is pending
// (not running) with no backend, and it is back once the target runs it and
// its probe passes; the removal alone never brings it back.
func TestMoveGapSingleInstanceShowsNotReady(t *testing.T) {
	ctx := context.Background()
	cluster := newMoveGapCluster(t, nil, "web-1")
	cluster.moveAndRemoveOnSource(ctx, "web-1")
	cluster.checkMovedInstanceFacts(t, ctx, "during the gap", "web-1", types.InstancePending, "node-1", "")
	cluster.checkEndpointInstanceIDs(t, ctx, "during the gap")
	cluster.publishOnTarget(t, ctx, "web-1")
	cluster.checkEndpointInstanceIDs(t, ctx, "target running, probe not passed yet")
	cluster.passReadiness(ctx, "web-1")
	cluster.checkEndpointInstanceIDs(t, ctx, "target ready", "web-1")
}

// TestTargetStartupFailureAfterMoveIsNotRunning: spec instance-rebalancing, "A
// failed target startup does not look healthy". web-1 was moved to node-2 and
// left pending by node-1; node-2 cannot pull or start it and publishes a
// non-running state as its own (failed is replaced by the failure controller).
func TestTargetStartupFailureAfterMoveIsNotRunning(t *testing.T) {
	testCases := []struct {
		name      string                          // name is the target's failure
		setUp     func(*runtime.SimulatorRuntime) // setUp makes node-2's runtime fail
		wantState types.InstanceState             // wantState is what node-2 publishes
	}{
		{name: "pull failure", wantState: types.InstanceImagePullBackoff, setUp: func(targetRuntime *runtime.SimulatorRuntime) {
			targetRuntime.PullFailures = map[string]string{"nginx:1.27": "registry unreachable"}
		}},
		{name: "start failure", wantState: types.InstanceFailed, setUp: func(targetRuntime *runtime.SimulatorRuntime) {
			targetRuntime.InjectStartFailure("web-1", "CNI setup: no IP addresses available")
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			memoryStore := store.NewMemoryStore()
			defer memoryStore.Close()
			_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
			types.WriteInstance(ctx, memoryStore, types.Instance{ID: "web-1", Service: "web", State: types.InstancePending, Node: "node-1"})
			types.WritePlacement(ctx, memoryStore, types.Placement{InstanceID: "web-1", NodeID: "node-2"})
			targetRuntime := runtime.NewSimulatorRuntime()
			testCase.setUp(targetRuntime)
			targetAgent := New("node-2", newAuditedInstanceStore(t, memoryStore, "node-2", injectedFault{}), targetRuntime)
			targetAgent.SetInterval(50 * time.Millisecond)
			go targetAgent.Run(ctx)

			waitFor(t, 2*time.Second, "node-2 publishes "+string(testCase.wantState), func() bool {
				return readFactValue(ctx, memoryStore, types.KeyObservedInstanceState("web-1")) == string(testCase.wantState) &&
					readFactValue(ctx, memoryStore, types.KeyObservedInstanceNode("web-1")) == "node-2"
			})
		})
	}
}

// moveAfterSourceStateStore runs a hook once, right after node-1's state
// transaction committed (the transaction that writes observed node = node-1):
// a move lands between the source's state write and the rest of its publish.
type moveAfterSourceStateStore struct {
	store.StateStore                           // StateStore serves every call.
	instanceID       string                    // instanceID is the instance being published.
	afterSourceState func(ctx context.Context) // afterSourceState moves the instance and publishes it on node-2.
	hookHasRun       atomic.Bool               // hookHasRun keeps the hook to one run.
}

// Transaction commits, then runs the hook once after node-1's state transaction.
func (racingStore *moveAfterSourceStateStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	swapped, transactionError := racingStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
	isSourceStateWrite := slices.ContainsFunc(onSuccess, func(operation store.Op) bool {
		return operation.Key == types.KeyObservedInstanceNode(racingStore.instanceID) && string(operation.Value) == "node-1"
	})
	if swapped && isSourceStateWrite && racingStore.afterSourceState != nil && racingStore.hookHasRun.CompareAndSwap(false, true) {
		racingStore.afterSourceState(ctx)
	}
	return swapped, transactionError
}

// TestSourcePublishNeverOverwritesTargetIP: review 3c.6. The source's publish
// commits its state, then the instance moves and node-2 publishes it with its
// own IP; whatever the source still writes must not replace node-2's IP or
// network allocation (the VIP would point at the removed source container).
func TestSourcePublishNeverOverwritesTargetIP(t *testing.T) {
	ctx := context.Background()
	var cluster *moveGapCluster
	var racingStore *moveAfterSourceStateStore
	cluster = newMoveGapCluster(t, func(memoryStore store.StateStore) store.StateStore {
		racingStore = &moveAfterSourceStateStore{StateStore: memoryStore, instanceID: "web-1"}
		return racingStore
	}, "web-1")
	sourceScannedInfo := scannedInstanceInfo(t, ctx, cluster.sourceAgent, "web-1")
	racingStore.afterSourceState = func(ctx context.Context) {
		cluster.moveToTarget(ctx, "web-1")
		cluster.publishOnTarget(t, ctx, "web-1")
	}
	cluster.sourceAgent.publishInstanceStateToStore(ctx, sourceScannedInfo, types.InstanceStarting, "", nil)
	if !racingStore.hookHasRun.Load() {
		t.Fatalf("the move was never injected after the source's state write; the case proves nothing")
	}
	targetIP, allocateError := cluster.targetAgent.networkProvider.AllocateIP(ctx, "node-2", "web-1")
	if allocateError != nil {
		t.Fatal(allocateError)
	}
	for _, instanceKey := range []string{types.KeyObservedInstanceIP("web-1"), types.KeyNetworkAllocation("web-1")} {
		if gotIP := readFactValue(ctx, cluster.factStore, instanceKey); gotIP != targetIP {
			t.Errorf("%s = %q after the source's late publish, want node-2's %q", instanceKey, gotIP, targetIP)
		}
	}
	gotState := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceState("web-1"))
	gotNode := readFactValue(ctx, cluster.factStore, types.KeyObservedInstanceNode("web-1"))
	if gotState != string(types.InstanceRunning) || gotNode != "node-2" {
		t.Errorf("state=%q node=%q, want node-2's running", gotState, gotNode)
	}
}

// runSourceProbePass scans web-1 on node-1 (readiness and liveness probes
// against a healthy server), optionally moves it to node-2, runs node-1's probe
// pass with that scan and returns the store.
func runSourceProbePass(t *testing.T, moveBeforeProbe bool) store.StateStore {
	t.Helper()
	factStore, _, sourceAgent := setupAuditedTestAgent(t, "node-1")
	ctx := context.Background()
	healthyListener, healthyPort := startTestHTTPServer(t, 200)
	t.Cleanup(func() { healthyListener.Close() })
	setupInstanceInStore(ctx, factStore, "web", "web-1", "node-1", "127.0.0.1")
	writeHTTPProbeConfig(ctx, factStore, "web", types.ProbeReadiness, healthyPort, "/", 0, 3, 1)
	writeHTTPProbeConfig(ctx, factStore, "web", types.ProbeLiveness, healthyPort, "/", 0, 3, 1)

	probePassInfo := scannedInstanceInfo(t, ctx, sourceAgent, "web-1") // the probe pass's scan
	if moveBeforeProbe {
		_ = types.DeletePlacement(ctx, factStore, "node-1", "web-1")
		types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "web-1", NodeID: "node-2"})
	}
	sourceAgent.probeScheduler.executeProbesForInstance(ctx, probePassInfo)
	return factStore
}

// TestSourceProbeAfterMoveDoesNotSetReadiness: review round 3 R3-S1 (positive
// control, round 4 Sg2). With its placement current, node-1's probe pass
// writes ready, healthy and health; after the move the same pass (scanned
// before it) writes none of node-2's instance's probe facts.
func TestSourceProbeAfterMoveDoesNotSetReadiness(t *testing.T) {
	ctx := context.Background()
	probedKeys := []string{
		types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness),
		types.KeyObservedInstanceProbeState("web-1", types.ProbeLiveness),
		types.KeyObservedInstanceHealth("web-1"),
	}
	controlStore := runSourceProbePass(t, false)
	for _, probedKey := range probedKeys {
		if readFactValue(ctx, controlStore, probedKey) == "" {
			t.Fatalf("control: node-1 did not write %s with its placement current; the guard case proves nothing", probedKey)
		}
	}
	movedStore := runSourceProbePass(t, true)
	for _, probedKey := range append(probedKeys, types.KeyObservedInstanceProbeProgress("web-1")) {
		if written := readFactValue(ctx, movedStore, probedKey); written != "" {
			t.Errorf("node-1 wrote %s = %q for web-1 after it moved to node-2", probedKey, written)
		}
	}
}

// TestStatusWrittenOnlyAfterCommittedPublish: review round 3 R3-S2. A source
// still in its cycle after the move fails its guarded state publish; the status
// reason, last exit and secret-wait status that follow it must not be written
// either (they would describe node-1's container on node-2's instance).
func TestStatusWrittenOnlyAfterCommittedPublish(t *testing.T) {
	testCases := []struct {
		name              string                                            // name is the publish path
		runPath           func(context.Context, *Agent, placedInstanceInfo) // runPath runs the path on node-1
		guardedStatusKeys []string                                          // guardedStatusKeys must stay unwritten
	}{
		{name: "start failure", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.handleInstanceStartError(ctx, instanceInfo, "nginx:1.27", "", errors.New("CNI setup failed"))
		}, guardedStatusKeys: []string{types.KeyObservedInstanceStatusReason("web-1")}},
		{name: "crash loop", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.beginCrashLoopWait(ctx, instanceInfo, sourceAgent.ensureCrashLoopBackoffEntry(instanceInfo.id), "exit(1)")
		}, guardedStatusKeys: []string{types.KeyObservedInstanceStatusReason("web-1"), types.KeyObservedInstanceLastExit("web-1")}},
		{name: "secret wait", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.recordSecretDeliveryWait(ctx, instanceInfo, serviceSecretRequirements{}, errors.New("secret API unreachable"))
		}, guardedStatusKeys: []string{types.KeyObservedInstanceStatusReason("web-1"), types.KeyObservedInstanceSecretWait("web-1")}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			factStore, _, sourceAgent := setupAuditedTestAgent(t, "node-1")
			ctx := context.Background()
			setupInstanceInStore(ctx, factStore, "web", "web-1", "node-1", "127.0.0.1")
			sourceScannedInfo := scannedInstanceInfo(t, ctx, sourceAgent, "web-1")
			_ = types.DeletePlacement(ctx, factStore, "node-1", "web-1")
			types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "web-1", NodeID: "node-2"})

			testCase.runPath(ctx, sourceAgent, sourceScannedInfo)
			for _, statusKey := range testCase.guardedStatusKeys {
				if written := readFactValue(ctx, factStore, statusKey); written != "" {
					t.Errorf("node-1 wrote %s = %q after its guarded publish failed (instance moved to node-2)", statusKey, written)
				}
			}
		})
	}
}

// targetOwnedFacts are facts node-2 published for web-1 after the move, which
// a behind node-1 must leave as they are (M267 3e).
var targetOwnedFacts = map[string]string{
	types.KeyObservedInstanceStatusReason("web-1"):                           "running",
	types.KeyObservedInstanceStartedAt("web-1"):                              "2026-11-23T12:00:00.5Z",
	types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness):       string(types.ReadinessProbeReady),
	types.KeyObservedInstanceConfigHash("web-1"):                             "target-config-hash",
	types.KeyObservedInstanceImage("web-1"):                                  "nginx:1.27",
	types.KeyObservedInstanceProbeProgress("web-1"):                          `{"readiness_failures":1,"readiness_threshold":3}`,
	types.KeyObservedInstanceExitReason("web-1"):                             "target exit",
	types.KeyObservedInstanceLastExit("web-1"):                               "target last exit",
	types.KeyObservedInstanceHostPortForPort("web-1", fakeContainerPortBase): "31000", // node-1's runtime maps fakeHostPortBase (3g.5)
}

// snapshotInstanceFacts returns every observed/instance/{id} fact (marker and
// tree), its telemetry (observed/telemetry/instance/{id}/) and
// network/allocation/{id}, key to value.
func snapshotInstanceFacts(t *testing.T, ctx context.Context, factStore store.StateStore, instanceID string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for _, instancePrefix := range []string{types.ScanObservedInstances + instanceID + "/", types.ScanObservedTelemetryInstances + instanceID + "/"} {
		instanceFacts, scanError := factStore.Scan(ctx, instancePrefix)
		if scanError != nil {
			t.Fatal(scanError)
		}
		for _, fact := range instanceFacts {
			snapshot[fact.Key] = string(fact.Value)
		}
	}
	for _, singleKey := range []string{types.KeyObservedInstance(instanceID), types.KeyNetworkAllocation(instanceID)} {
		if fact, getError := factStore.Get(ctx, singleKey); getError == nil {
			snapshot[singleKey] = string(fact.Value)
		}
	}
	return snapshot
}

// TestSourceWritesNothingAfterMove: review rounds 4 to 6 (B1-B4, Sg1, T2,
// R6-5). node-1, whose runtime maps host ports, scanned web-1 before the move
// and is still in that cycle; each of its paths below leaves every
// observed/instance/web-1 fact and the network allocation as node-2 left them.
func TestSourceWritesNothingAfterMove(t *testing.T) {
	testCases := []struct {
		name    string                                            // name is the finding and path
		runPath func(context.Context, *Agent, placedInstanceInfo) // runPath runs node-1's late path
	}{
		{name: "B1 pull failure", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.recordImagePullFailure(ctx, instanceInfo, "nginx:1.27", "registry unreachable")
		}},
		{name: "B2 start path", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.startInstanceAndPublishState(ctx, instanceInfo, "nginx:1.26", "source-config-hash",
				runtime.Spec{ID: instanceInfo.id, ServiceName: instanceInfo.service, Image: "nginx:1.26"})
		}},
		{name: "B2 reconcile desired instance", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.reconcileDesiredInstance(ctx, instanceInfo)
		}},
		{name: "B3 exit reason", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.recordInstanceExit(ctx, instanceInfo, sourceAgent.ensureCrashLoopBackoffEntry(instanceInfo.id),
				runtime.Status{ID: instanceInfo.id, ExitCode: 1, Error: "source exit"})
			sourceAgent.recordInstanceStartFailure(ctx, instanceInfo, "source start failure")
		}},
		{name: "B4 probe progress delete", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.probeScheduler.publishProbeProgress(ctx, instanceInfo, &probeConfig{failureThreshold: 3}, nil, nil,
				&instanceProbeState{startup: &probeTracker{consecutiveSuccesses: 1}})
		}},
		{name: "secret wait", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.recordSecretDeliveryWait(ctx, instanceInfo, serviceSecretRequirements{}, errors.New("secret API unreachable"))
		}},
		{name: "restart count", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.incrementRestartCountInStore(ctx, instanceInfo)
		}},
		{name: "init step and progress", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.executeInitStep(ctx, instanceInfo, initStepDefinition{index: 0, exec: "migrate"}, "nginx:1.27", 1)
			sourceAgent.clearInitProgress(ctx, instanceInfo)
		}},
		{name: "telemetry", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.nodeReporter.reportWorkloadTelemetry(ctx, []runtime.Status{{ID: instanceInfo.id, Running: true}})
		}},
		{name: "running path", runPath: func(ctx context.Context, sourceAgent *Agent, instanceInfo placedInstanceInfo) {
			sourceAgent.publishRunningInstance(ctx, instanceInfo, "10.100.1.9")
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			factStore := store.NewMemoryStore()
			defer factStore.Close()
			// Host ports, so the start and running paths have ports to write (3g.5 R6-5).
			sourceRuntime := &manyHostPortsRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), portCount: 2}
			sourceAgent := New("node-1", newAuditedInstanceStore(t, factStore, "node-1", injectedFault{}), sourceRuntime)
			ctx := context.Background()
			setupInstanceInStore(ctx, factStore, "web", "web-1", "node-1", "127.0.0.1")
			sourceScannedInfo := scannedInstanceInfo(t, ctx, sourceAgent, "web-1")
			_ = types.DeletePlacement(ctx, factStore, "node-1", "web-1")
			types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "web-1", NodeID: "node-2"})
			for key, value := range targetOwnedFacts {
				_, _ = factStore.Put(ctx, key, []byte(value))
			}

			if startError := sourceRuntime.Start(ctx, runtime.Spec{ID: "web-1", ServiceName: "web", Image: "nginx:1.27"}); startError != nil {
				t.Fatal(startError)
			}
			factsBefore := snapshotInstanceFacts(t, ctx, factStore, "web-1")
			testCase.runPath(ctx, sourceAgent, sourceScannedInfo)
			factsAfter := snapshotInstanceFacts(t, ctx, factStore, "web-1")
			for key, valueBefore := range factsBefore {
				if valueAfter, stillThere := factsAfter[key]; !stillThere || valueAfter != valueBefore {
					t.Errorf("node-1 changed %s from %q to %q (present %v) after web-1 moved to node-2", key, valueBefore, valueAfter, stillThere)
				}
			}
			for key, valueAfter := range factsAfter {
				if _, existedBefore := factsBefore[key]; !existedBefore {
					t.Errorf("node-1 wrote %s = %q after web-1 moved to node-2", key, valueAfter)
				}
			}
		})
	}
}
