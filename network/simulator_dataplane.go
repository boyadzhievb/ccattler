// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"sync"
)

// SimulatorDataPlane is a fake data plane provider for testing and semantic
// validation. It records every reconciliation call for later inspection by
// tests but does not program any real forwarding rules. This allows the
// full VIP and route data plane pipeline to be exercised alongside SimulatorRuntime.
type SimulatorDataPlane struct {
	// mutex guards concurrent access to the recorded state.
	mutex sync.Mutex
	// lastReconciled holds the most recent set of service VIP configurations
	// passed to ReconcileVIPDataPlane.
	lastReconciled []ServiceVIPConfig
	// lastReconciledRoutes holds the most recent set of route configurations
	// passed to ReconcileRouteDataPlane.
	lastReconciledRoutes []RouteDataPlaneConfig
	// reconcileCallCount tracks how many times ReconcileVIPDataPlane was called.
	reconcileCallCount int
	// routeReconcileCallCount tracks how many times ReconcileRouteDataPlane was called.
	routeReconcileCallCount int
	// cleanedUp is set to true when Cleanup is called.
	cleanedUp bool
}

// NewSimulatorDataPlane creates a SimulatorDataPlane that records reconciliation
// calls without programming any real iptables rules or network interfaces.
func NewSimulatorDataPlane() *SimulatorDataPlane {
	return &SimulatorDataPlane{}
}

// ReconcileVIPDataPlane records the service VIP configurations for later
// inspection. No real forwarding rules are created.
func (simulatorDataPlane *SimulatorDataPlane) ReconcileVIPDataPlane(_ context.Context, serviceConfigs []ServiceVIPConfig) error {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()

	configsCopy := make([]ServiceVIPConfig, len(serviceConfigs))
	copy(configsCopy, serviceConfigs)
	simulatorDataPlane.lastReconciled = configsCopy
	simulatorDataPlane.reconcileCallCount++
	return nil
}

// ReconcileRouteDataPlane records the route configurations for later
// inspection. No real forwarding rules or proxies are created.
func (simulatorDataPlane *SimulatorDataPlane) ReconcileRouteDataPlane(_ context.Context, routeConfigs []RouteDataPlaneConfig) error {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()

	configsCopy := make([]RouteDataPlaneConfig, len(routeConfigs))
	copy(configsCopy, routeConfigs)
	simulatorDataPlane.lastReconciledRoutes = configsCopy
	simulatorDataPlane.routeReconcileCallCount++
	return nil
}

// Cleanup marks the simulator as cleaned up. No real resources to release.
func (simulatorDataPlane *SimulatorDataPlane) Cleanup(_ context.Context) error {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()

	simulatorDataPlane.cleanedUp = true
	return nil
}

// LastReconciled returns a copy of the most recent service VIP configurations
// passed to ReconcileVIPDataPlane. Returns nil if never called.
func (simulatorDataPlane *SimulatorDataPlane) LastReconciled() []ServiceVIPConfig {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()

	if simulatorDataPlane.lastReconciled == nil {
		return nil
	}
	configsCopy := make([]ServiceVIPConfig, len(simulatorDataPlane.lastReconciled))
	copy(configsCopy, simulatorDataPlane.lastReconciled)
	return configsCopy
}

// ReconcileCallCount returns the number of times ReconcileVIPDataPlane was called.
func (simulatorDataPlane *SimulatorDataPlane) ReconcileCallCount() int {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()
	return simulatorDataPlane.reconcileCallCount
}

// LastReconciledRoutes returns a copy of the most recent route configurations
// passed to ReconcileRouteDataPlane. Returns nil if never called.
func (simulatorDataPlane *SimulatorDataPlane) LastReconciledRoutes() []RouteDataPlaneConfig {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()

	if simulatorDataPlane.lastReconciledRoutes == nil {
		return nil
	}
	configsCopy := make([]RouteDataPlaneConfig, len(simulatorDataPlane.lastReconciledRoutes))
	copy(configsCopy, simulatorDataPlane.lastReconciledRoutes)
	return configsCopy
}

// RouteReconcileCallCount returns the number of times ReconcileRouteDataPlane was called.
func (simulatorDataPlane *SimulatorDataPlane) RouteReconcileCallCount() int {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()
	return simulatorDataPlane.routeReconcileCallCount
}

// IsCleanedUp returns true if Cleanup has been called.
func (simulatorDataPlane *SimulatorDataPlane) IsCleanedUp() bool {
	simulatorDataPlane.mutex.Lock()
	defer simulatorDataPlane.mutex.Unlock()
	return simulatorDataPlane.cleanedUp
}
