// Package network — data plane provider interface for VIP-based load balancing.
//
// The DataPlaneProvider programs per-node packet forwarding rules that
// translate service VIP traffic into DNAT to real backend endpoints.
// This is the mechanism that makes VIPs routable — without a data plane,
// VIPs are facts only with no packet-level effect.
package network

import "context"

// DataPlaneProvider programs per-node forwarding rules for service VIPs.
// Each worker node runs a data plane provider that translates VIP:port
// traffic into DNAT to the actual backend endpoints. Implementations
// range from a no-op simulator (for tests) to iptables DNAT rules
// (for real Linux hosts, analogous to kube-proxy iptables mode).
type DataPlaneProvider interface {
	// ReconcileVIPDataPlane computes the desired forwarding state from the
	// given service VIP configurations and applies any necessary changes.
	// Called on every agent reconciliation cycle. Implementations must be
	// idempotent — calling with the same input twice produces no changes.
	ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error

	// Cleanup removes all data plane state managed by this provider:
	// iptables chains/rules, dummy interfaces, VIP addresses. Called on
	// agent shutdown for clean teardown.
	Cleanup(ctx context.Context) error
}

// ServiceVIPConfig describes the complete forwarding configuration for
// one service VIP: the virtual address and port that clients connect to,
// and the set of backend endpoints that traffic is distributed across.
type ServiceVIPConfig struct {
	// ServiceName is the CCattler service name (e.g. "web").
	ServiceName string
	// VirtualIP is the service's VIP address (e.g. "10.200.0.1").
	VirtualIP string
	// Port is the port number on the VIP (e.g. 80).
	Port int
	// Backends is the set of real endpoints to distribute traffic across.
	// Order does not matter — the data plane implementation chooses the
	// load balancing strategy (round-robin via iptables statistic module).
	Backends []DataPlaneBackend
}

// DataPlaneBackend is a single real endpoint that a VIP can forward to.
// The address must be routable from the node where the data plane runs.
type DataPlaneBackend struct {
	// Address is the IP address of the backend (host IP for cross-host,
	// container IP for local containers on the same bridge).
	Address string
	// Port is the port number on the backend (host port for cross-host,
	// container port for local containers).
	Port int
}
