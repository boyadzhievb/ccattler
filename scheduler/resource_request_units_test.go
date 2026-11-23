// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// schedulerRunnerTestTimeout bounds how long a runner-driven test waits for
// the scheduler's facts.
const schedulerRunnerTestTimeout = 5 * time.Second

// schedulerRunnerPollInterval is how often a runner-driven test checks the store.
const schedulerRunnerPollInterval = 10 * time.Millisecond

// putValueOf returns the value of the put change for key, and whether one exists.
func putValueOf(changes []controllers.Change, key string) (string, bool) {
	for _, change := range changes {
		if change.Key == key && change.Type == store.OpPut {
			return string(change.Value), true
		}
	}
	return "", false
}

// TestSchedulerLeavesUnitRequestLargerThanNodesUnplaced: spec
// resource-requests, "Request larger than every node".
func TestSchedulerLeavesUnitRequestLargerThanNodesUnplaced(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("big-1"), "big"),
		kv(types.KeyObservedInstanceState("big-1"), "pending"),
		kv(types.KeyDesiredServiceResourcesMemory("big"), "64Gi"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "3072"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "2048"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if placements := filterPlacementChanges(changes); len(placements) != 0 {
		t.Errorf("a 64Gi request was placed on a node with at most 3072 MiB: %v", placements)
	}
	if reason, _ := putValueOf(changes, types.KeyDerivedSchedulerUnplacedReason("big-1")); reason != string(types.UnplacedInsufficientCapacity) {
		t.Errorf("unplaced reason = %q, want %s", reason, types.UnplacedInsufficientCapacity)
	}
	requirements, _ := putValueOf(changes, types.KeyDerivedSchedulerUnplacedRequirements("big-1"))
	if !strings.Contains(requirements, `"memory":65536`) {
		t.Errorf("unplaced requirements = %s, want memory 65536 (MiB)", requirements)
	}
}

// TestSchedulerPacksUnitRequestsWithinNodeCapacity: spec resource-requests,
// "Requests fill a node".
func TestSchedulerPacksUnitRequestsWithinNodeCapacity(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "pending"),
		kv(types.KeyObservedInstanceService("web-2"), "web"),
		kv(types.KeyObservedInstanceState("web-2"), "pending"),
		kv(types.KeyObservedInstanceService("web-3"), "web"),
		kv(types.KeyObservedInstanceState("web-3"), "pending"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "750m"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "2000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	placedInstances := 0
	for _, instanceID := range []string{"web-1", "web-2", "web-3"} {
		if _, placed := putValueOf(changes, types.KeyPlacementInstance(instanceID)); placed {
			placedInstances++
		}
	}
	if placedInstances != 2 {
		t.Errorf("placed %d instances of 750m on a 2000m node, want 2", placedInstances)
	}
	if reason, _ := putValueOf(changes, types.KeyDerivedSchedulerUnplacedReason("web-3")); reason != string(types.UnplacedInsufficientCapacity) {
		t.Errorf("web-3 unplaced reason = %q, want %s", reason, types.UnplacedInsufficientCapacity)
	}
}

// waitForStoreCondition polls the store until condition holds or the
// deadline passes, then fails the test with description.
func waitForStoreCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(schedulerRunnerTestTimeout)
	defer deadline.Stop()
	pollTicker := time.NewTicker(schedulerRunnerPollInterval)
	defer pollTicker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for: %s", description)
		case <-pollTicker.C:
		}
	}
}

