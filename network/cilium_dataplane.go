// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build cilium

package network

import (
	"context"
	"fmt"
	"sync"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

// CiliumDataPlane implements DataPlaneProvider by translating CCattler VIP and
// route configurations into Cilium kvstore entries. Cilium agents watch these
// keys and program eBPF maps for socket-level load balancing, replacing
// nftables/iptables DNAT rules. Also implements DataPlaneInitializer,
// DataPlaneNodeRegistrar, EndpointIdentityRegistrar, and
// DataPlanePolicyReconciler for full Cilium integration.
type CiliumDataPlane struct {
	// factTranslator translates CCattler facts to Cilium kvstore entries.
	factTranslator *CiliumFactTranslator
	// policyTranslator translates compiled network rules to Cilium policy entries.
	policyTranslator *CiliumPolicyTranslator
	// mutex serializes reconciliation calls.
	mutex sync.Mutex
	// initialized tracks whether Initialize has been called.
	initialized bool
}

// NewCiliumDataPlane creates a CiliumDataPlane that writes Cilium-compatible
// kvstore entries to the given fact store. The clusterName identifies the
// cluster in Cilium's kvstore key paths. The nodeID identifies this node
// for slave key writes and node registration.
func NewCiliumDataPlane(factStore store.StateStore, clusterName string, nodeID string) *CiliumDataPlane {
	return &CiliumDataPlane{
		factTranslator:   NewCiliumFactTranslator(factStore, clusterName, nodeID),
		policyTranslator: NewCiliumPolicyTranslator(factStore, clusterName),
	}
}

// Initialize scans etcd for existing identity allocations, creates a lease
// for TTL-bound keys, and starts the lease renewal goroutine. Must be called
// before the first ReconcileVIPDataPlane call.
func (ciliumDataPlane *CiliumDataPlane) Initialize(ctx context.Context) error {
	return ciliumDataPlane.factTranslator.Initialize(ctx)
}

// FactTranslator returns the underlying translator for direct endpoint
// registration by the agent reconciliation loop.
func (ciliumDataPlane *CiliumDataPlane) FactTranslator() *CiliumFactTranslator {
	return ciliumDataPlane.factTranslator
}

// ReconcileVIPDataPlane translates CCattler service VIP configurations into
// Cilium kvstore service and backend entries. Cilium agents watch these keys
// and program eBPF maps for socket-level load balancing.
func (ciliumDataPlane *CiliumDataPlane) ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	ciliumDataPlane.mutex.Lock()
	defer ciliumDataPlane.mutex.Unlock()

	if !ciliumDataPlane.initialized {
		logging.Default().Info("cilium data plane: first reconciliation cycle")
		ciliumDataPlane.initialized = true
	}

	translationError := ciliumDataPlane.factTranslator.ReconcileServices(ctx, serviceConfigs)
	if translationError != nil {
		return fmt.Errorf("cilium service translation failed: %w", translationError)
	}

	return nil
}

// ReconcileRouteDataPlane translates CCattler route configurations into Cilium
// kvstore entries. For sticky sessions, Cilium uses native eBPF session
// affinity via socket cookies.
func (ciliumDataPlane *CiliumDataPlane) ReconcileRouteDataPlane(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error {
	ciliumDataPlane.mutex.Lock()
	defer ciliumDataPlane.mutex.Unlock()

	translationError := ciliumDataPlane.factTranslator.ReconcileRoutes(ctx, routeConfigs)
	if translationError != nil {
		return fmt.Errorf("cilium route translation failed: %w", translationError)
	}

	return nil
}

// Cleanup removes all Cilium kvstore keys written by this provider, stops the
// lease renewal goroutine, and revokes the active lease.
func (ciliumDataPlane *CiliumDataPlane) Cleanup(ctx context.Context) error {
	ciliumDataPlane.mutex.Lock()
	defer ciliumDataPlane.mutex.Unlock()

	logging.Default().Info("cilium data plane: cleaning up kvstore entries")

	policyError := ciliumDataPlane.policyTranslator.CleanupPolicies(ctx)
	if policyError != nil {
		logging.Default().Error("cilium policy cleanup failed", "error", policyError.Error())
	}

	cleanupError := ciliumDataPlane.factTranslator.CleanupAll(ctx)
	if cleanupError != nil {
		return fmt.Errorf("cilium kvstore cleanup failed: %w", cleanupError)
	}

	return nil
}

// RegisterNode writes a Cilium node registration entry so other Cilium agents
// can discover this node for tunnel setup and health checking.
func (ciliumDataPlane *CiliumDataPlane) RegisterNode(ctx context.Context, nodeIP string, podCIDR string) error {
	return ciliumDataPlane.factTranslator.WriteNodeRegistration(ctx, nodeIP, podCIDR)
}

// RegisterEndpointIdentity writes a Cilium IP-to-identity mapping and the
// corresponding identity master and slave keys for a newly started container.
func (ciliumDataPlane *CiliumDataPlane) RegisterEndpointIdentity(ctx context.Context, containerIP string, hostIP string, serviceName string, containerPort int) error {
	return ciliumDataPlane.factTranslator.WriteEndpointIPIdentity(ctx, containerIP, hostIP, serviceName, containerPort)
}

// ReconcilePolicies reads compiled network policy rules from the fact store
// and translates them into Cilium identity-based policy entries. Uses the
// translator's identity cache to resolve IPs to Cilium security identities.
func (ciliumDataPlane *CiliumDataPlane) ReconcilePolicies(ctx context.Context) error {
	ciliumDataPlane.mutex.Lock()
	defer ciliumDataPlane.mutex.Unlock()

	identityResolver := ciliumDataPlane.factTranslator.ResolveIPToIdentity
	return ciliumDataPlane.policyTranslator.ReconcilePolicies(ctx, identityResolver)
}
