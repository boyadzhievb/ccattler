// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Service resource requests as the scheduler reads them (M243 D4, D5): the
// effective request first, then the desired one, through the shared parser.

const (
	// cpuRequestKeySuffix ends the key of a service's CPU request, under
	// desired/service/{name} or effective/service/{name} (hierarchical names too).
	cpuRequestKeySuffix = "/resources/cpu"

	// memoryRequestKeySuffix ends the key of a service's memory request.
	memoryRequestKeySuffix = "/resources/memory"

	// invalidRequestConsequence is what the scheduler does about an unreadable
	// request, logged with it.
	invalidRequestConsequence = "new instances stay unplaced (invalid_resource_request); running ones keep their node and count as 0"
)

// storedResourceRequest is one stored request value and the key it is under.
type storedResourceRequest struct {
	factKey   string // factKey is the store key, named in the invalid-request fact and the log
	factValue string // factValue is the stored text, e.g. "500m" or "512Mi"
}

// serviceStoredRequests holds one layer's (desired or effective) CPU and
// memory request of a service; an empty factValue means not set.
type serviceStoredRequests struct {
	cpu    storedResourceRequest // cpu is the stored CPU request
	memory storedResourceRequest // memory is the stored memory request
}

// collectStoredResourceRequests returns, per service, the CPU and memory
// requests stored under one service prefix (desired or effective).
func collectStoredResourceRequests(facts []store.Fact, servicePrefix string) map[string]*serviceStoredRequests {
	requestsByService := make(map[string]*serviceStoredRequests)
	for _, fact := range store.FactsWithPrefix(facts, servicePrefix) {
		relativePath := strings.TrimPrefix(fact.Key, servicePrefix)
		serviceName, isCPU := strings.CutSuffix(relativePath, cpuRequestKeySuffix)
		serviceName, isMemory := strings.CutSuffix(serviceName, memoryRequestKeySuffix)
		if (!isCPU && !isMemory) || serviceName == "" || len(fact.Value) == 0 {
			continue
		}
		if requestsByService[serviceName] == nil {
			requestsByService[serviceName] = &serviceStoredRequests{}
		}
		storedRequest := storedResourceRequest{factKey: fact.Key, factValue: string(fact.Value)}
		if isCPU {
			requestsByService[serviceName].cpu = storedRequest
		} else {
			requestsByService[serviceName].memory = storedRequest
		}
	}
	return requestsByService
}

// effectiveFirst returns the effective request when one is stored, otherwise
// the desired one; an unreadable effective value does not fall back, because
// the agent uses it too.
func effectiveFirst(effectiveRequest storedResourceRequest, desiredRequest storedResourceRequest) storedResourceRequest {
	if effectiveRequest.factValue != "" {
		return effectiveRequest
	}
	return desiredRequest
}

// extractServiceResourcesFromFacts returns each service's request in
// millicores and whole MiB (rounded up), effective-first, and separately the
// services whose request cannot be parsed, mapped to "{key}={value}". An
// unparseable service has no request entry, so its placed instances count 0.
func extractServiceResourcesFromFacts(facts []store.Fact) (map[string]serviceResourceRequirements, map[string]string) {
	desiredRequests := collectStoredResourceRequests(facts, types.ScanDesiredServices)
	effectiveRequests := collectStoredResourceRequests(facts, types.ScanEffectiveServices)
	serviceNames := make(map[string]bool, len(desiredRequests)+len(effectiveRequests))
	for serviceName := range desiredRequests {
		serviceNames[serviceName] = true
	}
	for serviceName := range effectiveRequests {
		serviceNames[serviceName] = true
	}

	resources := make(map[string]serviceResourceRequirements, len(serviceNames))
	invalidResourceRequests := make(map[string]string)
	for serviceName := range serviceNames {
		desiredRequest, effectiveRequest := serviceStoredRequests{}, serviceStoredRequests{}
		if stored := desiredRequests[serviceName]; stored != nil {
			desiredRequest = *stored
		}
		if stored := effectiveRequests[serviceName]; stored != nil {
			effectiveRequest = *stored
		}
		requirements, unreadableRequest := parseServiceRequest(
			effectiveFirst(effectiveRequest.cpu, desiredRequest.cpu),
			effectiveFirst(effectiveRequest.memory, desiredRequest.memory),
		)
		if unreadableRequest != "" {
			invalidResourceRequests[serviceName] = unreadableRequest
			continue
		}
		resources[serviceName] = requirements
	}
	return resources, invalidResourceRequests
}

// parseServiceRequest parses the chosen CPU and memory request of a service.
// On a parse error it logs once and returns the "{key}={value}" of the
// unreadable fact (CPU checked first, so the result is deterministic).
func parseServiceRequest(cpuRequest storedResourceRequest, memoryRequest storedResourceRequest) (serviceResourceRequirements, string) {
	var requirements serviceResourceRequirements
	if cpuRequest.factValue != "" {
		cpuMillicores, parseError := types.ParseCPUMillicores(cpuRequest.factValue)
		if parseError != nil {
			types.WarnUnreadableResourceOnce("scheduler", cpuRequest.factKey, cpuRequest.factValue, parseError, invalidRequestConsequence)
			return serviceResourceRequirements{}, cpuRequest.factKey + "=" + cpuRequest.factValue
		}
		requirements.cpu = cpuMillicores
	}
	if memoryRequest.factValue != "" {
		memoryBytes, parseError := types.ParseMemoryBytes(memoryRequest.factValue)
		if parseError != nil {
			types.WarnUnreadableResourceOnce("scheduler", memoryRequest.factKey, memoryRequest.factValue, parseError, invalidRequestConsequence)
			return serviceResourceRequirements{}, memoryRequest.factKey + "=" + memoryRequest.factValue
		}
		requirements.memory = types.MemoryBytesToMiBRoundedUp(memoryBytes)
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
	for _, serviceName := range sortedStringMapKeys(invalidResourceRequests) {
		if storedValue, stored := storedInvalidRequests[serviceName]; stored && storedValue == invalidResourceRequests[serviceName] {
			continue
		}
		changes = append(changes, controllers.Change{
			Type: store.OpPut, Key: types.KeyDerivedSchedulerInvalidResourceRequest(serviceName),
			Value: []byte(invalidResourceRequests[serviceName]),
		})
	}
	for _, serviceName := range sortedStringMapKeys(storedInvalidRequests) {
		if _, stillInvalid := invalidResourceRequests[serviceName]; !stillInvalid {
			changes = append(changes, controllers.Change{
				Type: store.OpDelete, Key: types.KeyDerivedSchedulerInvalidResourceRequest(serviceName),
			})
		}
	}
	return changes
}

// sortedStringMapKeys returns the keys of a string map in sorted order.
func sortedStringMapKeys(valuesByKey map[string]string) []string {
	sortedKeys := make([]string, 0, len(valuesByKey))
	for key := range valuesByKey {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	return sortedKeys
}