// TestSchedulerWatchesEffectiveResourceRequest: spec resource-requests,
// "Effective request from the vertical autoscaler". The scheduler runs under
// the controller runner, so it sees only the prefixes it watches.
func TestSchedulerWatchesEffectiveResourceRequest(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 1000, CapacityMemory: 8192, AvailableCPU: 1000, AvailableMemory: 8192,
	})
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("web"), []byte("250m"))

	controllerRunner := controllers.NewRunner(factStore, newTestScheduler())
	controllerRunner.SetDebounce(schedulerRunnerPollInterval)
	controllerRunner.SetResyncInterval(0)
	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		_ = controllerRunner.Run(ctx)
	}()
	defer func() {
		cancel()
		<-runnerDone
	}()

	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("web"), []byte("1500m"))
	factStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("web"))
	factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte("pending"))

	waitForStoreCondition(t, "the scheduler decides on web-1", func() bool {
		_, placementError := factStore.Get(ctx, types.KeyPlacementInstance("web-1"))
		_, reasonError := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedReason("web-1"))
		return placementError == nil || reasonError == nil
	})
	if placementFact, placementError := factStore.Get(ctx, types.KeyPlacementInstance("web-1")); placementError == nil {
		t.Fatalf("web-1 placed on %s with effective cpu 1500m on a 1000m node", placementFact.Value)
	}
	reasonFact, _ := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedReason("web-1"))
	if string(reasonFact.Value) != string(types.UnplacedInsufficientCapacity) {
		t.Errorf("unplaced reason = %q, want %s", reasonFact.Value, types.UnplacedInsufficientCapacity)
	}

	// The instance exists now; lowering only the effective value must bring the
	// scheduler back (resync is off, so only the watch can) and place web-1.
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("web"), []byte("500m"))
	waitForStoreCondition(t, "web-1 placed after the effective cpu dropped to 500m", func() bool {
		placementFact, placementError := factStore.Get(ctx, types.KeyPlacementInstance("web-1"))
		return placementError == nil && string(placementFact.Value) == "node-1"
	})
}

// TestSchedulerRefusesUnparseableStoredRequest: spec resource-requests,
// "Legacy bad value". web's stored memory cannot be read: its pending
// instance stays unplaced with invalid_resource_request, it preempts nobody,
// its running instance is neither moved nor stopped, and the derived fact
// names web and the value for cca status.
func TestSchedulerRefusesUnparseableStoredRequest(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyDesiredServiceResourcesMemory("web"), "512MB"),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "500m"),
		kv(types.KeyDesiredServicePriority("web"), "500"),
		kv(types.KeyDesiredServicePlacementRebalance("web"), "true"),
		kv(types.KeyDesiredServiceResourcesCPU("batch"), "1000m"),
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "running"),
		kv(types.KeyPlacementInstance("web-1"), "node-1"),
		kv(types.KeyObservedInstanceService("web-2"), "web"),
		kv(types.KeyObservedInstanceState("web-2"), "pending"),
		kv(types.KeyObservedInstanceService("batch-1"), "batch"),
		kv(types.KeyObservedInstanceState("batch-1"), "running"),
		kv(types.KeyPlacementInstance("batch-1"), "node-1"),
		kv(types.KeyObservedInstanceService("batch-2"), "batch"),
		kv(types.KeyObservedInstanceState("batch-2"), "running"),
		kv(types.KeyPlacementInstance("batch-2"), "node-1"),
		kv(types.KeyObservedInstanceService("batch-3"), "batch"),
		kv(types.KeyObservedInstanceState("batch-3"), "running"),
		kv(types.KeyPlacementInstance("batch-3"), "node-1"),
		kv(types.KeyObservedInstanceService("batch-4"), "batch"),
		kv(types.KeyObservedInstanceState("batch-4"), "running"),
		kv(types.KeyPlacementInstance("batch-4"), "node-2"),
		// Every node is full for cpu 500m, so only preemption could place web-2,
		// and node-3 is empty, so only the pin keeps web-1 from moving there.
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "3000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
		kv(types.KeyObservedNodeState("node-3"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-3"), "0"),
		kv(types.KeyObservedNodeAvailableMemory("node-3"), "8192"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Key, types.ScanPlacements) {
			t.Errorf("placement change %v %s=%q for a service with an unreadable request", change.Type, change.Key, change.Value)
		}
		if strings.HasSuffix(change.Key, "/controller_stopped") {
			t.Errorf("preempted %s for a service with an unreadable request", change.Key)
		}
	}
	if reason, _ := putValueOf(changes, types.KeyDerivedSchedulerUnplacedReason("web-2")); reason != string(types.UnplacedInvalidResourceRequest) {
		t.Errorf("web-2 unplaced reason = %q, want %s", reason, types.UnplacedInvalidResourceRequest)
	}
	invalidRequest, _ := putValueOf(changes, types.KeyDerivedSchedulerInvalidResourceRequest("web"))
	if !strings.Contains(invalidRequest, types.KeyDesiredServiceResourcesMemory("web")) || !strings.Contains(invalidRequest, "512MB") {
		t.Errorf("invalid_resource_request fact = %q, want the key and value 512MB", invalidRequest)
	}
}

