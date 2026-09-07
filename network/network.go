// Package network defines the pluggable networking subsystem for CCattler.
// It provides an abstraction layer between the control plane and the
// underlying packet implementation, covering addressing, service discovery,
// and traffic routing. Implementations range from a pure-simulation allocator
// (for testing) to Linux namespace/veth/bridge setups (for development) to
// Cilium/Calico (for production).
package network

import "context"

// DefaultClusterCIDR is the default IP range from which per-node subnets are
// carved. Each node receives a /24 within this /16 block.
const DefaultClusterCIDR = "10.100.0.0/16"

// DefaultVIPCIDR is the default IP range for service virtual IPs. The
// NetworkController allocates one VIP per service from this pool.
const DefaultVIPCIDR = "10.200.0.0/24"

// NetworkProvider is the pluggable interface for instance IP management.
// The agent calls AllocateIP when starting an instance and ReleaseIP when
// stopping one. The provider decides how addresses are assigned — from a
// simulated in-memory pool, a Linux bridge, or a production CNI plugin.
type NetworkProvider interface {
	// AllocateIP assigns an IP address to the given instance on the specified
	// node. Returns the same IP if the instance was already allocated
	// (idempotent). The caller writes the returned IP to the fact store.
	AllocateIP(ctx context.Context, nodeID string, instanceID string) (string, error)

	// ReleaseIP returns the given instance's IP to the available pool on the
	// specified node. Called when an instance is stopped or moved. Releasing
	// an unallocated instance is a no-op.
	ReleaseIP(ctx context.Context, nodeID string, instanceID string) error

	// NodeSubnet returns the CIDR subnet string assigned to the given node
	// (e.g. "10.100.1.0/24"). The subnet is assigned on first use and remains
	// stable for the lifetime of the provider.
	NodeSubnet(nodeID string) string
}
