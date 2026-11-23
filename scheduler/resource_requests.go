// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"maps"
	"slices"
	"strings"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Service resource requests as the scheduler reads them (M243 D4, D5): the
// effective request first, then the desired one, through the shared parser.

// invalidRequestConsequence is what the scheduler does about an unreadable
// request, logged with it.
const invalidRequestConsequence = "new instances stay unplaced (invalid_resource_request); running ones keep their node and count as 0"

// extractServiceResourcesFromFacts returns each service's request in
// millicores and whole MiB (rounded up), effective-first through
// types.CollectEffectiveFirstResourceValues (no fallback from an unreadable
// effective value), and separately the services whose request cannot be
// parsed, mapped to "{key}={value}". Those have no request entry (count 0).
func extractServiceResourcesFromFacts(facts []store.Fact) (map[string]serviceResourceRequirements, map[string]string) {
	chosenValues := types.CollectEffectiveFirstResourceValues(facts)
	serviceNames := make(map[string]bool, len(chosenValues))
	for resourceKey := range chosenValues {
		serviceNames[resourceKey.ServiceName] = true
	}
	resources := make(map[string]serviceResourceRequirements, len(serviceNames))
	invalidResourceRequests := make(map[string]string)
	for serviceName := range serviceNames {
		requirements, unreadableRequest := parseServiceRequest(serviceName, chosenValues)
		if unreadableRequest != "" {
			invalidResourceRequests[serviceName] = unreadableRequest
			continue
		}
		resources[serviceName] = requirements
	}
	return resources, invalidResourceRequests
}

// parseServiceRequest parses the chosen CPU and memory request of a service,
// kinds in types.ResourceKindSpecs order (CPU first, so the result is
// deterministic). On a parse error it logs once and returns the
// "{key}={value}" of the unreadable fact.
func parseServiceRequest(serviceName string, chosenValues map[types.ServiceResourceKey]types.StoredResourceValue) (serviceResourceRequirements, string) {
	var requirements serviceResourceRequirements
	for _, kindSpec := range types.ResourceKindSpecs() {
		storedValue, stored := chosenValues[types.ServiceResourceKey{ServiceName: serviceName, Kind: kindSpec.Kind}]
		if !stored {
			continue
		}
		parsedQuantity, parseError := kindSpec.Parse(storedValue.FactValue)
		if parseError != nil {
			types.WarnUnreadableResourceOnce("scheduler", storedValue.FactKey, storedValue.FactValue, parseError, invalidRequestConsequence)
			return serviceResourceRequirements{}, storedValue.FactKey + "=" + storedValue.FactValue
		}
		switch kindSpec.Kind {
		case types.ResourceKindCPU:
			requirements.cpu = parsedQuantity
		case types.ResourceKindMemory:
			requirements.memory = types.MemoryBytesToMiBRoundedUp(parsedQuantity) // node facts are MiB (D4)
		}
	}
	return requirements, ""
}

// excludeInvalidRequestInstances returns the unplaced instances whose service
// request is readable; the others are neither placed nor preempt for (D5).
func excludeInvalidRequestInstances(
	unplaced []string,
	instances map[string]*schedulerInstanceInfo,
	invalidResourceRequests map[string]string,
) []string {
	if len(invalidResourceRequests) == 0 {
		return unplaced
	}
	placeable := make([]string, 0, len(unplaced))
	for _, instanceID := range unplaced {
		if instanceInfo := instances[instanceID]; instanceInfo != nil && invalidResourceRequests[instanceInfo.service] != "" {
			continue
		}
		placeable = append(placeable, instanceID)
	}
	return placeable
}

// buildInvalidResourceRequestChanges writes derived/scheduler/invalid_resource_request/{svc}
// for every service with an unreadable request (shown by cca status) and
// deletes the facts of services that were fixed. Services are sorted.
func buildInvalidResourceRequestChanges(facts []store.Fact, invalidResourceRequests map[string]string) []controllers.Change {
	storedInvalidRequests := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedSchedulerInvalidResourceRequests) {
		storedInvalidRequests[strings.TrimPrefix(fact.Key, types.ScanDerivedSchedulerInvalidResourceRequests)] = string(fact.Value)
	}
	var changes []controllers.Change
	for _, serviceName := range slices.Sorted(maps.Keys(invalidResourceRequests)) {
		if storedValue, stored := storedInvalidRequests[serviceName]; stored && storedValue == invalidResourceRequests[serviceName] {
			continue
		}
		changes = append(changes, controllers.Change{
			Type: store.OpPut, Key: types.KeyDerivedSchedulerInvalidResourceRequest(serviceName),
			Value: []byte(invalidResourceRequests[serviceName]),
		})
	}
	for _, serviceName := range slices.Sorted(maps.Keys(storedInvalidRequests)) {
		if _, stillInvalid := invalidResourceRequests[serviceName]; !stillInvalid {
			changes = append(changes, controllers.Change{
				Type: store.OpDelete, Key: types.KeyDerivedSchedulerInvalidResourceRequest(serviceName),
			})
		}
	}
	return changes
}
