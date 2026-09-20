package cloud

import (
	"context"
	"testing"
)

func TestSimulatorCloudProviderCreateAndListInstances(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, createError := simulatorProvider.CreateInstance(ctx, InstanceConfig{
		InstanceType: "m5.large",
		Region:       "us-east-1",
	})
	if createError != nil {
		testing.Fatalf("CreateInstance failed: %v", createError)
	}
	if instanceID == "" {
		testing.Fatal("expected non-empty instance ID")
	}

	instanceList, listError := simulatorProvider.ListInstances(ctx)
	if listError != nil {
		testing.Fatalf("ListInstances failed: %v", listError)
	}
	if len(instanceList) != 1 {
		testing.Fatalf("expected 1 instance, got %d", len(instanceList))
	}
	if instanceList[0].ProviderInstanceID != instanceID {
		testing.Errorf("expected instance ID %q, got %q", instanceID, instanceList[0].ProviderInstanceID)
	}
	if instanceList[0].State != InstanceStateRunning {
		testing.Errorf("expected state running, got %q", instanceList[0].State)
	}
}

func TestSimulatorCloudProviderTerminateInstance(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, InstanceConfig{})
	terminateError := simulatorProvider.TerminateInstance(ctx, instanceID)
	if terminateError != nil {
		testing.Fatalf("TerminateInstance failed: %v", terminateError)
	}

	instanceList, _ := simulatorProvider.ListInstances(ctx)
	if instanceList[0].State != InstanceStateTerminated {
		testing.Errorf("expected state terminated, got %q", instanceList[0].State)
	}
}

func TestSimulatorCloudProviderSimulateTermination(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, InstanceConfig{})
	simulatorProvider.SimulateInstanceTermination(instanceID)

	instanceList, _ := simulatorProvider.ListInstances(ctx)
	if instanceList[0].State != InstanceStateTerminated {
		testing.Errorf("expected state terminated after simulation, got %q", instanceList[0].State)
	}
}

func TestSimulatorCloudProviderEnsureAndDeleteLoadBalancer(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	externalAddress, ensureError := simulatorProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{
		ServiceName: "web",
		Port:        80,
		Protocol:    "http",
		Backends: []LoadBalancerBackend{
			{NodeID: "node-1", Address: "10.0.1.5", Port: 8080},
		},
	})
	if ensureError != nil {
		testing.Fatalf("EnsureLoadBalancer failed: %v", ensureError)
	}
	if externalAddress == "" {
		testing.Fatal("expected non-empty external address")
	}

	loadBalancerList, _ := simulatorProvider.ListLoadBalancers(ctx)
	if len(loadBalancerList) != 1 {
		testing.Fatalf("expected 1 load balancer, got %d", len(loadBalancerList))
	}
	if loadBalancerList[0].ServiceName != "web" {
		testing.Errorf("expected service web, got %q", loadBalancerList[0].ServiceName)
	}
	if loadBalancerList[0].State != LoadBalancerStateActive {
		testing.Errorf("expected state active, got %q", loadBalancerList[0].State)
	}

	secondAddress, _ := simulatorProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{
		ServiceName: "web",
		Port:        80,
	})
	if secondAddress != externalAddress {
		testing.Errorf("idempotent EnsureLoadBalancer changed address from %q to %q", externalAddress, secondAddress)
	}

	deleteError := simulatorProvider.DeleteLoadBalancer(ctx, "web")
	if deleteError != nil {
		testing.Fatalf("DeleteLoadBalancer failed: %v", deleteError)
	}
	loadBalancerList, _ = simulatorProvider.ListLoadBalancers(ctx)
	if len(loadBalancerList) != 0 {
		testing.Errorf("expected 0 load balancers after delete, got %d", len(loadBalancerList))
	}
}

