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

// resourceQuantityReader names who reads a resource value, for the
// once-per-value warning, and which services it reads.
type resourceQuantityReader struct {
	readerName     string                        // readerName is logged with an unreadable value, e.g. "eviction"
	consequence    string                        // consequence is what the reader does about an unreadable value
	includeService func(serviceName string) bool // includeService selects the services read; nil reads all
}

// collectResourceQuantities parses each selected service's request of kind,
// effective-first (types.CollectEffectiveFirstResourceValues: an unreadable
// effective value does not fall back to desired). Unreadable values are left
// out and logged once (M243 D6). The kind picks suffix and parser together.
func collectResourceQuantities(facts []store.Fact, kind types.ResourceKind, reader resourceQuantityReader) map[string]int64 {
	return parseChosenResourceQuantities(types.CollectEffectiveFirstResourceValues(facts), kind, reader)
}

// parseChosenResourceQuantities is collectResourceQuantities on an
// already-built effective-first map, so a reader of several kinds builds it
// once per cycle (R5-N5).
func parseChosenResourceQuantities(
	chosenValues map[types.ServiceResourceKey]types.StoredResourceValue,
	kind types.ResourceKind,
	reader resourceQuantityReader,
) map[string]int64 {
	kindSpec := types.LookupResourceKindSpec(kind)
	parsedQuantities := make(map[string]int64)
	for resourceKey, storedValue := range chosenValues {
		if resourceKey.Kind != kind || (reader.includeService != nil && !reader.includeService(resourceKey.ServiceName)) {
			continue
		}
		parsedQuantity, parseError := kindSpec.Parse(storedValue.FactValue)
		if parseError != nil {
			types.WarnUnreadableResourceOnce(reader.readerName, storedValue.FactKey, storedValue.FactValue, parseError, reader.consequence)
			continue
		}
		parsedQuantities[resourceKey.ServiceName] = parsedQuantity
	}
	return parsedQuantities
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

// stateWithControllerStop returns the observed state, or stopped under the
// controller_stopped marker; node_failure (a scheduling signal) is ignored.
// Callers: endpoint eligibility (buildDesiredEndpoints) and cluster-scaling
// demand (isInstanceWaitingForPlacement); a change here affects both.
func stateWithControllerStop(instanceFields map[string]string) types.InstanceState {
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
