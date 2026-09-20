package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestNodeLifecycleControllerDetectsTerminatedInstance(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, cloud.InstanceConfig{})
	simulatorProvider.SetInstanceNodeID(instanceID, "worker-1")
	simulatorProvider.SimulateInstanceTermination(instanceID)

	nodeLifecycleController := NewNodeLifecycleController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeAlive))},
		{Key: types.KeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
	}

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
		{Key: types.KeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
		{Key: types.KeyObservedCloudInstanceState(instanceID), Value: []byte(string(cloud.InstanceStateTerminated))},
	}

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
		{Key: types.KeyObservedNodeProviderInstanceID("worker-1"), Value: []byte(instanceID)},
	}

	proposedChanges, _ := nodeLifecycleController.Reconcile(ctx, facts)

	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("worker-1") {
			testing.Error("should not change state for running cloud instance")
		}
	}
}

func TestCloudLoadBalancerControllerCreatesLoadBalancer(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceExpose("web", 80), Value: []byte("")},
		{Key: types.KeyDesiredServiceExposeExternal("web", 80), Value: []byte("http")},
		{Key: types.KeyEndpoint("web", "inst-1"), Value: []byte("10.0.1.5:80")},
		{Key: types.KeyObservedNodeAddress("node-1"), Value: []byte("192.168.1.10")},
	}

	proposedChanges, reconcileError := loadBalancerController.Reconcile(ctx, facts)
	if reconcileError != nil {
		testing.Fatalf("Reconcile failed: %v", reconcileError)
	}

	if len(simulatorProvider.EnsureLoadBalancerCalls) != 1 {
		testing.Fatalf("expected 1 EnsureLoadBalancer call, got %d", len(simulatorProvider.EnsureLoadBalancerCalls))
	}
	ensuredConfig := simulatorProvider.EnsureLoadBalancerCalls[0]
	if ensuredConfig.ServiceName != "web" {
		testing.Errorf("expected service name web, got %q", ensuredConfig.ServiceName)
	}
	if ensuredConfig.Protocol != "http" {
		testing.Errorf("expected protocol http, got %q", ensuredConfig.Protocol)
	}

	foundAddressChange := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedCloudLoadBalancerAddress("web") {
			foundAddressChange = true
		}
	}
	if !foundAddressChange {
		testing.Error("expected change to write load balancer address")
	}
}

func TestCloudLoadBalancerControllerDeletesOrphanedLoadBalancer(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	loadBalancerController := NewCloudLoadBalancerController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedCloudLoadBalancerAddress("old-service"), Value: []byte("203.0.113.1")},
	}

	proposedChanges, _ := loadBalancerController.Reconcile(ctx, facts)

	if len(simulatorProvider.DeleteLoadBalancerCalls) != 1 {
		testing.Fatalf("expected 1 DeleteLoadBalancer call, got %d", len(simulatorProvider.DeleteLoadBalancerCalls))
	}
	if simulatorProvider.DeleteLoadBalancerCalls[0] != "old-service" {
		testing.Errorf("expected delete for old-service, got %q", simulatorProvider.DeleteLoadBalancerCalls[0])
	}

	foundDelete := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedCloudLoadBalancerAddress("old-service") && change.Type == store.OpDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		testing.Error("expected delete change for orphaned load balancer address")
	}
}

func TestCloudRouteControllerCreatesRoutes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyNetworkNodeSubnet("worker-1"), Value: []byte("10.244.1.0/24")},
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeAlive))},
		{Key: types.KeyObservedNodeProviderInstanceID("worker-1"), Value: []byte("i-abc123")},
	}

	proposedChanges, reconcileError := cloudRouteController.Reconcile(ctx, facts)
	if reconcileError != nil {
		testing.Fatalf("Reconcile failed: %v", reconcileError)
	}

	if len(simulatorProvider.EnsureRouteCalls) != 1 {
		testing.Fatalf("expected 1 EnsureRoute call, got %d", len(simulatorProvider.EnsureRouteCalls))
	}
	ensuredRoute := simulatorProvider.EnsureRouteCalls[0]
	if ensuredRoute.DestinationCIDR != "10.244.1.0/24" {
		testing.Errorf("expected CIDR 10.244.1.0/24, got %q", ensuredRoute.DestinationCIDR)
	}
	if ensuredRoute.TargetNodeID != "worker-1" {
		testing.Errorf("expected target worker-1, got %q", ensuredRoute.TargetNodeID)
	}

	foundRouteChange := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedCloudRoute("10.244.1.0/24") {
			foundRouteChange = true
		}
	}
	if !foundRouteChange {
		testing.Error("expected change to write cloud route fact")
	}
}

func TestCloudRouteControllerDeletesStaleRoutes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyObservedCloudRoute("10.244.99.0/24"), Value: []byte("dead-node")},
	}

	proposedChanges, _ := cloudRouteController.Reconcile(ctx, facts)

	if len(simulatorProvider.DeleteRouteCalls) != 1 {
		testing.Fatalf("expected 1 DeleteRoute call, got %d", len(simulatorProvider.DeleteRouteCalls))
	}

	foundDelete := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedCloudRoute("10.244.99.0/24") && change.Type == store.OpDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		testing.Error("expected delete change for stale cloud route")
	}
}

func TestCloudRouteControllerSkipsUnreachableNodes(testing *testing.T) {
	simulatorProvider := cloud.NewSimulatorCloudProvider()
	ctx := context.Background()

	cloudRouteController := NewCloudRouteController(simulatorProvider)

	facts := []store.Fact{
		{Key: types.KeyNetworkNodeSubnet("worker-1"), Value: []byte("10.244.1.0/24")},
		{Key: types.KeyObservedNodeState("worker-1"), Value: []byte(string(types.NodeUnreachable))},
		{Key: types.KeyObservedNodeProviderInstanceID("worker-1"), Value: []byte("i-abc123")},
	}

	cloudRouteController.Reconcile(ctx, facts)

	if len(simulatorProvider.EnsureRouteCalls) != 0 {
		testing.Errorf("expected 0 EnsureRoute calls for unreachable node, got %d", len(simulatorProvider.EnsureRouteCalls))
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
	loadBalancerController := NewCloudLoadBalancerController(cloud.NewSimulatorCloudProvider())
	if loadBalancerController.Name() != "cloud-loadbalancer" {
		testing.Errorf("unexpected controller name: %q", loadBalancerController.Name())
	}
	if len(loadBalancerController.Watch()) == 0 {
		testing.Error("expected non-empty watch prefixes")
	}
}

func TestCloudRouteControllerNameAndWatch(testing *testing.T) {
	cloudRouteController := NewCloudRouteController(cloud.NewSimulatorCloudProvider())
	if cloudRouteController.Name() != "cloud-routes" {
		testing.Errorf("unexpected controller name: %q", cloudRouteController.Name())
	}
	if len(cloudRouteController.Watch()) == 0 {
		testing.Error("expected non-empty watch prefixes")
	}
}
