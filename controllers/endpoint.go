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
	}
}

// Reconcile examines observed instance facts, desired service port
// configurations, and existing endpoints, then emits changes to create
// missing endpoints and delete stale ones. An endpoint is desired when
// an instance is running, has an IP, and its service exposes a port.
func (endpointController *EndpointController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	instanceFields := parseInstanceFieldsFromFacts(facts)

	servicePorts := extractServiceExposedPorts(facts)

	// Track which services have a readiness probe configured.
	serviceHasReadinessProbe := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 4 && pathParts[1] == "probe" && pathParts[2] == "readiness" && pathParts[3] == "method" {
			serviceHasReadinessProbe[pathParts[0]] = true
		}
	}

	// Parse node advertise addresses: nodeID -> address.
	nodeAddresses := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "address" {
			nodeAddresses[pathParts[0]] = string(fact.Value)
		}
	}

	// Parse existing endpoints: "service/instance" -> true.
	existingEndpoints := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		existingEndpoints[relativePath] = true
	}

	// Determine desired endpoints: running instances with an IP, exposed ports,
	// and passing readiness (if a readiness probe is configured for the service).
	// When a node advertise address and host port are available, use those for
	// cross-host reachability instead of the container-local IP.
	// Each instance gets one endpoint per exposed port.
	// Sort instance IDs for deterministic output — map iteration order varies.
	sortedInstanceIDs := make([]string, 0, len(instanceFields))
	for instanceID := range instanceFields {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	desiredEndpoints := make(map[string]string) // "service/instance/port" -> "ip:port"
	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		if types.InstanceState(fields["state"]) != types.InstanceRunning {
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

	var changes []Change

	// Sort keys for deterministic output — map iteration order varies.
	sortedDesiredKeys := make([]string, 0, len(desiredEndpoints))
	for endpointKey := range desiredEndpoints {
		sortedDesiredKeys = append(sortedDesiredKeys, endpointKey)
	}
	sort.Strings(sortedDesiredKeys)

	// Create missing endpoints.
	for _, endpointKey := range sortedDesiredKeys {
		if !existingEndpoints[endpointKey] {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.PrefixEndpoint + "/service/" + endpointKey,
				Value: []byte(desiredEndpoints[endpointKey]),
			})
		}
	}

	// Remove stale endpoints.
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

	return changes, nil
}
