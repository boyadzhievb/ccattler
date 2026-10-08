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
