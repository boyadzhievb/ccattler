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
// nftables/iptables DNAT rules. Cleanup removes Cilium kvstore keys and
// revokes the active lease.
type CiliumDataPlane struct {
	// factTranslator translates CCattler facts to Cilium kvstore entries.
	factTranslator *CiliumFactTranslator
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
		factTranslator: NewCiliumFactTranslator(factStore, clusterName, nodeID),
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

	cleanupError := ciliumDataPlane.factTranslator.CleanupAll(ctx)
	if cleanupError != nil {
		return fmt.Errorf("cilium kvstore cleanup failed: %w", cleanupError)
	}

	return nil
}
