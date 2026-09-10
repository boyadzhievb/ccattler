package controllers

import (
	"context"
	"fmt"
	"strconv"
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
// observed instances (for state, IP, and probe results), existing endpoints
// (for staleness detection), and desired services (for exposed port and
// probe configuration).
func (endpointController *EndpointController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
		types.ScanEndpoints,
		types.ScanDesiredServices,
	}
}

// Reconcile examines observed instance facts, desired service port
// configurations, and existing endpoints, then emits changes to create
// missing endpoints and delete stale ones. An endpoint is desired when
// an instance is running, has an IP, and its service exposes a port.
func (endpointController *EndpointController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	// Parse instance info: instanceID -> {field -> value}.
	instanceFields := make(map[string]map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		instanceID := pathParts[0]
		if instanceFields[instanceID] == nil {
			instanceFields[instanceID] = make(map[string]string)
		}
		instanceFields[instanceID][pathParts[1]] = string(fact.Value)
	}

	// Parse service exposed ports: serviceName -> first exposed port number.
	// Also track which services have a readiness probe configured.
	servicePorts := make(map[string]int)
	serviceHasReadinessProbe := make(map[string]bool)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		// relativePath = "{name}/expose/{port}" or "{name}/probe/readiness/method"
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 3 && pathParts[1] == "expose" {
			portNumber, _ := strconv.Atoi(pathParts[2])
			if portNumber > 0 {
				servicePorts[pathParts[0]] = portNumber
			}
		}
		if len(pathParts) == 4 && pathParts[1] == "probe" && pathParts[2] == "readiness" && pathParts[3] == "method" {
			serviceHasReadinessProbe[pathParts[0]] = true
		}
	}

	// Parse existing endpoints: "service/instance" -> true.
	existingEndpoints := make(map[string]bool)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanEndpoints) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		existingEndpoints[relativePath] = true
	}

	// Determine desired endpoints: running instances with an IP, an exposed port,
	// and passing readiness (if a readiness probe is configured for the service).
	desiredEndpoints := make(map[string]string) // "service/instance" -> "ip:port"
	for instanceID, fields := range instanceFields {
		if types.InstanceState(fields["state"]) != types.InstanceRunning {
			continue
		}
		instanceIP := fields["ip"]
		if instanceIP == "" {
			continue
		}
		serviceName := fields["service"]
		exposedPort := servicePorts[serviceName]
		if exposedPort == 0 {
			continue
		}
		if serviceHasReadinessProbe[serviceName] {
			readinessState := fields["probe/readiness"]
			if readinessState != string(types.ReadinessProbeReady) {
				continue
			}
		}
		endpointKey := fmt.Sprintf("%s/%s", serviceName, instanceID)
		desiredEndpoints[endpointKey] = fmt.Sprintf("%s:%d", instanceIP, exposedPort)
	}

	var changes []Change

	// Create missing endpoints.
	for endpointKey, endpointAddress := range desiredEndpoints {
		if !existingEndpoints[endpointKey] {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.PrefixEndpoint + "/service/" + endpointKey,
				Value: []byte(endpointAddress),
			})
		}
	}

	// Remove stale endpoints.
	for endpointKey := range existingEndpoints {
		if _, stillDesired := desiredEndpoints[endpointKey]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.PrefixEndpoint + "/service/" + endpointKey,
			})
		}
	}

	return changes, nil
}