// TestSchedulerNeverPreemptsInvalidRequestService: spec resource-requests,
// "Not a preemption victim" (B4). legacy's running instance has an unreadable
// request and the lowest priority; a pending high-priority instance that fits
// nowhere must preempt batch, never legacy, whose size is unknown.
func TestSchedulerNeverPreemptsInvalidRequestService(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyDesiredServiceResourcesMemory("legacy"), "512MB"),
		kv(types.KeyDesiredServicePriority("legacy"), "10"),
		kv(types.KeyDesiredServiceResourcesCPU("batch"), "1000m"),
		kv(types.KeyDesiredServicePriority("batch"), "100"),
		kv(types.KeyDesiredServiceResourcesCPU("critical"), "1000m"),
		kv(types.KeyDesiredServicePriority("critical"), "900"),
		kv(types.KeyObservedInstanceService("legacy-1"), "legacy"),
		kv(types.KeyObservedInstanceState("legacy-1"), "running"),
		kv(types.KeyPlacementInstance("legacy-1"), "node-1"),
		kv(types.KeyObservedInstanceService("batch-1"), "batch"),
		kv(types.KeyObservedInstanceState("batch-1"), "running"),
		kv(types.KeyPlacementInstance("batch-1"), "node-1"),
		kv(types.KeyObservedInstanceService("critical-1"), "critical"),
		kv(types.KeyObservedInstanceState("critical-1"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("legacy-1")); stopped {
		t.Errorf("preempted legacy-1, whose request cannot be read")
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("batch-1")); !stopped {
		t.Errorf("batch-1 not preempted; the test needs a valid victim to prove legacy-1 was skipped")
	}
}

// TestSchedulerRebalanceLeavesInvalidRequestServiceInPlace: spec
// resource-requests, "Legacy bad value" (T3). web opted in to rebalancing and
// node-1 is overloaded, but web's request cannot be read, so its instances
// are not moved; with a readable request the same facts move one.
func TestSchedulerRebalanceLeavesInvalidRequestServiceInPlace(t *testing.T) {
	buildRebalanceFacts := func(webMemory string) []store.Fact {
		entries := []struct{ k, v string }{
			kv(types.KeyDesiredServiceResourcesMemory("web"), webMemory),
			kv(types.KeyDesiredServicePlacementRebalance("web"), "true"),
			kv(types.KeyObservedNodeState("node-1"), "alive"),
			kv(types.KeyObservedNodeAvailableCPU("node-1"), "8000"),
			kv(types.KeyObservedNodeAvailableMemory("node-1"), "8192"),
			kv(types.KeyObservedNodeState("node-2"), "alive"),
			kv(types.KeyObservedNodeAvailableCPU("node-2"), "8000"),
			kv(types.KeyObservedNodeAvailableMemory("node-2"), "8192"),
		}
		for _, instanceID := range []string{"web-1", "web-2", "web-3", "web-4"} {
			entries = append(entries,
				kv(types.KeyObservedInstanceService(instanceID), "web"),
				kv(types.KeyObservedInstanceState(instanceID), "running"),
				kv(types.KeyPlacementInstance(instanceID), "node-1"))
		}
		return buildFacts(entries...)
	}
	countMoves := func(webMemory string) int {
		changes, reconcileError := newTestScheduler().Reconcile(context.Background(), buildRebalanceFacts(webMemory))
		if reconcileError != nil {
			t.Fatal(reconcileError)
		}
		return len(filterPlacementChanges(changes))
	}
	if readableMoves := countMoves("64Mi"); readableMoves == 0 {
		t.Fatalf("no rebalance move with a readable request; the test proves nothing")
	}
	if invalidMoves := countMoves("64MB"); invalidMoves != 0 {
		t.Errorf("%d placement changes for a service with an unreadable request, want none", invalidMoves)
	}
}

