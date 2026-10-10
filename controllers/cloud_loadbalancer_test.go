// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperReconcileCloudLB builds the fact list from the store and runs Reconcile.
func helperReconcileCloudLB(testHandle *testing.T, loadBalancerController *CloudLoadBalancerController, factStore store.StateStore) []Change {
	testHandle.Helper()
	ctx := context.Background()
	var allFacts []store.Fact
	for _, prefix := range loadBalancerController.Watch() {
		facts, scanError := factStore.Scan(ctx, prefix)
		if scanError != nil {
			testHandle.Fatal(scanError)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)
	changes, reconcileError := loadBalancerController.Reconcile(ctx, allFacts)
	if reconcileError != nil {
		testHandle.Fatalf("Reconcile failed: %v", reconcileError)
	}
	return changes
}

// TestCloudLBEmitsEnsureForExistingLoadBalancer verifies that an existing
// load balancer still receives an "ensure" operation so backend changes
// propagate to the cloud provider.
func TestCloudLBEmitsEnsureForExistingLoadBalancer(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, factStore)

	factStore.Put(ctx, types.KeyDesiredServiceExposeExternal("web", 443), []byte("http"))
	factStore.Put(ctx, types.KeyObservedCloudLoadBalancerAddress("web"), []byte("1.2.3.4"))

	changes := helperReconcileCloudLB(testHandle, loadBalancerController, factStore)

	foundEnsure := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedCloudLBPendingOperation("web") && change.Type == store.OpPut {
			var pendingOp pendingCloudLBOperation
			if unmarshalErr := json.Unmarshal(change.Value, &pendingOp); unmarshalErr != nil {
				testHandle.Fatalf("failed to unmarshal pending op: %v", unmarshalErr)
			}
			if pendingOp.Kind == "ensure" {
				foundEnsure = true
			}
		}
	}
	if !foundEnsure {
		testHandle.Fatal("expected ensure operation for existing LB to update backends")
	}
}

// TestCloudLBSkipsEnsureWhenConfigUnchanged verifies that Reconcile does not
// emit an ensure operation when the applied config hash matches the current
// configuration, breaking the feedback loop of unnecessary cloud API calls.
func TestCloudLBSkipsEnsureWhenConfigUnchanged(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, factStore)

	// Set up desired external service and observed LB.
	factStore.Put(ctx, types.KeyDesiredServiceExposeExternal("web", 443), []byte("http"))
	factStore.Put(ctx, types.KeyObservedCloudLoadBalancerAddress("web"), []byte("1.2.3.4"))

	// Set up an endpoint for the service.
	factStore.Put(ctx, types.ScanEndpoints+"web/inst-1", []byte("10.0.0.1:8080"))

	// Compute and store the applied hash matching the current configuration.
	configHash := computeLoadBalancerConfigHash("web", 443, "http", []string{"10.0.0.1:8080"})
	factStore.Put(ctx, types.KeyDerivedCloudLBAppliedHash("web"), []byte(configHash))

	changes := helperReconcileCloudLB(testHandle, loadBalancerController, factStore)

	for _, change := range changes {
		if change.Key == types.KeyDerivedCloudLBPendingOperation("web") && change.Type == store.OpPut {
			var pendingOp pendingCloudLBOperation
			if unmarshalErr := json.Unmarshal(change.Value, &pendingOp); unmarshalErr != nil {
				testHandle.Fatalf("failed to unmarshal pending op: %v", unmarshalErr)
			}
			if pendingOp.Kind == "ensure" {
				testHandle.Fatal("expected no ensure operation when config hash is unchanged")
			}
		}
	}
}

// TestCloudLBCancelsPendingEnsureWhenServiceDeleted verifies that an orphaned
// pending "ensure" operation is deleted when the service is no longer external.
func TestCloudLBCancelsPendingEnsureWhenServiceDeleted(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, factStore)

	pendingOp := pendingCloudLBOperation{
		Kind:        "ensure",
		ServiceName: "web",
		Port:        443,
		TargetPort:  443,
		Protocol:    "http",
	}
	pendingJSON, _ := json.Marshal(pendingOp)
	factStore.Put(ctx, types.KeyDerivedCloudLBPendingOperation("web"), pendingJSON)

	changes := helperReconcileCloudLB(testHandle, loadBalancerController, factStore)

	foundDelete := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedCloudLBPendingOperation("web") && change.Type == store.OpDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		testHandle.Fatal("expected delete of orphaned pending ensure for removed service")
	}
}

// TestCloudLBPostCommitUpdatesBackends verifies that EnsureLoadBalancer is
// called with current backends when the post-commit executor runs.
func TestCloudLBPostCommitUpdatesBackends(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, factStore)

	pendingOp := pendingCloudLBOperation{
		Kind:        "ensure",
		ServiceName: "web",
		Port:        443,
		TargetPort:  443,
		Protocol:    "http",
	}
	pendingJSON, _ := json.Marshal(pendingOp)
	factStore.Put(ctx, types.KeyDerivedCloudLBPendingOperation("web"), pendingJSON)

	factStore.Put(ctx, types.ScanEndpoints+"web/inst-1", []byte("10.0.0.1:8080"))
	factStore.Put(ctx, strings.TrimSuffix(types.ScanObservedInstances, "/")+"/inst-1/node", []byte("node-1"))
	factStore.Put(ctx, strings.TrimSuffix(types.ScanObservedNodes, "/")+"/node-1/address", []byte("192.168.1.10"))

	executeError := loadBalancerController.ExecutePostCommitOperations(ctx)
	if executeError != nil {
		testHandle.Fatalf("ExecutePostCommitOperations failed: %v", executeError)
	}

	if len(simulatorProvider.EnsureLoadBalancerCalls) != 1 {
		testHandle.Fatalf("expected 1 EnsureLoadBalancer call, got %d", len(simulatorProvider.EnsureLoadBalancerCalls))
	}

	callConfig := simulatorProvider.EnsureLoadBalancerCalls[0]
	if len(callConfig.Backends) == 0 {
		testHandle.Fatal("expected at least one backend in EnsureLoadBalancer call")
	}
}
