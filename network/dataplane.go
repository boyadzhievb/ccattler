// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package network — data plane provider interface for VIP-based load balancing.
//
// The DataPlaneProvider programs per-node packet forwarding rules that
// translate service VIP traffic into DNAT to real backend endpoints.
// This is the mechanism that makes VIPs routable — without a data plane,
// VIPs are facts only with no packet-level effect.
package network

import "context"

// DataPlaneProvider programs per-node forwarding rules for service VIPs
// and route-based traffic distribution. Each worker node runs a data plane
// provider that translates VIP:port traffic into DNAT to backend endpoints
// and applies route-level policies (sticky sessions). Implementations
// range from a no-op simulator (for tests) to iptables DNAT rules
// (for real Linux hosts, analogous to kube-proxy iptables mode).
type DataPlaneProvider interface {
	// ReconcileVIPDataPlane computes the desired forwarding state from the
	// given service VIP configurations and applies any necessary changes.
	// Called on every agent reconciliation cycle. Implementations must be
	// idempotent — calling with the same input twice produces no changes.
	ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error

	// ReconcileRouteDataPlane programs forwarding rules for route-based
	// traffic distribution with optional sticky session affinity. Called
	// on every agent reconciliation cycle alongside VIP reconciliation.
	ReconcileRouteDataPlane(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error

	// Cleanup removes all data plane state managed by this provider:
	// iptables chains/rules, dummy interfaces, VIP addresses, route
	// proxy processes. Called on agent shutdown for clean teardown.
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
	// Protocol is the IP protocol: "tcp" or "udp". Defaults to "tcp".
	Protocol string
	// Backends is the set of real endpoints to distribute traffic across.
	// Order does not matter — the data plane implementation chooses the
	// load balancing strategy (round-robin via iptables statistic module).
	Backends []DataPlaneBackend
}

// EffectiveProtocol returns the protocol for this config, defaulting to
// "tcp" when unset. This preserves backward compatibility with callers
// that do not populate the Protocol field.
func (serviceVIPConfig ServiceVIPConfig) EffectiveProtocol() string {
	if serviceVIPConfig.Protocol == "" {
		return "tcp"
	}
	return serviceVIPConfig.Protocol
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

// RouteDataPlaneConfig describes the forwarding configuration for a single
// route: which service VIP to intercept and how to distribute traffic
// across backends, optionally with sticky session affinity.
type RouteDataPlaneConfig struct {
	// RouteName is the CCattler route name (e.g. "web-public").
	RouteName string
	// ServiceName is the target service name (e.g. "web").
	ServiceName string
	// VirtualIP is the service's VIP address used as the DNAT entry point.
	VirtualIP string
	// Port is the service port on the VIP.
	Port int
	// Backends is the set of resolved backend endpoints.
	Backends []DataPlaneBackend
	// StickyMethod is the affinity method: "" (none), "source-ip", or "cookie".
	StickyMethod string
	// CookieConfig holds cookie-specific settings when StickyMethod is "cookie".
	CookieConfig *RouteCookieConfig
}

// DataPlaneInitializer can be implemented by data plane providers that require
// one-time initialization before the first reconciliation cycle (e.g. scanning
// existing state, creating leases).
type DataPlaneInitializer interface {
	Initialize(ctx context.Context) error
}

// DataPlaneNodeRegistrar can be implemented by data plane providers that need
// to register node metadata on agent startup (e.g. eBPF bridge discovery).
type DataPlaneNodeRegistrar interface {
	RegisterNode(ctx context.Context, nodeIP string, podCIDR string) error
}

// EndpointIdentityRegistrar can be implemented by data plane providers that
// need to register container IP-to-identity mappings when instances start.
type EndpointIdentityRegistrar interface {
	RegisterEndpointIdentity(ctx context.Context, containerIP string, hostIP string, serviceName string, containerPort int) error
}

// DataPlanePolicyReconciler can be implemented by data plane providers that
// translate compiled network policy rules (derived/network/rule/) into their
// native enforcement format.
type DataPlanePolicyReconciler interface {
	ReconcilePolicies(ctx context.Context) error
}

// RouteCookieConfig holds the cookie attributes for cookie-based sticky sessions.
type RouteCookieConfig struct {
	// Name is the cookie name (e.g. "session" or "__CCA_STICKY_web").
	Name string
	// Secure sets the Secure flag on the cookie.
	Secure bool
	// HTTPOnly sets the HttpOnly flag on the cookie.
	HTTPOnly bool
	// SameSite is the SameSite attribute: "strict", "lax", or "none".
	SameSite string
}