// TestSchedulerSkipsPreemptionThatCannotMakeRoom: spec resource-requests, "No
// victim makes room" (D12). Stopping small's 128Mi on a 3072 MiB node never
// lets a 64Gi request fit, so nothing is preempted.
func TestSchedulerSkipsPreemptionThatCannotMakeRoom(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyDesiredServicePriority("big"), "900"),
		kv(types.KeyDesiredServiceResourcesMemory("big"), "64Gi"),
		kv(types.KeyObservedInstanceService("big-1"), "big"),
		kv(types.KeyObservedInstanceState("big-1"), "pending"),
		kv(types.KeyDesiredServicePriority("small"), "10"),
		kv(types.KeyDesiredServiceResourcesMemory("small"), "128Mi"),
		kv(types.KeyObservedInstanceService("small-1"), "small"),
		kv(types.KeyObservedInstanceState("small-1"), "running"),
		kv(types.KeyPlacementInstance("small-1"), "node-1"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "3072"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("small-1")); stopped {
		t.Errorf("preempted small-1, although stopping it cannot make room for 64Gi")
	}
	if reason, _ := putValueOf(changes, types.KeyDerivedSchedulerUnplacedReason("big-1")); reason != string(types.UnplacedInsufficientCapacity) {
		t.Errorf("big-1 unplaced reason = %q, want %s", reason, types.UnplacedInsufficientCapacity)
	}
}

// TestSchedulerPreemptsOnlyAVictimThatMakesRoom: spec resource-requests, "The
// victim that makes room" (D12). node-1 is full with tiny (128Mi) and medium
// (1Gi); only stopping medium lets 768Mi fit. node-2's cheap victim would make
// room too, but the requester requires a label only node-1 has.
func TestSchedulerPreemptsOnlyAVictimThatMakesRoom(t *testing.T) {
	facts := buildFacts(
		kv(types.KeyDesiredServicePriority("urgent"), "900"),
		kv(types.KeyDesiredServiceResourcesMemory("urgent"), "768Mi"),
		kv(types.KeyDesiredServicePlacementRequire("urgent", "tier"), "gold"),
		kv(types.KeyObservedInstanceService("urgent-1"), "urgent"),
		kv(types.KeyObservedInstanceState("urgent-1"), "pending"),
		kv(types.KeyDesiredServicePriority("tiny"), "5"),
		kv(types.KeyDesiredServiceResourcesMemory("tiny"), "128Mi"),
		kv(types.KeyObservedInstanceService("tiny-1"), "tiny"),
		kv(types.KeyObservedInstanceState("tiny-1"), "running"),
		kv(types.KeyPlacementInstance("tiny-1"), "node-1"),
		kv(types.KeyDesiredServicePriority("medium"), "10"),
		kv(types.KeyDesiredServiceResourcesMemory("medium"), "1Gi"),
		kv(types.KeyObservedInstanceService("medium-1"), "medium"),
		kv(types.KeyObservedInstanceState("medium-1"), "running"),
		kv(types.KeyPlacementInstance("medium-1"), "node-1"),
		kv(types.KeyDesiredServicePriority("cheap"), "1"),
		kv(types.KeyDesiredServiceResourcesMemory("cheap"), "1Gi"),
		kv(types.KeyObservedInstanceService("cheap-1"), "cheap"),
		kv(types.KeyObservedInstanceState("cheap-1"), "running"),
		kv(types.KeyPlacementInstance("cheap-1"), "node-2"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "1152"),
		kv(testKeyObservedNodeLabel("node-1", "tier"), "gold"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "4000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "1024"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("medium-1")); !stopped {
		t.Errorf("medium-1 not preempted; it is the only victim that makes room on a node urgent may use")
	}
	for _, keptInstance := range []string{"tiny-1", "cheap-1"} {
		if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped(keptInstance)); stopped {
			t.Errorf("preempted %s, want only medium-1", keptInstance)
		}
	}
}

