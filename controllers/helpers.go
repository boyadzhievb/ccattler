// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// splitFactKeyIntoEntityAndSuffix strips a known prefix from a fact key and
// splits the remainder at the first "/" into two parts: the entity name (e.g.
// service name, node ID, instance ID, volume name) and the remaining suffix
// (e.g. "state", "image", "scale/horizontal/min"). Returns ("", "", false)
// when the key has no "/" after the prefix, indicating a root-level key.
func splitFactKeyIntoEntityAndSuffix(factKey string, prefix string) (string, string, bool) {
	relativePath := strings.TrimPrefix(factKey, prefix)
	pathParts := strings.SplitN(relativePath, "/", 2)
	if len(pathParts) != 2 {
		return "", "", false
	}
	return pathParts[0], pathParts[1], true
}

// splitServiceFactKey splits a fact key under a service scan prefix into a
// service name and field suffix. Unlike splitFactKeyIntoEntityAndSuffix, this
// handles hierarchical service names (e.g. "payments/checkout") by using a
// prebuilt set of known service names to determine where the name ends and
// the field suffix begins.
func splitServiceFactKey(factKey string, prefix string, knownServiceNames map[string]bool) (string, string, bool) {
	relativePath := strings.TrimPrefix(factKey, prefix)
	for serviceName := range knownServiceNames {
		if strings.HasPrefix(relativePath, serviceName+"/") {
			fieldSuffix := relativePath[len(serviceName)+1:]
			return serviceName, fieldSuffix, true
		}
		if relativePath == serviceName {
			return "", "", false
		}
	}
	pathParts := strings.SplitN(relativePath, "/", 2)
	if len(pathParts) != 2 {
		return "", "", false
	}
	return pathParts[0], pathParts[1], true
}

// discoverServiceNamesFromFacts builds a set of service names from root marker
// facts (empty value) under the given service scan prefix.
func discoverServiceNamesFromFacts(facts []store.Fact, prefix string) map[string]bool {
	serviceNames := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, prefix) {
		if len(fact.Value) == 0 {
			serviceName := strings.TrimPrefix(fact.Key, prefix)
			serviceNames[serviceName] = true
		}
	}
	return serviceNames
}

// collectStringValuesBySuffix scans all facts under the given prefix, splits
// each key into entity name and suffix via splitFactKeyIntoEntityAndSuffix,
// and collects the string value for entries whose suffix matches exactly.
// Returns a map from entity name to the fact's string value. This replaces
// the common pattern of iterating a prefix, TrimPrefix, SplitN, and checking
// a specific suffix to build a map[string]string.
func collectStringValuesBySuffix(facts []store.Fact, prefix string, matchSuffix string) map[string]string {
	collectedValues := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, prefix) {
		entityName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, prefix)
		if hasSuffix && suffix == matchSuffix {
			collectedValues[entityName] = string(fact.Value)
		}
	}
	return collectedValues
}

// collectIntValuesBySuffix scans all facts under the given prefix, splits
// each key into entity name and suffix via splitFactKeyIntoEntityAndSuffix,
// and parses the value as an integer for entries whose suffix matches exactly.
// Returns a map from entity name to the parsed integer value. Values that
// cannot be parsed as integers are logged as warnings and stored as zero.
func collectIntValuesBySuffix(facts []store.Fact, prefix string, matchSuffix string) map[string]int {
	collectedValues := make(map[string]int)
	for _, fact := range store.FactsWithPrefix(facts, prefix) {
		entityName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, prefix)
		if hasSuffix && suffix == matchSuffix {
			parsedValue, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt integer fact", "key", fact.Key, "value", string(fact.Value))
			}
			collectedValues[entityName] = parsedValue
		}
	}
	return collectedValues
}

// collectCPUMillicoresBySuffix reads each service's CPU value under
// matchSuffix (e.g. "resources/cpu") in millicores, effective-first. Services
// whose value cannot be parsed are left out, returned separately and logged
// once under readerName (M243 D6).
func collectCPUMillicoresBySuffix(facts []store.Fact, matchSuffix string, readerName string, consequence string) (map[string]int64, map[string]bool) {
	return collectResourceQuantitiesEffectiveFirst(facts, matchSuffix, types.ParseCPUMillicores, readerName, consequence)
}

