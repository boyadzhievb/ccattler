// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// testKeyObservedNodeProviderInstanceID constructs the store path mapping a
// node to its cloud provider instance ID, for use in tests only. Production
// code reads this via prefix scan on ScanObservedNodes.
func testKeyObservedNodeProviderInstanceID(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/provider_instance_id", types.PrefixObserved, nodeID)
}

func TestNodeLifecycleControllerDetectsTerminatedInstance(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, cloud.InstanceConfig{})
	simulatorProvider.SetInstanceNodeID(instanceID, "worker-1")
	simulatorProvider.SimulateInstanceTermination(instanceID)

	nodeLifecycleController := NewNodeLifecycleController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeAlive))},
		{Key: testKeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
	}

	store.SortFacts(facts)
	proposedChanges, reconcileError := nodeLifecycleController.Reconcile(ctx, facts)
	if reconcileError != nil {
		testing.Fatalf("Reconcile failed: %v", reconcileError)
	}

	foundDrainingChange := false
	foundCloudStateChange := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("worker-1") && string(change.Value) == string(types.NodeDraining) {
			foundDrainingChange = true
		}
		if change.Key == types.KeyObservedCloudInstanceState(instanceID) && string(change.Value) == string(cloud.InstanceStateTerminated) {
			foundCloudStateChange = true
		}
	}
	if !foundDrainingChange {
		testing.Error("expected change to mark worker-1 as draining")
	}
	if !foundCloudStateChange {
		testing.Error("expected change to write cloud instance terminated state")
	}
}

func TestNodeLifecycleControllerDrainingToUnreachable(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, cloud.InstanceConfig{})
	simulatorProvider.SetInstanceNodeID(instanceID, "worker-1")
	simulatorProvider.SimulateInstanceTermination(instanceID)

	nodeLifecycleController := NewNodeLifecycleController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeDraining))},
		{Key: testKeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
		{Key: types.KeyObservedCloudInstanceState(instanceID), Value: []byte(string(cloud.InstanceStateTerminated))},
	}

	store.SortFacts(facts)
	proposedChanges, _ := nodeLifecycleController.Reconcile(ctx, facts)

	foundUnreachableChange := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("worker-1") && string(change.Value) == string(types.NodeUnreachable) {
			foundUnreachableChange = true
		}
	}
	if !foundUnreachableChange {
		testing.Error("expected draining node to transition to unreachable")
	}
}

func TestNodeLifecycleControllerIgnoresRunningInstances(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, cloud.InstanceConfig{})
	simulatorProvider.SetInstanceNodeID(instanceID, "worker-1")

	nodeLifecycleController := NewNodeLifecycleController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeAlive))},
		{Key: testKeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
	}

	store.SortFacts(facts)
	proposedChanges, _ := nodeLifecycleController.Reconcile(ctx, facts)

	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("worker-1") {
			testing.Error("should not change state for running cloud instance")
		}
	}
}