func TestSimulatorCloudProviderEnsureAndDeleteRoute(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	ensureError := simulatorProvider.EnsureRoute(ctx, RouteConfig{
		DestinationCIDR:  "10.244.1.0/24",
		TargetNodeID:     "node-1",
		TargetInstanceID: "i-abc123",
	})
	if ensureError != nil {
		testing.Fatalf("EnsureRoute failed: %v", ensureError)
	}

	routeList, _ := simulatorProvider.ListRoutes(ctx)
	if len(routeList) != 1 {
		testing.Fatalf("expected 1 route, got %d", len(routeList))
	}
	if routeList[0].DestinationCIDR != "10.244.1.0/24" {
		testing.Errorf("expected CIDR 10.244.1.0/24, got %q", routeList[0].DestinationCIDR)
	}

	deleteError := simulatorProvider.DeleteRoute(ctx, "10.244.1.0/24")
	if deleteError != nil {
		testing.Fatalf("DeleteRoute failed: %v", deleteError)
	}
	routeList, _ = simulatorProvider.ListRoutes(ctx)
	if len(routeList) != 0 {
		testing.Errorf("expected 0 routes after delete, got %d", len(routeList))
	}
}

func TestSimulatorCloudProviderCallTracking(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	simulatorProvider.CreateInstance(ctx, InstanceConfig{InstanceType: "t3.micro"})
	simulatorProvider.CreateInstance(ctx, InstanceConfig{InstanceType: "m5.large"})
	simulatorProvider.TerminateInstance(ctx, "sim-instance-1")
	simulatorProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{ServiceName: "api"})
	simulatorProvider.DeleteLoadBalancer(ctx, "api")
	simulatorProvider.EnsureRoute(ctx, RouteConfig{DestinationCIDR: "10.0.0.0/24"})
	simulatorProvider.DeleteRoute(ctx, "10.0.0.0/24")

	if len(simulatorProvider.CreateInstanceCalls) != 2 {
		testing.Errorf("expected 2 create calls, got %d", len(simulatorProvider.CreateInstanceCalls))
	}
	if len(simulatorProvider.TerminateCalls) != 1 {
		testing.Errorf("expected 1 terminate call, got %d", len(simulatorProvider.TerminateCalls))
	}
	if len(simulatorProvider.EnsureLoadBalancerCalls) != 1 {
		testing.Errorf("expected 1 ensure LB call, got %d", len(simulatorProvider.EnsureLoadBalancerCalls))
	}
	if len(simulatorProvider.DeleteLoadBalancerCalls) != 1 {
		testing.Errorf("expected 1 delete LB call, got %d", len(simulatorProvider.DeleteLoadBalancerCalls))
	}
	if len(simulatorProvider.EnsureRouteCalls) != 1 {
		testing.Errorf("expected 1 ensure route call, got %d", len(simulatorProvider.EnsureRouteCalls))
	}
	if len(simulatorProvider.DeleteRouteCalls) != 1 {
		testing.Errorf("expected 1 delete route call, got %d", len(simulatorProvider.DeleteRouteCalls))
	}
}

func TestSimulatorCloudProviderSetNodeID(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	instanceID, _ := simulatorProvider.CreateInstance(ctx, InstanceConfig{})
	simulatorProvider.SetInstanceNodeID(instanceID, "ccattler-node-1")

	instanceList, _ := simulatorProvider.ListInstances(ctx)
	if instanceList[0].NodeID != "ccattler-node-1" {
		testing.Errorf("expected node ID ccattler-node-1, got %q", instanceList[0].NodeID)
	}
}

func TestAWSProviderStubReturnsErrors(testing *testing.T) {
	awsProvider := NewAWSCloudProvider("us-east-1")
	ctx := context.Background()

	if awsProvider.ProviderName() != "aws" {
		testing.Errorf("expected provider name aws, got %q", awsProvider.ProviderName())
	}

	_, listError := awsProvider.ListInstances(ctx)
	if listError == nil {
		testing.Error("expected error from stub ListInstances")
	}
	_, createError := awsProvider.CreateInstance(ctx, InstanceConfig{})
	if createError == nil {
		testing.Error("expected error from stub CreateInstance")
	}
	if terminateError := awsProvider.TerminateInstance(ctx, "i-123"); terminateError == nil {
		testing.Error("expected error from stub TerminateInstance")
	}
}

func TestGCPProviderStubReturnsErrors(testing *testing.T) {
	gcpProvider := NewGCPCloudProvider("my-project", "us-central1")
	if gcpProvider.ProviderName() != "gcp" {
		testing.Errorf("expected provider name gcp, got %q", gcpProvider.ProviderName())
	}
}