// collectMemoryBytesBySuffix reads each service's memory value under
// matchSuffix (e.g. "resources/memory") in bytes, effective-first. Services
// whose value cannot be parsed are left out, returned separately and logged
// once under readerName (M243 D6).
func collectMemoryBytesBySuffix(facts []store.Fact, matchSuffix string, readerName string, consequence string) (map[string]int64, map[string]bool) {
	return collectResourceQuantitiesEffectiveFirst(facts, matchSuffix, types.ParseMemoryBytes, readerName, consequence)
}

// collectResourceQuantitiesEffectiveFirst parses each service's value under
// matchSuffix from effective/service/, or desired/service/ when no effective
// value exists. An unreadable effective value does not fall back to desired.
func collectResourceQuantitiesEffectiveFirst(
	facts []store.Fact,
	matchSuffix string,
	parseQuantity func(string) (int64, error),
	readerName string,
	consequence string,
) (map[string]int64, map[string]bool) {
	chosenFacts := make(map[string]store.Fact)
	for _, servicePrefix := range []string{types.ScanDesiredServices, types.ScanEffectiveServices} {
		for _, fact := range store.FactsWithPrefix(facts, servicePrefix) {
			// Match the key's suffix, not its first segment, so team/web works (B5).
			serviceName, matched := strings.CutSuffix(strings.TrimPrefix(fact.Key, servicePrefix), "/"+matchSuffix)
			if matched && serviceName != "" && len(fact.Value) > 0 {
				chosenFacts[serviceName] = fact
			}
		}
	}
	parsedQuantities := make(map[string]int64, len(chosenFacts))
	unreadableServices := make(map[string]bool)
	for serviceName, fact := range chosenFacts {
		parsedQuantity, parseError := parseQuantity(string(fact.Value))
		if parseError != nil {
			types.WarnUnreadableResourceOnce(readerName, fact.Key, string(fact.Value), parseError, consequence)
			unreadableServices[serviceName] = true
			continue
		}
		parsedQuantities[serviceName] = parsedQuantity
	}
	return parsedQuantities, unreadableServices
}

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
	for _, instancePrefix := range instancePrefixes {
		for _, fact := range store.FactsWithPrefix(facts, instancePrefix) {
			relativePath := strings.TrimPrefix(fact.Key, instancePrefix)
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
	}
	return instanceFields
}

// endpointEligibleState returns the instance state for endpoint eligibility.
// Only observed runtime state and the controller_stopped marker matter.
// node_failure is deliberately ignored: it is a scheduling signal (replace
// this instance on a healthy node), not a runtime signal (the process exited).
// The data plane's fast-path eviction handles traffic away from stale nodes.
func endpointEligibleState(instanceFields map[string]string) types.InstanceState {
	if instanceFields["controller_stopped"] == "true" {
		return types.InstanceStopped
	}
	return types.InstanceState(instanceFields["state"])
}

// effectiveInstanceState merges observed state with derived markers.
// controller_stopped takes priority: once replaced, the instance is stopped
// even if a node_failure marker also exists.
func effectiveInstanceState(instanceFields map[string]string) types.InstanceState {
	if instanceFields["controller_stopped"] == "true" {
		return types.InstanceStopped
	}
	if instanceFields["node_failure"] == "true" {
		return types.InstanceFailed
	}
	return types.InstanceState(instanceFields["state"])
}

// extractServiceExposedPorts scans desired service facts and returns a map from
// service name to all exposed port numbers. Services without an expose
// declaration are omitted from the result.
func extractServiceExposedPorts(facts []store.Fact) map[string][]int {
	servicePorts := make(map[string][]int)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
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

// isActiveInstanceState returns true if the instance state represents a
// workload that is still active (not yet stopped or failed), including one
// waiting to restart in place (crash-loop-backoff, M233).
func isActiveInstanceState(instanceState types.InstanceState) bool {
	return instanceState == types.InstanceRunning ||
		instanceState == types.InstancePending ||
		instanceState == types.InstanceStarting ||
		instanceState == types.InstanceCrashLoopBackoff
}