// TestCloudLoadBalancerControllerCreatesLoadBalancer verifies that a pending
// ensure operation is emitted during Reconcile and the actual provider call
// happens during post-commit execution.
func TestCloudLoadBalancerControllerCreatesLoadBalancer(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, stateStore)

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceExpose("web", 80), Value: []byte("")},
		{Key: types.KeyDesiredServiceExposeExternal("web", 80), Value: []byte("http")},
		{Key: types.KeyEndpoint("web", "inst-1", 80), Value: []byte("10.0.1.5:80")},
		{Key: types.KeyObservedNodeAddress("node-1"), Value: []byte("192.168.1.10")},
	}

	store.SortFacts(facts)
	proposedChanges, reconcileError := loadBalancerController.Reconcile(ctx, facts)
	if reconcileError != nil {
		testing.Fatalf("Reconcile failed: %v", reconcileError)
	}

	if len(simulatorProvider.EnsureLoadBalancerCalls) != 0 {
		testing.Fatal("provider should NOT be called during Reconcile")
	}

	foundPendingChange := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedCloudLBPendingOperation("web") {
			foundPendingChange = true
		}
	}
	if !foundPendingChange {
		testing.Fatal("expected pending_operation change for LB ensure")
	}

	for _, change := range proposedChanges {
		if change.Type == store.OpPut {
			stateStore.Put(ctx, change.Key, change.Value)
		}
	}
	stateStore.Put(ctx, types.KeyEndpoint("web", "inst-1", 80), []byte("10.0.1.5:80"))
	stateStore.Put(ctx, types.KeyObservedNodeAddress("node-1"), []byte("192.168.1.10"))
	stateStore.Put(ctx, types.KeyObservedInstanceNode("inst-1"), []byte("node-1"))

	if postCommitErr := loadBalancerController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		testing.Fatal(postCommitErr)
	}

	if len(simulatorProvider.EnsureLoadBalancerCalls) != 1 {
		testing.Fatalf("expected 1 EnsureLoadBalancer call after post-commit, got %d",
			len(simulatorProvider.EnsureLoadBalancerCalls))
	}
	ensuredConfig := simulatorProvider.EnsureLoadBalancerCalls[0]
	if ensuredConfig.ServiceName != "web" {
		testing.Errorf("expected service name web, got %q", ensuredConfig.ServiceName)
	}

	addressFact, getErr := stateStore.Get(ctx, types.KeyObservedCloudLoadBalancerAddress("web"))
	if getErr != nil {
		testing.Fatal("expected observed LB address after post-commit")
	}
	if string(addressFact.Value) == "" {
		testing.Error("expected non-empty LB address")
	}
}

// TestCloudLoadBalancerControllerDeletesOrphanedLoadBalancer verifies that a
// pending delete is emitted and executed post-commit.
func TestCloudLoadBalancerControllerDeletesOrphanedLoadBalancer(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider, stateStore)

	facts := []store.Fact{
		{Key: types.KeyObservedCloudLoadBalancerAddress("old-service"), Value: []byte("203.0.113.1")},
	}

	store.SortFacts(facts)
	proposedChanges, _ := loadBalancerController.Reconcile(ctx, facts)

	if len(simulatorProvider.DeleteLoadBalancerCalls) != 0 {
		testing.Fatal("provider should NOT be called during Reconcile")
	}

	for _, change := range proposedChanges {
		if change.Type == store.OpPut {
			stateStore.Put(ctx, change.Key, change.Value)
		}
	}
	stateStore.Put(ctx, types.KeyObservedCloudLoadBalancerAddress("old-service"), []byte("203.0.113.1"))

	if postCommitErr := loadBalancerController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		testing.Fatal(postCommitErr)
	}

	if len(simulatorProvider.DeleteLoadBalancerCalls) != 1 {
		testing.Fatalf("expected 1 DeleteLoadBalancer call, got %d",
			len(simulatorProvider.DeleteLoadBalancerCalls))
	}
	if simulatorProvider.DeleteLoadBalancerCalls[0] != "old-service" {
		testing.Errorf("expected delete for old-service, got %q",
			simulatorProvider.DeleteLoadBalancerCalls[0])
	}
}

// TestCloudRouteControllerCreatesRoutes verifies that a pending ensure
// operation is emitted and executed post-commit.
func TestCloudRouteControllerCreatesRoutes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider, stateStore)

	facts := []store.Fact{
		{Key: types.KeyNetworkNodeSubnet("worker-1"), Value: []byte("10.244.1.0/24")},
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeAlive))},
		{Key: testKeyObservedNodeProviderInstanceID("worker-1"), Value: []byte("i-abc123")},
	}

	store.SortFacts(facts)
	proposedChanges, reconcileError := cloudRouteController.Reconcile(ctx, facts)
	if reconcileError != nil {
		testing.Fatalf("Reconcile failed: %v", reconcileError)
	}

	if len(simulatorProvider.EnsureRouteCalls) != 0 {
		testing.Fatal("provider should NOT be called during Reconcile")
	}

	for _, change := range proposedChanges {
		if change.Type == store.OpPut {
			stateStore.Put(ctx, change.Key, change.Value)
		}
	}

	if postCommitErr := cloudRouteController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		testing.Fatal(postCommitErr)
	}

	if len(simulatorProvider.EnsureRouteCalls) != 1 {
		testing.Fatalf("expected 1 EnsureRoute call after post-commit, got %d",
			len(simulatorProvider.EnsureRouteCalls))
	}
	ensuredRoute := simulatorProvider.EnsureRouteCalls[0]
	if ensuredRoute.DestinationCIDR != "10.244.1.0/24" {
		testing.Errorf("expected CIDR 10.244.1.0/24, got %q", ensuredRoute.DestinationCIDR)
	}
	if ensuredRoute.TargetNodeID != "worker-1" {
		testing.Errorf("expected target worker-1, got %q", ensuredRoute.TargetNodeID)
	}

	routeFact, getErr := stateStore.Get(ctx, types.KeyObservedCloudRoute("10.244.1.0/24"))
	if getErr != nil {
		testing.Fatal("expected observed route after post-commit")
	}
	if string(routeFact.Value) != "worker-1" {
		testing.Errorf("observed route target = %s, want worker-1", routeFact.Value)
	}
}

