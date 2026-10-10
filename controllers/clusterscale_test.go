// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/infra"
	storelib "github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// filterCapacityRequestChanges returns only changes under the capacity request prefix.
func filterCapacityRequestChanges(changes []Change) []Change {
	var filtered []Change
	for _, change := range changes {
		if strings.HasPrefix(change.Key, types.ScanDerivedCapacityRequests) {
			filtered = append(filtered, change)
		}
	}
	return filtered
}

// findCapacityRequestStateChange finds a state change for the given request ID.
func findCapacityRequestStateChange(changes []Change, requestID string) *Change {
	targetKey := types.KeyDerivedCapacityRequestState(requestID)
	for _, change := range changes {
		if change.Key == targetKey {
			return &change
		}
	}
	return nil
}

func TestClusterAutoscaleEmitsCapacityRequestForInsufficientCapacity(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000,"memory":512,"architecture":"amd64"}`)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	capacityChanges := filterCapacityRequestChanges(changes)
	if len(capacityChanges) == 0 {
		t.Fatal("expected capacity request changes for insufficient-capacity demand")
	}

	stateChange := findCapacityRequestStateChange(changes, "inst-1")
	if stateChange == nil {
		t.Fatal("expected capacity request state change for inst-1")
	}
	if string(stateChange.Value) != string(types.CapacityRequestPending) {
		t.Errorf("expected pending state, got %s", stateChange.Value)
	}
}

func TestClusterAutoscaleIgnoresUnsatisfiableConstraint(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedUnsatisfiableConstraint))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000,"memory":512,"architecture":"arm64"}`)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	capacityChanges := filterCapacityRequestChanges(changes)
	if len(capacityChanges) != 0 {
		t.Fatalf("expected no capacity requests for unsatisfiable constraints, got %d", len(capacityChanges))
	}
}

