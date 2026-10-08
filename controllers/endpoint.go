// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// EndpointController creates and removes endpoint facts based on running
// instances. An endpoint is the combination of a service identity, an
// instance, and its reachable address (ip:port). The controller ensures
// that every running instance with an IP and an exposed port has a
// corresponding endpoint fact, and that endpoints for stopped or missing
// instances are cleaned up.
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
// an instance is running, has an IP, and its service exposes a port.
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

// parseExistingEndpoints scans endpoint facts and returns a set of existing
// endpoint keys (in the format "service/instance/port") for staleness detection.
func parseExistingEndpoints(facts []store.Fact) map[string]bool {
	existingEndpoints := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		existingEndpoints[relativePath] = true
	}
	return existingEndpoints
}

// buildDesiredEndpoints determines which endpoints should exist based on running
// instances, exposed ports, readiness probe state, and network addresses. For
// each running instance with an IP and an exposed port, it produces an endpoint
// entry. When a node advertise address and host port are available, those are
// used for cross-host reachability instead of the container-local IP.
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
		if effectiveInstanceState(fields) != types.InstanceRunning {
			continue
		}
		instanceIP := fields["ip"]
		if instanceIP == "" {
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

		hostPort := fields["hostport"]
		nodeID := fields["node"]
		nodeAddress := nodeAddresses[nodeID]

		for _, exposedPort := range exposedPorts {
			endpointKey := fmt.Sprintf("%s/%s/%d", serviceName, instanceID, exposedPort)
			if hostPort != "" && nodeAddress != "" {
				desiredEndpoints[endpointKey] = fmt.Sprintf("%s:%s", nodeAddress, hostPort)
			} else {
				desiredEndpoints[endpointKey] = fmt.Sprintf("%s:%d", instanceIP, exposedPort)
			}
		}
	}
	return desiredEndpoints
}

// computeEndpointDiffChanges compares the desired endpoint set against the
// existing endpoint set and returns changes to create missing endpoints and
// delete stale ones. Output is sorted for deterministic reconciliation.
func computeEndpointDiffChanges(desiredEndpoints map[string]string, existingEndpoints map[string]bool) []Change {
	var changes []Change

	sortedDesiredKeys := make([]string, 0, len(desiredEndpoints))
	for endpointKey := range desiredEndpoints {
		sortedDesiredKeys = append(sortedDesiredKeys, endpointKey)
	}
	sort.Strings(sortedDesiredKeys)

	for _, endpointKey := range sortedDesiredKeys {
		if !existingEndpoints[endpointKey] {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.PrefixEndpoint + "/service/" + endpointKey,
				Value: []byte(desiredEndpoints[endpointKey]),
			})
		}
	}

	sortedExistingKeys := make([]string, 0, len(existingEndpoints))
	for endpointKey := range existingEndpoints {
		sortedExistingKeys = append(sortedExistingKeys, endpointKey)
	}
	sort.Strings(sortedExistingKeys)

	for _, endpointKey := range sortedExistingKeys {
		if _, stillDesired := desiredEndpoints[endpointKey]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.PrefixEndpoint + "/service/" + endpointKey,
			})
		}
	}

	return changes
}
