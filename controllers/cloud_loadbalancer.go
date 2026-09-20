package controllers

import (
	"context"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// CloudLoadBalancerController watches services with "expose external" ports
// and manages cloud load balancers for them. It creates load balancers for
// newly externally-exposed services, updates backend lists when endpoints
// change, and deletes load balancers when services are removed or no longer
// externally exposed.
type CloudLoadBalancerController struct {
	cloudProvider cloud.CloudProvider
}

// NewCloudLoadBalancerController creates a CloudLoadBalancerController with the
// given cloud provider.
func NewCloudLoadBalancerController(cloudProvider cloud.CloudProvider) *CloudLoadBalancerController {
	return &CloudLoadBalancerController{cloudProvider: cloudProvider}
}

// Name returns "cloud-loadbalancer".
func (loadBalancerController *CloudLoadBalancerController) Name() string {
	return "cloud-loadbalancer"
}

// Watch returns the fact prefixes needed to reconcile cloud load balancers:
// desired service config (for expose external), endpoints (for backends),
// and observed node addresses (for backend IPs).
func (loadBalancerController *CloudLoadBalancerController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanEndpoints,
		types.ScanObservedNodes,
		types.ScanObservedCloudLoadBalancers,
	}
}

// Reconcile compares desired external services against existing cloud load
// balancers and creates, updates, or deletes them as needed.
func (loadBalancerController *CloudLoadBalancerController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	externalServices := extractExternalServicePorts(facts)
	serviceEndpoints := extractServiceEndpointBackends(facts)
	nodeAddresses := extractNodeAddressesFromFacts(facts)
	existingLoadBalancers := extractExistingLoadBalancers(facts)

	var proposedChanges []Change

	for serviceName, externalConfig := range externalServices {
		backends := buildLoadBalancerBackends(serviceEndpoints[serviceName], nodeAddresses)
		loadBalancerConfig := cloud.LoadBalancerConfig{
			ServiceName: serviceName,
			Port:        externalConfig.port,
			TargetPort:  externalConfig.port,
			Protocol:    externalConfig.protocol,
			Backends:    backends,
		}

		externalAddress, ensureError := loadBalancerController.cloudProvider.EnsureLoadBalancer(ctx, loadBalancerConfig)
		if ensureError != nil {
			continue
		}

		previousAddress := existingLoadBalancers[serviceName]
		if previousAddress != externalAddress {
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedCloudLoadBalancerAddress(serviceName),
				Value: []byte(externalAddress),
			})
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedCloudLoadBalancerState(serviceName),
				Value: []byte(string(cloud.LoadBalancerStateActive)),
			})
		}
	}

	for serviceName := range existingLoadBalancers {
		if _, stillExternal := externalServices[serviceName]; !stillExternal {
			deleteError := loadBalancerController.cloudProvider.DeleteLoadBalancer(ctx, serviceName)
			if deleteError != nil {
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedCloudLoadBalancerAddress(serviceName),
			})
			proposedChanges = append(proposedChanges, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedCloudLoadBalancerState(serviceName),
			})
		}
	}

	return proposedChanges, nil
}

// externalServiceConfig holds the parsed external exposure config for a service.
type externalServiceConfig struct {
	port     int
	protocol string
}

// extractExternalServicePorts builds a map of service names to their external
// port configuration from desired facts.
func extractExternalServicePorts(facts []store.Fact) map[string]externalServiceConfig {
	externalServices := make(map[string]externalServiceConfig)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) >= 4 && pathParts[1] == "expose" && pathParts[3] == "external" {
			serviceName := pathParts[0]
			port, parseError := strconv.Atoi(pathParts[2])
			if parseError != nil {
				continue
			}
			protocol := string(factEntry.Value)
			if protocol == "" {
				protocol = "tcp"
			}
			externalServices[serviceName] = externalServiceConfig{
				port:     port,
				protocol: protocol,
			}
		}
	}
	return externalServices
}

// endpointBackend holds the parsed address and port from an endpoint fact.
type endpointBackend struct {
	address string
	port    int
	nodeID  string
}

// extractServiceEndpointBackends parses endpoint facts into a map of service
// name to backend list.
func extractServiceEndpointBackends(facts []store.Fact) map[string][]endpointBackend {
	serviceEndpoints := make(map[string][]endpointBackend)

	instanceNodes := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedInstances) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "node" {
			instanceNodes[pathParts[0]] = string(factEntry.Value)
		}
	}

	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanEndpoints) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanEndpoints)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		serviceName := pathParts[0]
		instanceID := pathParts[1]

		addressPort := string(factEntry.Value)
		colonIndex := strings.LastIndex(addressPort, ":")
		if colonIndex < 0 {
			continue
		}
		address := addressPort[:colonIndex]
		port, parseError := strconv.Atoi(addressPort[colonIndex+1:])
		if parseError != nil {
			continue
		}

		serviceEndpoints[serviceName] = append(serviceEndpoints[serviceName], endpointBackend{
			address: address,
			port:    port,
			nodeID:  instanceNodes[instanceID],
		})
	}
	return serviceEndpoints
}

// extractNodeAddressesFromFacts builds a map of node ID to advertised address.
func extractNodeAddressesFromFacts(facts []store.Fact) map[string]string {
	nodeAddresses := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedNodes) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedNodes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "address" {
			nodeAddresses[pathParts[0]] = string(factEntry.Value)
		}
	}
	return nodeAddresses
}

// buildLoadBalancerBackends converts endpoint backends to cloud load balancer
// backend targets, using node addresses where available.
func buildLoadBalancerBackends(endpoints []endpointBackend, nodeAddresses map[string]string) []cloud.LoadBalancerBackend {
	var backends []cloud.LoadBalancerBackend
	for _, endpoint := range endpoints {
		backendAddress := endpoint.address
		if endpoint.nodeID != "" {
			if nodeAddress, exists := nodeAddresses[endpoint.nodeID]; exists {
				backendAddress = nodeAddress
			}
		}
		backends = append(backends, cloud.LoadBalancerBackend{
			NodeID:  endpoint.nodeID,
			Address: backendAddress,
			Port:    endpoint.port,
		})
	}
	return backends
}

// extractExistingLoadBalancers returns a map of service name to external
// address for all load balancers currently tracked in the store.
func extractExistingLoadBalancers(facts []store.Fact) map[string]string {
	loadBalancers := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedCloudLoadBalancers) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedCloudLoadBalancers)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "address" {
			loadBalancers[pathParts[0]] = string(factEntry.Value)
		}
	}
	return loadBalancers
}