// perNodeRequesterFacts returns a per-node agent (priority 900) running on
// node-1 with a second instance pending, cheap (priority 5) on node-1, and the
// extra facts; agent's request is agentCPU ("" for none).
func perNodeRequesterFacts(agentCPU string, extraEntries ...struct{ k, v string }) []store.Fact {
	entries := []struct{ k, v string }{
		kv(types.KeyDesiredServicePriority("agent"), "900"),
		kv(types.KeyDesiredServicePlacementMode("agent"), "per-node"),
		kv(types.KeyObservedInstanceService("agent-1"), "agent"),
		kv(types.KeyObservedInstanceState("agent-1"), "running"),
		kv(types.KeyPlacementInstance("agent-1"), "node-1"),
		kv(types.KeyObservedInstanceService("agent-2"), "agent"),
		kv(types.KeyObservedInstanceState("agent-2"), "pending"),
		kv(types.KeyDesiredServicePriority("cheap"), "5"),
		kv(types.KeyObservedInstanceService("cheap-1"), "cheap"),
		kv(types.KeyObservedInstanceState("cheap-1"), "running"),
		kv(types.KeyPlacementInstance("cheap-1"), "node-1"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	}
	if agentCPU != "" {
		entries = append(entries, kv(types.KeyDesiredServiceResourcesCPU("agent"), agentCPU))
	}
	return buildFacts(append(entries, extraEntries...)...)
}

// TestSchedulerPerNodeRequesterPreemptsOnlyOnNodesItCanUse: spec
// resource-requests, "The victim that makes room" (D12, review round 2 I1).
// node-1 already hosts agent-1, so placement never puts agent-2 there: cheap
// on node-1 is not a victim even though stopping it frees enough; low, which
// fills node-2, is. With no other node, nothing is preempted (zero request).
func TestSchedulerPerNodeRequesterPreemptsOnlyOnNodesItCanUse(t *testing.T) {
	twoNodeFacts := perNodeRequesterFacts("500m",
		kv(types.KeyDesiredServiceResourcesCPU("cheap"), "200m"),
		kv(types.KeyDesiredServicePriority("low"), "10"),
		kv(types.KeyDesiredServiceResourcesCPU("low"), "1000m"),
		kv(types.KeyObservedInstanceService("low-1"), "low"),
		kv(types.KeyObservedInstanceState("low-1"), "running"),
		kv(types.KeyPlacementInstance("low-1"), "node-2"),
		kv(types.KeyObservedNodeState("node-2"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-2"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-2"), "4096"),
	)
	changes, reconcileError := newTestScheduler().Reconcile(context.Background(), twoNodeFacts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("cheap-1")); stopped {
		t.Errorf("preempted cheap-1 on node-1, which already hosts agent-1, so agent-2 can never use it")
	}
	if _, stopped := putValueOf(changes, types.KeyDerivedInstanceControllerStopped("low-1")); !stopped {
		t.Errorf("low-1 not preempted; it is the victim on node-2, the only node agent-2 may use")
	}

	zeroRequestChanges, zeroRequestError := newTestScheduler().Reconcile(context.Background(), perNodeRequesterFacts(""))
	if zeroRequestError != nil {
		t.Fatal(zeroRequestError)
	}
	if _, stopped := putValueOf(zeroRequestChanges, types.KeyDerivedInstanceControllerStopped("cheap-1")); stopped {
		t.Errorf("zero request: preempted cheap-1, although agent-2 cannot use node-1, the only node")
	}
}