func TestAzureProviderStubReturnsErrors(testing *testing.T) {
	azureProvider := NewAzureCloudProvider("sub-123", "my-rg", "eastus")
	if azureProvider.ProviderName() != "azure" {
		testing.Errorf("expected provider name azure, got %q", azureProvider.ProviderName())
	}
}

func TestSimulatorTerminateNonexistentInstanceNoError(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	terminateError := simulatorProvider.TerminateInstance(context.Background(), "nonexistent-id")
	if terminateError != nil {
		testing.Errorf("terminate nonexistent should not error (idempotent), got: %v", terminateError)
	}
	if len(simulatorProvider.TerminateCalls) != 1 {
		testing.Errorf("expected 1 terminate call recorded, got %d", len(simulatorProvider.TerminateCalls))
	}
}

func TestSimulatorLoadBalancerUpdatesBackends(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	simulatorProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{
		ServiceName: "api",
		Port:        443,
		Protocol:    "https",
		Backends: []LoadBalancerBackend{
			{NodeID: "node-1", Address: "10.0.1.5", Port: 9090},
		},
	})

	simulatorProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{
		ServiceName: "api",
		Port:        443,
		Protocol:    "https",
		Backends: []LoadBalancerBackend{
			{NodeID: "node-1", Address: "10.0.1.5", Port: 9090},
			{NodeID: "node-2", Address: "10.0.2.5", Port: 9090},
		},
	})

	loadBalancerList, _ := simulatorProvider.ListLoadBalancers(ctx)
	if len(loadBalancerList) != 1 {
		testing.Fatalf("expected 1 load balancer, got %d", len(loadBalancerList))
	}
}

func TestSimulatorRouteIdempotency(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	ctx := context.Background()

	routeConfig := RouteConfig{
		DestinationCIDR:  "10.244.2.0/24",
		TargetNodeID:     "node-2",
		TargetInstanceID: "i-def456",
	}
	simulatorProvider.EnsureRoute(ctx, routeConfig)
	simulatorProvider.EnsureRoute(ctx, routeConfig)

	routeList, _ := simulatorProvider.ListRoutes(ctx)
	if len(routeList) != 1 {
		testing.Errorf("expected 1 route after idempotent ensure, got %d", len(routeList))
	}
}

func TestSimulatorDeleteNonexistentLoadBalancer(testing *testing.T) {
	simulatorProvider := NewSimulatorCloudProvider()
	deleteError := simulatorProvider.DeleteLoadBalancer(context.Background(), "nonexistent")
	if deleteError != nil {
		testing.Errorf("deleting nonexistent LB should not error, got: %v", deleteError)
	}
}

func TestGCPProviderAllStubMethodsReturnErrors(testing *testing.T) {
	gcpProvider := NewGCPCloudProvider("my-project", "us-central1")
	ctx := context.Background()

	_, listError := gcpProvider.ListInstances(ctx)
	if listError == nil {
		testing.Error("expected error from stub ListInstances")
	}
	_, createError := gcpProvider.CreateInstance(ctx, InstanceConfig{})
	if createError == nil {
		testing.Error("expected error from stub CreateInstance")
	}
	if terminateError := gcpProvider.TerminateInstance(ctx, "gce-123"); terminateError == nil {
		testing.Error("expected error from stub TerminateInstance")
	}
	if _, ensureError := gcpProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{}); ensureError == nil {
		testing.Error("expected error from stub EnsureLoadBalancer")
	}
}

func TestAzureProviderAllStubMethodsReturnErrors(testing *testing.T) {
	azureProvider := NewAzureCloudProvider("sub-123", "my-rg", "eastus")
	ctx := context.Background()

	_, listError := azureProvider.ListInstances(ctx)
	if listError == nil {
		testing.Error("expected error from stub ListInstances")
	}
	_, createError := azureProvider.CreateInstance(ctx, InstanceConfig{})
	if createError == nil {
		testing.Error("expected error from stub CreateInstance")
	}
	if terminateError := azureProvider.TerminateInstance(ctx, "vm-123"); terminateError == nil {
		testing.Error("expected error from stub TerminateInstance")
	}
	if _, ensureError := azureProvider.EnsureLoadBalancer(ctx, LoadBalancerConfig{}); ensureError == nil {
		testing.Error("expected error from stub EnsureLoadBalancer")
	}
}
