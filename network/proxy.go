package network

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/boyadzhievb/ccattler/types"
)

// ServiceProxy selects a backend endpoint for incoming traffic to a service.
// The SimulatorProxy records decisions as facts for test verification; a
// future UserSpaceProxy would forward real TCP connections.
type ServiceProxy interface {
	// RouteRequest selects a backend endpoint for the named service using
	// round-robin load balancing. Returns an error if the service has no
	// available endpoints.
	RouteRequest(ctx context.Context, serviceName string) (types.Endpoint, error)
}

// SimulatorProxy implements ServiceProxy using round-robin selection across
// endpoints read from the store via a ServiceResolver. It does not open
// real TCP connections — it just selects backends and records routing
// decisions, making it suitable for integration tests with SimulatorRuntime.
type SimulatorProxy struct {
	// resolver provides the current set of endpoints for each service.
	resolver ServiceResolver
	// roundRobinCounters tracks the next endpoint index per service for
	// round-robin distribution.
	roundRobinCounters map[string]*atomic.Uint64
	// countersMutex protects the roundRobinCounters map during creation
	// of new per-service counters.
	countersMutex sync.Mutex
	// routingDecisions records every routing decision made by the proxy:
	// service name -> list of selected endpoints. Protected by decisionsMutex.
	routingDecisions map[string][]types.Endpoint
	// decisionsMutex protects the routingDecisions map.
	decisionsMutex sync.Mutex
}

// NewSimulatorProxy creates a SimulatorProxy that reads endpoints from the
// provided resolver and distributes traffic using round-robin selection.
func NewSimulatorProxy(resolver ServiceResolver) *SimulatorProxy {
	return &SimulatorProxy{
		resolver:           resolver,
		roundRobinCounters: make(map[string]*atomic.Uint64),
		routingDecisions:   make(map[string][]types.Endpoint),
	}
}

// RouteRequest selects the next backend endpoint for the named service using
// round-robin. The selection is recorded in routingDecisions for later
// inspection by tests.
func (simulatorProxy *SimulatorProxy) RouteRequest(ctx context.Context, serviceName string) (types.Endpoint, error) {
	availableEndpoints, err := simulatorProxy.resolver.ResolveEndpoints(ctx, serviceName)
	if err != nil {
		return types.Endpoint{}, fmt.Errorf("resolving endpoints for %s: %w", serviceName, err)
	}
	if len(availableEndpoints) == 0 {
		return types.Endpoint{}, fmt.Errorf("no endpoints available for service %s", serviceName)
	}

	roundRobinCounter := simulatorProxy.getOrCreateCounter(serviceName)
	currentIndex := roundRobinCounter.Add(1) - 1
	selectedEndpoint := availableEndpoints[int(currentIndex)%len(availableEndpoints)]

	simulatorProxy.decisionsMutex.Lock()
	simulatorProxy.routingDecisions[serviceName] = append(
		simulatorProxy.routingDecisions[serviceName], selectedEndpoint)
	simulatorProxy.decisionsMutex.Unlock()

	return selectedEndpoint, nil
}

// RoutingDecisionsForService returns a copy of all routing decisions made for
// the named service, in order. Used by tests to verify load balancing behavior.
func (simulatorProxy *SimulatorProxy) RoutingDecisionsForService(serviceName string) []types.Endpoint {
	simulatorProxy.decisionsMutex.Lock()
	defer simulatorProxy.decisionsMutex.Unlock()

	originalDecisions := simulatorProxy.routingDecisions[serviceName]
	decisionsCopy := make([]types.Endpoint, len(originalDecisions))
	copy(decisionsCopy, originalDecisions)
	return decisionsCopy
}

// getOrCreateCounter returns the round-robin counter for the named service,
// creating one if it doesn't exist yet.
func (simulatorProxy *SimulatorProxy) getOrCreateCounter(serviceName string) *atomic.Uint64 {
	simulatorProxy.countersMutex.Lock()
	defer simulatorProxy.countersMutex.Unlock()

	if counter, exists := simulatorProxy.roundRobinCounters[serviceName]; exists {
		return counter
	}
	newCounter := &atomic.Uint64{}
	simulatorProxy.roundRobinCounters[serviceName] = newCounter
	return newCounter
}
