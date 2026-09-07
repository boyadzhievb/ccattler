package network

import (
	"context"
	"fmt"
	"sync"
)

// SimulatorNetworkProvider is a fake network provider for testing and semantic
// validation. It allocates IP addresses from per-node /24 subnets carved out
// of a cluster-wide /16 CIDR block, without creating any real network
// interfaces or namespaces. This allows the full networking pipeline to be
// exercised cheaply alongside SimulatorRuntime.
type SimulatorNetworkProvider struct {
	// clusterBaseOctetOne is the first octet of the cluster CIDR (e.g. 10).
	clusterBaseOctetOne int
	// clusterBaseOctetTwo is the second octet of the cluster CIDR (e.g. 100).
	clusterBaseOctetTwo int
	// mutex guards all mutable state below against concurrent access.
	mutex sync.Mutex
	// nodeSubnetOffset maps each node ID to its assigned third-octet value.
	// Node "node-1" might map to 1, giving it subnet 10.100.1.0/24.
	nodeSubnetOffset map[string]int
	// nextSubnetOffset is the next third-octet value to assign to a new node.
	nextSubnetOffset int
	// instanceAllocations maps each instance ID to its allocated IP address.
	instanceAllocations map[string]string
	// nodeNextHostAddress tracks the next available host address (fourth octet)
	// for each node. Starts at 2 because .1 is reserved for the node gateway.
	nodeNextHostAddress map[string]int
}

// NewSimulatorNetworkProvider creates a SimulatorNetworkProvider that allocates
// IPs from per-node /24 subnets within the default cluster CIDR (10.100.0.0/16).
func NewSimulatorNetworkProvider() *SimulatorNetworkProvider {
	return NewSimulatorNetworkProviderWithCIDR(10, 100)
}

// NewSimulatorNetworkProviderWithCIDR creates a SimulatorNetworkProvider using
// the given first and second octets as the base of the cluster CIDR. For
// example, NewSimulatorNetworkProviderWithCIDR(10, 100) allocates from
// 10.100.x.0/24 subnets.
func NewSimulatorNetworkProviderWithCIDR(firstOctet int, secondOctet int) *SimulatorNetworkProvider {
	return &SimulatorNetworkProvider{
		clusterBaseOctetOne: firstOctet,
		clusterBaseOctetTwo: secondOctet,
		nodeSubnetOffset:    make(map[string]int),
		nextSubnetOffset:    1,
		instanceAllocations: make(map[string]string),
		nodeNextHostAddress: make(map[string]int),
	}
}

// AllocateIP assigns an IP address to the given instance on the specified node.
// If the instance already has an allocation, the same IP is returned (idempotent).
// IPs are drawn sequentially from the node's /24 subnet, starting at .2.
func (simulatorNetworkProvider *SimulatorNetworkProvider) AllocateIP(_ context.Context, nodeID string, instanceID string) (string, error) {
	simulatorNetworkProvider.mutex.Lock()
	defer simulatorNetworkProvider.mutex.Unlock()

	// Return existing allocation if instance was already assigned an IP.
	if existingIP, alreadyAllocated := simulatorNetworkProvider.instanceAllocations[instanceID]; alreadyAllocated {
		return existingIP, nil
	}

	// Ensure the node has a subnet assigned.
	simulatorNetworkProvider.ensureNodeSubnetAssigned(nodeID)

	thirdOctet := simulatorNetworkProvider.nodeSubnetOffset[nodeID]
	fourthOctet := simulatorNetworkProvider.nodeNextHostAddress[nodeID]

	if fourthOctet > 254 {
		return "", fmt.Errorf("IP pool exhausted for node %s: all 253 addresses in subnet %s are allocated",
			nodeID, simulatorNetworkProvider.formatSubnet(thirdOctet))
	}

	allocatedIP := fmt.Sprintf("%d.%d.%d.%d",
		simulatorNetworkProvider.clusterBaseOctetOne,
		simulatorNetworkProvider.clusterBaseOctetTwo,
		thirdOctet,
		fourthOctet,
	)

	simulatorNetworkProvider.instanceAllocations[instanceID] = allocatedIP
	simulatorNetworkProvider.nodeNextHostAddress[nodeID] = fourthOctet + 1

	return allocatedIP, nil
}

// ReleaseIP returns the given instance's IP to the available pool. Releasing
// an instance that has no allocation is a no-op.
func (simulatorNetworkProvider *SimulatorNetworkProvider) ReleaseIP(_ context.Context, _ string, instanceID string) error {
	simulatorNetworkProvider.mutex.Lock()
	defer simulatorNetworkProvider.mutex.Unlock()

	delete(simulatorNetworkProvider.instanceAllocations, instanceID)
	return nil
}

// NodeSubnet returns the CIDR subnet string assigned to the given node. If the
// node has not yet been assigned a subnet, one is allocated automatically.
func (simulatorNetworkProvider *SimulatorNetworkProvider) NodeSubnet(nodeID string) string {
	simulatorNetworkProvider.mutex.Lock()
	defer simulatorNetworkProvider.mutex.Unlock()

	simulatorNetworkProvider.ensureNodeSubnetAssigned(nodeID)
	thirdOctet := simulatorNetworkProvider.nodeSubnetOffset[nodeID]
	return simulatorNetworkProvider.formatSubnet(thirdOctet)
}

// ensureNodeSubnetAssigned guarantees the node has a subnet offset and a host
// address counter. Must be called while holding the mutex.
func (simulatorNetworkProvider *SimulatorNetworkProvider) ensureNodeSubnetAssigned(nodeID string) {
	if _, hasSubnet := simulatorNetworkProvider.nodeSubnetOffset[nodeID]; !hasSubnet {
		simulatorNetworkProvider.nodeSubnetOffset[nodeID] = simulatorNetworkProvider.nextSubnetOffset
		simulatorNetworkProvider.nodeNextHostAddress[nodeID] = 2
		simulatorNetworkProvider.nextSubnetOffset++
	}
}

// formatSubnet returns the CIDR notation for a /24 subnet given its third octet.
func (simulatorNetworkProvider *SimulatorNetworkProvider) formatSubnet(thirdOctet int) string {
	return fmt.Sprintf("%d.%d.%d.0/24",
		simulatorNetworkProvider.clusterBaseOctetOne,
		simulatorNetworkProvider.clusterBaseOctetTwo,
		thirdOctet,
	)
}
