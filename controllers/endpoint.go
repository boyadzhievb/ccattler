// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// EndpointController creates and removes endpoint facts based on running
// instances. An endpoint is the combination of a service identity, an
// instance, and its reachable address. The controller ensures that every
// running instance with an exposed port and a reachable address (either
// a container IP from overlay networking, or a node address + host port
// mapping) has a corresponding endpoint fact, and that endpoints for
// stopped or missing instances are cleaned up.
type EndpointController struct{}

// NewEndpointController returns a ready-to-use EndpointController.
func NewEndpointController() *EndpointController { return &EndpointController{} }

// Name returns "endpoint", identifying this controller in logs and runner
// bookkeeping.
func (endpointController *EndpointController) Name() string { return "endpoint" }

// Watch returns the fact prefixes the endpoint controller monitors:
// observed instances (for state, IP, host port, and probe results),
// observed nodes (for advertise addresses), existing endpoints
// (for staleness detection), and desired services (for exposed port and
// probe configuration).
func (endpointController *EndpointController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
		types.ScanObservedNodes,
		types.ScanEndpoints,
		types.ScanDesiredServices,
		types.ScanDerivedInstances,
	}
}

// Reconcile examines observed instance facts, desired service port
// configurations, and existing endpoints, then emits changes to create
// missing endpoints and delete stale ones. An endpoint is desired when
// an instance is running, has a reachable address, and its service
// exposes a port.
func (endpointController *EndpointController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	servicePorts := extractServiceExposedPorts(facts)
	serviceHasReadinessProbe := detectServicesWithReadinessProbe(facts)
	nodeAddresses := parseNodeAdvertiseAddresses(facts)
	existingEndpoints := parseExistingEndpoints(facts)

	desiredEndpoints := buildDesiredEndpoints(
		instanceFields, servicePorts, serviceHasReadinessProbe, nodeAddresses,
	)

	return computeEndpointDiffChanges(desiredEndpoints, existingEndpoints), nil
}

// detectServicesWithReadinessProbe scans desired service facts and returns a
// set of service names that have a readiness probe method configured. This
// is used to gate endpoint creation on the readiness probe passing.
func detectServicesWithReadinessProbe(facts []store.Fact) map[string]bool {
	serviceHasReadinessProbe := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 4 && pathParts[1] == "probe" && pathParts[2] == string(types.ProbeReadiness) && pathParts[3] == "method" {
			serviceHasReadinessProbe[pathParts[0]] = true
		}
	}
	return serviceHasReadinessProbe
}

// parseNodeAdvertiseAddresses scans observed node facts and returns a map from
// node ID to the advertise address reported by that node agent.
func parseNodeAdvertiseAddresses(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedNodes, "address")
}

// parseExistingEndpoints scans endpoint facts and returns a map from endpoint
// key ("service/instance/port") to its current value (address:port). Both key
// and value are needed so the diff can detect stale addresses that need updating.
func parseExistingEndpoints(facts []store.Fact) map[string]string {
	existingEndpoints := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		existingEndpoints[relativePath] = string(fact.Value)
	}
	return existingEndpoints
}

