package cloud

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// SimulatorCloudProvider is an in-memory cloud provider for testing. It tracks
// instances, load balancers, and routes without making real cloud API calls.
// All operations are recorded and can be inspected in tests.
type SimulatorCloudProvider struct {
	instanceCounter  atomic.Int64
	instances        map[string]*CloudInstance
	loadBalancers    map[string]*LoadBalancerStatus
	routes           map[string]*RouteEntry
	providerMutex    sync.Mutex

	// CreateInstanceCalls records each CreateInstance invocation for test assertions.
	CreateInstanceCalls []InstanceConfig
	// TerminateCalls records each TerminateInstance invocation.
	TerminateCalls []string
	// EnsureLoadBalancerCalls records each EnsureLoadBalancer invocation.
	EnsureLoadBalancerCalls []LoadBalancerConfig
	// DeleteLoadBalancerCalls records each DeleteLoadBalancer invocation.
	DeleteLoadBalancerCalls []string
	// EnsureRouteCalls records each EnsureRoute invocation.
	EnsureRouteCalls []RouteConfig
	// DeleteRouteCalls records each DeleteRoute invocation.
	DeleteRouteCalls []string
}

// NewSimulatorCloudProvider creates a SimulatorCloudProvider with empty state.
func NewSimulatorCloudProvider() *SimulatorCloudProvider {
	return &SimulatorCloudProvider{
		instances:     make(map[string]*CloudInstance),
		loadBalancers: make(map[string]*LoadBalancerStatus),
		routes:        make(map[string]*RouteEntry),
	}
}

// ProviderName returns "simulator".
func (simulatorProvider *SimulatorCloudProvider) ProviderName() string {
	return "simulator"
}

// ListInstances returns all tracked cloud instances.
func (simulatorProvider *SimulatorCloudProvider) ListInstances(_ context.Context) ([]CloudInstance, error) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	instanceList := make([]CloudInstance, 0, len(simulatorProvider.instances))
	for _, cloudInstance := range simulatorProvider.instances {
		instanceList = append(instanceList, *cloudInstance)
	}
	return instanceList, nil
}

// CreateInstance adds a simulated instance and returns its generated ID.
func (simulatorProvider *SimulatorCloudProvider) CreateInstance(_ context.Context, config InstanceConfig) (string, error) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	instanceNumber := simulatorProvider.instanceCounter.Add(1)
	providerInstanceID := fmt.Sprintf("sim-instance-%d", instanceNumber)

	simulatorProvider.instances[providerInstanceID] = &CloudInstance{
		ProviderInstanceID: providerInstanceID,
		State:              InstanceStateRunning,
		Region:             config.Region,
		InstanceType:       config.InstanceType,
	}
	simulatorProvider.CreateInstanceCalls = append(simulatorProvider.CreateInstanceCalls, config)

	return providerInstanceID, nil
}

// TerminateInstance marks a simulated instance as terminated.
func (simulatorProvider *SimulatorCloudProvider) TerminateInstance(_ context.Context, providerInstanceID string) error {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	if cloudInstance, exists := simulatorProvider.instances[providerInstanceID]; exists {
		cloudInstance.State = InstanceStateTerminated
	}
	simulatorProvider.TerminateCalls = append(simulatorProvider.TerminateCalls, providerInstanceID)

	return nil
}

// EnsureLoadBalancer creates or updates a simulated load balancer and returns
// a synthetic external address.
func (simulatorProvider *SimulatorCloudProvider) EnsureLoadBalancer(_ context.Context, config LoadBalancerConfig) (string, error) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	externalAddress := fmt.Sprintf("203.0.113.%d", len(simulatorProvider.loadBalancers)+1)
	if existing, exists := simulatorProvider.loadBalancers[config.ServiceName]; exists {
		externalAddress = existing.ExternalAddress
	}

	simulatorProvider.loadBalancers[config.ServiceName] = &LoadBalancerStatus{
		ServiceName:     config.ServiceName,
		ExternalAddress: externalAddress,
		State:           LoadBalancerStateActive,
	}
	simulatorProvider.EnsureLoadBalancerCalls = append(simulatorProvider.EnsureLoadBalancerCalls, config)

	return externalAddress, nil
}

// DeleteLoadBalancer removes a simulated load balancer.
func (simulatorProvider *SimulatorCloudProvider) DeleteLoadBalancer(_ context.Context, serviceName string) error {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	delete(simulatorProvider.loadBalancers, serviceName)
	simulatorProvider.DeleteLoadBalancerCalls = append(simulatorProvider.DeleteLoadBalancerCalls, serviceName)

	return nil
}

// ListLoadBalancers returns all tracked load balancers.
func (simulatorProvider *SimulatorCloudProvider) ListLoadBalancers(_ context.Context) ([]LoadBalancerStatus, error) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	loadBalancerList := make([]LoadBalancerStatus, 0, len(simulatorProvider.loadBalancers))
	for _, loadBalancerStatus := range simulatorProvider.loadBalancers {
		loadBalancerList = append(loadBalancerList, *loadBalancerStatus)
	}
	return loadBalancerList, nil
}

// EnsureRoute creates or updates a simulated VPC route.
func (simulatorProvider *SimulatorCloudProvider) EnsureRoute(_ context.Context, route RouteConfig) error {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	simulatorProvider.routes[route.DestinationCIDR] = &RouteEntry{
		DestinationCIDR:  route.DestinationCIDR,
		TargetNodeID:     route.TargetNodeID,
		TargetInstanceID: route.TargetInstanceID,
	}
	simulatorProvider.EnsureRouteCalls = append(simulatorProvider.EnsureRouteCalls, route)

	return nil
}

// DeleteRoute removes a simulated VPC route.
func (simulatorProvider *SimulatorCloudProvider) DeleteRoute(_ context.Context, destinationCIDR string) error {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	delete(simulatorProvider.routes, destinationCIDR)
	simulatorProvider.DeleteRouteCalls = append(simulatorProvider.DeleteRouteCalls, destinationCIDR)

	return nil
}

// ListRoutes returns all tracked VPC routes.
func (simulatorProvider *SimulatorCloudProvider) ListRoutes(_ context.Context) ([]RouteEntry, error) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	routeList := make([]RouteEntry, 0, len(simulatorProvider.routes))
	for _, routeEntry := range simulatorProvider.routes {
		routeList = append(routeList, *routeEntry)
	}
	return routeList, nil
}

// SimulateInstanceTermination marks an instance as terminated, simulating a
// cloud-side termination that the node lifecycle controller should detect.
func (simulatorProvider *SimulatorCloudProvider) SimulateInstanceTermination(providerInstanceID string) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	if cloudInstance, exists := simulatorProvider.instances[providerInstanceID]; exists {
		cloudInstance.State = InstanceStateTerminated
	}
}

// SetInstanceNodeID associates a CCattler node ID with a cloud instance,
// simulating the mapping that happens when a node registers after provisioning.
func (simulatorProvider *SimulatorCloudProvider) SetInstanceNodeID(providerInstanceID string, nodeID string) {
	simulatorProvider.providerMutex.Lock()
	defer simulatorProvider.providerMutex.Unlock()

	if cloudInstance, exists := simulatorProvider.instances[providerInstanceID]; exists {
		cloudInstance.NodeID = nodeID
	}
}
