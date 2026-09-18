package controllers

import (
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// parseInstanceFieldsFromFacts scans observed instance facts and returns a
// nested map keyed by instance ID, where each inner map holds field name to
// value pairs (e.g. "state" -> "running", "service" -> "web").
func parseInstanceFieldsFromFacts(facts []store.Fact) map[string]map[string]string {
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
	return instanceFields
}

// extractServiceExposedPorts scans desired service facts and returns a map from
// service name to its first exposed port number. Services without an expose
// declaration are omitted from the result.
func extractServiceExposedPorts(facts []store.Fact) map[string]int {
	servicePorts := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 3 && pathParts[1] == "expose" {
			portNumber, _ := strconv.Atoi(pathParts[2])
			if portNumber > 0 {
				if _, alreadySet := servicePorts[pathParts[0]]; !alreadySet {
					servicePorts[pathParts[0]] = portNumber
				}
			}
		}
	}
	return servicePorts
}