// buildDesiredEndpoints determines which endpoints should exist based on running
// instances, exposed ports, readiness probe state, and network addresses. For
// each running instance with an exposed port, it produces an endpoint entry
// when a reachable address can be determined. When a node advertise address
// and host port are available, those are used for cross-host reachability.
// When only a container IP is available (overlay networking), that is used
// with the exposed port directly.
func buildDesiredEndpoints(
	instanceFields map[string]map[string]string,
	servicePorts map[string][]int,
	serviceHasReadinessProbe map[string]bool,
	nodeAddresses map[string]string,
) map[string]string {
	sortedInstanceIDs := make([]string, 0, len(instanceFields))
	for instanceID := range instanceFields {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	desiredEndpoints := make(map[string]string)
	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		if endpointEligibleState(fields) != types.InstanceRunning {
			continue
		}
		instanceIP := fields["ip"]
		legacyHostPort := fields["hostport"]
		nodeID := fields["node"]
		nodeAddress := nodeAddresses[nodeID]

		hasOverlayAddress := instanceIP != ""
		hasAnyHostAddress := (legacyHostPort != "" || hasPerPortHostPorts(fields)) && nodeAddress != ""
		if !hasOverlayAddress && !hasAnyHostAddress {
			continue
		}

		serviceName := fields["service"]
		exposedPorts := servicePorts[serviceName]
		if len(exposedPorts) == 0 {
			continue
		}
		if fields["drain_readiness"] == string(types.ReadinessProbeNotReady) {
			continue
		}
		if serviceHasReadinessProbe[serviceName] {
			readinessState := fields["probe/readiness"]
			if readinessState != string(types.ReadinessProbeReady) {
				continue
			}
		}

		for _, exposedPort := range exposedPorts {
			endpointKey := fmt.Sprintf("%s/%s/%d", serviceName, instanceID, exposedPort)
			perPortHostPort := fields[fmt.Sprintf("hostport/%d", exposedPort)]
			if perPortHostPort == "" {
				perPortHostPort = legacyHostPort
			}
			if perPortHostPort != "" && nodeAddress != "" {
				desiredEndpoints[endpointKey] = fmt.Sprintf("%s:%s", nodeAddress, perPortHostPort)
			} else if hasOverlayAddress {
				desiredEndpoints[endpointKey] = fmt.Sprintf("%s:%d", instanceIP, exposedPort)
			}
		}
	}
	return desiredEndpoints
}

// computeEndpointDiffChanges compares the desired endpoint set against the
// existing endpoint set and returns changes to create missing endpoints,
// update endpoints whose address has changed, and delete stale ones.
// Output is sorted for deterministic reconciliation.
func computeEndpointDiffChanges(desiredEndpoints map[string]string, existingEndpoints map[string]string) []Change {
	var changes []Change

	sortedDesiredKeys := make([]string, 0, len(desiredEndpoints))
	for endpointKey := range desiredEndpoints {
		sortedDesiredKeys = append(sortedDesiredKeys, endpointKey)
	}
	sort.Strings(sortedDesiredKeys)

	for _, endpointKey := range sortedDesiredKeys {
		desiredValue := desiredEndpoints[endpointKey]
		existingValue, exists := existingEndpoints[endpointKey]
		if !exists {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.PrefixEndpoint + "/service/" + endpointKey,
				Value: []byte(desiredValue),
			})
		} else if existingValue != desiredValue {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.PrefixEndpoint + "/service/" + endpointKey,
				Value: []byte(desiredValue),
			})
		}
	}

	sortedExistingKeys := make([]string, 0, len(existingEndpoints))
	for endpointKey := range existingEndpoints {
		sortedExistingKeys = append(sortedExistingKeys, endpointKey)
	}
	sort.Strings(sortedExistingKeys)

	var removedEndpointCount int
	for _, endpointKey := range sortedExistingKeys {
		if _, stillDesired := desiredEndpoints[endpointKey]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.PrefixEndpoint + "/service/" + endpointKey,
			})
			removedEndpointCount++
		}
	}

	createdEndpointCount := len(desiredEndpoints) - (len(existingEndpoints) - removedEndpointCount)
	if createdEndpointCount > 0 || removedEndpointCount > 0 {
		logging.Default().Info("endpoint reconciliation",
			"created", strconv.Itoa(createdEndpointCount),
			"removed", strconv.Itoa(removedEndpointCount))
	}

	return changes
}

// hasPerPortHostPorts returns true if the instance fields contain any
// per-container-port host port keys (e.g. "hostport/8080").
func hasPerPortHostPorts(instanceFields map[string]string) bool {
	for fieldKey := range instanceFields {
		if strings.HasPrefix(fieldKey, "hostport/") {
			return true
		}
	}
	return false
}
