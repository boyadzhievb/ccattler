package controllers

import (
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// parseInstanceFieldsFromFacts scans observed and derived instance facts and
// returns a nested map keyed by instance ID, where each inner map holds field
// name to value pairs (e.g. "state" -> "running", "service" -> "web",
// "drain_since" -> "1726704000000").
func parseInstanceFieldsFromFacts(facts []store.Fact) map[string]map[string]string {
	instanceFields := make(map[string]map[string]string)
	instancePrefixes := []string{
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
	}
	for _, fact := range facts {
		for _, instancePrefix := range instancePrefixes {
			if !strings.HasPrefix(fact.Key, instancePrefix) {
				continue
			}
			relativePath := strings.TrimPrefix(fact.Key, instancePrefix)
			pathParts := strings.SplitN(relativePath, "/", 2)
			if len(pathParts) != 2 {
				break
			}
			instanceID := pathParts[0]
			if instanceFields[instanceID] == nil {
				instanceFields[instanceID] = make(map[string]string)
			}
			instanceFields[instanceID][pathParts[1]] = string(fact.Value)
			break
		}
	}
	return instanceFields
}

// extractServiceExposedPorts scans desired service facts and returns a map from
// service name to all exposed port numbers. Services without an expose
// declaration are omitted from the result.
func extractServiceExposedPorts(facts []store.Fact) map[string][]int {
	servicePorts := make(map[string][]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) >= 3 && pathParts[1] == "expose" {
			if pathParts[len(pathParts)-1] == "external" {
				continue
			}
			portNumber, _ := strconv.Atoi(pathParts[2])
			if portNumber > 0 {
				alreadyPresent := false
				for _, existingPort := range servicePorts[pathParts[0]] {
					if existingPort == portNumber {
						alreadyPresent = true
						break
					}
				}
				if !alreadyPresent {
					servicePorts[pathParts[0]] = append(servicePorts[pathParts[0]], portNumber)
				}
			}
		}
	}
	return servicePorts
}