// TestCloudRouteControllerDeletesStaleRoutes verifies that stale routes are
// deleted via pending operation and post-commit.
func TestCloudRouteControllerDeletesStaleRoutes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider, stateStore)

	facts := []store.Fact{
		{Key: types.KeyObservedCloudRoute("10.244.99.0/24"), Value: []byte("dead-node")},
	}

	store.SortFacts(facts)
	proposedChanges, _ := cloudRouteController.Reconcile(ctx, facts)

	if len(simulatorProvider.DeleteRouteCalls) != 0 {
		testing.Fatal("provider should NOT be called during Reconcile")
	}

	for _, change := range proposedChanges {
		if change.Type == store.OpPut {
			stateStore.Put(ctx, change.Key, change.Value)
		}
	}
	stateStore.Put(ctx, types.KeyObservedCloudRoute("10.244.99.0/24"), []byte("dead-node"))

	if postCommitErr := cloudRouteController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		testing.Fatal(postCommitErr)
	}

	if len(simulatorProvider.DeleteRouteCalls) != 1 {
		testing.Fatalf("expected 1 DeleteRoute call, got %d",
			len(simulatorProvider.DeleteRouteCalls))
	}
}

// TestCloudRouteControllerSkipsUnreachableNodes verifies that routes are not
// created for unreachable nodes.
func TestCloudRouteControllerSkipsUnreachableNodes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider, stateStore)

	facts := []store.Fact{
		{Key: types.KeyNetworkNodeSubnet("worker-1"), Value: []byte("10.244.1.0/24")},
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeUnreachable))},
		{Key: testKeyObservedNodeProviderInstanceID("worker-1"), Value: []byte("i-abc123")},
	}

	store.SortFacts(facts)
	proposedChanges, _ := cloudRouteController.Reconcile(ctx, facts)

	if len(proposedChanges) != 0 {
		testing.Errorf("expected 0 changes for unreachable node, got %d", len(proposedChanges))
	}
}

func TestNodeLifecycleControllerNameAndWatch(testing *testing.T) {
	nodeLifecycleController := NewNodeLifecycleController(cloud.NewSimulatorCloudProvider())
	if nodeLifecycleController.Name() != "cloud-node-lifecycle" {
		testing.Errorf("unexpected controller name: %q", nodeLifecycleController.Name())
	}
	if len(nodeLifecycleController.Watch()) == 0 {
		testing.Error("expected non-empty watch prefixes")
	}
}

func TestCloudLoadBalancerControllerNameAndWatch(testing *testing.T) {
	loadBalancerController := NewCloudLoadBalancerController(cloud.NewSimulatorCloudProvider(), store.NewMemoryStore())
	if loadBalancerController.Name() != "cloud-loadbalancer" {
		testing.Errorf("unexpected controller name: %q", loadBalancerController.Name())
	}
	if len(loadBalancerController.Watch()) == 0 {
		testing.Error("expected non-empty watch prefixes")
	}
}

func TestCloudRouteControllerNameAndWatch(testing *testing.T) {
	cloudRouteController := NewCloudRouteController(cloud.NewSimulatorCloudProvider(), store.NewMemoryStore())
	if cloudRouteController.Name() != "cloud-routes" {
		testing.Errorf("unexpected controller name: %q", cloudRouteController.Name())
	}
	if len(cloudRouteController.Watch()) == 0 {
		testing.Error("expected non-empty watch prefixes")
	}
}