func TestClusterAutoscaleRespectsMaxNodes(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDesiredClusterAutoscaleMaxNodes(), Value: []byte("2")},
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-2"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-2"), Value: []byte(`{"cpu":1000}`)},
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-3"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-3"), Value: []byte(`{"cpu":1000}`)},
		// 1 alive auto-node already exists, max=2, so only 1 more allowed.
		{Key: types.KeyObservedNodeState("auto-node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	stateChanges := 0
	for _, change := range changes {
		if strings.HasSuffix(change.Key, "/state") && strings.HasPrefix(change.Key, types.ScanDerivedCapacityRequests) {
			if string(change.Value) == string(types.CapacityRequestPending) {
				stateChanges++
			}
		}
	}
	if stateChanges != 1 {
		t.Fatalf("expected 1 new capacity request (maxNodes=2, 1 alive), got %d", stateChanges)
	}
}

func TestClusterAutoscaleDoesNotDuplicateInFlightRequests(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
		// An in-flight request already exists for inst-1.
		{Key: types.KeyDerivedCapacityRequestState("inst-1"), Value: []byte(string(types.CapacityRequestLaunching))},
		{Key: types.KeyDerivedCapacityRequestRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	newPendingCount := 0
	for _, change := range changes {
		if strings.HasSuffix(change.Key, "/state") && strings.HasPrefix(change.Key, types.ScanDerivedCapacityRequests) {
			if string(change.Value) == string(types.CapacityRequestPending) {
				newPendingCount++
			}
		}
	}
	if newPendingCount != 0 {
		t.Fatalf("expected 0 new capacity requests (inst-1 already in-flight), got %d", newPendingCount)
	}
}

func TestClusterAutoscalePostCommitTransitionsPendingToLaunching(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	ctx := context.Background()

	// Write a pending capacity request to the store.
	factStore.Put(ctx, types.KeyDerivedCapacityRequestState("inst-1"), []byte(string(types.CapacityRequestPending)))
	factStore.Put(ctx, types.KeyDerivedCapacityRequestRequirements("inst-1"), []byte(`{"cpu":1000,"memory":512,"architecture":"amd64"}`))
	factStore.Put(ctx, types.KeyDerivedCapacityRequestReason("inst-1"), []byte("test"))

	postCommitError := autoscaleController.ExecutePostCommitOperations(ctx)
	if postCommitError != nil {
		t.Fatalf("ExecutePostCommitOperations failed: %v", postCommitError)
	}

	// Check that the request transitioned to launching with a node_id.
	stateFact, _ := factStore.Get(ctx, types.KeyDerivedCapacityRequestState("inst-1"))
	if stateFact == nil || string(stateFact.Value) != string(types.CapacityRequestLaunching) {
		stateVal := "nil"
		if stateFact != nil {
			stateVal = string(stateFact.Value)
		}
		t.Fatalf("expected launching state, got %s", stateVal)
	}

	nodeIDFact, _ := factStore.Get(ctx, types.KeyDerivedCapacityRequestNodeID("inst-1"))
	if nodeIDFact == nil || !strings.HasPrefix(string(nodeIDFact.Value), "auto-node-") {
		t.Fatal("expected node_id to be set to an auto-node")
	}
}

func TestClusterAutoscalePostCommitLaunchingBecomesReady(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	ctx := context.Background()

	// Write a launching request with node_id.
	factStore.Put(ctx, types.KeyDerivedCapacityRequestState("inst-1"), []byte(string(types.CapacityRequestLaunching)))
	factStore.Put(ctx, types.KeyDerivedCapacityRequestNodeID("inst-1"), []byte("auto-node-1"))

	// The node is alive in observed state.
	factStore.Put(ctx, types.KeyObservedNodeState("auto-node-1"), []byte(string(types.NodeAlive)))

	postCommitError := autoscaleController.ExecutePostCommitOperations(ctx)
	if postCommitError != nil {
		t.Fatalf("ExecutePostCommitOperations failed: %v", postCommitError)
	}

	stateFact, _ := factStore.Get(ctx, types.KeyDerivedCapacityRequestState("inst-1"))
	if stateFact == nil || string(stateFact.Value) != string(types.CapacityRequestReady) {
		stateVal := "nil"
		if stateFact != nil {
			stateVal = string(stateFact.Value)
		}
		t.Fatalf("expected ready state, got %s", stateVal)
	}
}

func TestClusterAutoscaleCleansUpReadyRequests(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedCapacityRequestState("inst-1"), Value: []byte(string(types.CapacityRequestReady))},
		{Key: types.KeyDerivedCapacityRequestNodeID("inst-1"), Value: []byte("auto-node-1")},
		{Key: types.KeyObservedNodeState("auto-node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	deleteCount := 0
	for _, change := range changes {
		if change.Type == storelib.OpDelete && strings.HasPrefix(change.Key, types.ScanDerivedCapacityRequests) {
			deleteCount++
		}
	}
	if deleteCount == 0 {
		t.Fatal("expected delete changes to clean up ready capacity request")
	}
}

func TestClusterAutoscaleScaleDownEmitsRemovalRequest(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDesiredClusterAutoscaleMinNodes(), Value: []byte("1")},
		// Two alive auto-nodes, no placed instances — one should be removed.
		{Key: types.KeyObservedNodeState("auto-node-1"), Value: []byte(string(types.NodeAlive))},
		{Key: types.KeyObservedNodeState("auto-node-2"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	removalCount := 0
	for _, change := range changes {
		if strings.HasSuffix(change.Key, "/state") && strings.HasPrefix(change.Key, types.ScanDerivedCapacityRequests) {
			if string(change.Value) == string(types.CapacityRequestRemoving) {
				removalCount++
			}
		}
	}
	if removalCount != 1 {
		t.Fatalf("expected 1 removal request (2 idle, min=1), got %d", removalCount)
	}
}

func TestClusterAutoscalePostCommitExecutesRemoval(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	ctx := context.Background()

	// First provision a node so it can be removed.
	simulatorProvider.RequestNodeWithRequirements(ctx, "setup", infra.CapacityRequestRequirements{})

	// Write a removing request.
	factStore.Put(ctx, types.KeyDerivedCapacityRequestState("remove-auto-node-1"), []byte(string(types.CapacityRequestRemoving)))
	factStore.Put(ctx, types.KeyDerivedCapacityRequestNodeID("remove-auto-node-1"), []byte("auto-node-1"))

	postCommitError := autoscaleController.ExecutePostCommitOperations(ctx)
	if postCommitError != nil {
		t.Fatalf("ExecutePostCommitOperations failed: %v", postCommitError)
	}

	stateFact, _ := factStore.Get(ctx, types.KeyDerivedCapacityRequestState("remove-auto-node-1"))
	if stateFact == nil || string(stateFact.Value) != string(types.CapacityRequestReady) {
		stateVal := "nil"
		if stateFact != nil {
			stateVal = string(stateFact.Value)
		}
		t.Fatalf("expected ready state after removal, got %s", stateVal)
	}
}

func TestClusterAutoscaleNoDuplicateKeysWhenFailedRequestAndPersistentDemand(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedInsufficientCapacity))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
		{Key: types.KeyDerivedCapacityRequestState("inst-1"), Value: []byte(string(types.CapacityRequestFailed))},
		{Key: types.KeyDerivedCapacityRequestRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte(string(types.NodeAlive))},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	keyOccurrences := make(map[string]int)
	for _, change := range changes {
		keyOccurrences[change.Key]++
	}
	for key, count := range keyOccurrences {
		if count > 1 {
			t.Fatalf("duplicate key in change set: %s (appeared %d times)", key, count)
		}
	}

	newPendingCount := 0
	for _, change := range changes {
		if change.Type == storelib.OpPut && strings.HasSuffix(change.Key, "/state") &&
			string(change.Value) == string(types.CapacityRequestPending) {
			newPendingCount++
		}
	}
	if newPendingCount != 0 {
		t.Fatal("expected no new pending request in same cycle as failed request cleanup")
	}
}

func TestClusterAutoscaleHandlesNoNodesReason(t *testing.T) {
	factStore := storelib.NewMemoryStore()
	simulatorProvider := infra.NewSimulatorInfraProvider(factStore)
	autoscaleController := NewClusterAutoscaleController(simulatorProvider, factStore)

	facts := []storelib.Fact{
		{Key: types.KeyDerivedSchedulerUnplacedReason("inst-1"), Value: []byte(string(types.UnplacedNoNodes))},
		{Key: types.KeyDerivedSchedulerUnplacedRequirements("inst-1"), Value: []byte(`{"cpu":1000}`)},
	}
	storelib.SortFacts(facts)

	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("Reconcile failed: %v", reconcileError)
	}

	stateChange := findCapacityRequestStateChange(changes, "inst-1")
	if stateChange == nil {
		t.Fatal("expected capacity request for no_nodes demand")
	}
	if string(stateChange.Value) != string(types.CapacityRequestPending) {
		t.Errorf("expected pending state, got %s", stateChange.Value)
	}
}
