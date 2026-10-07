// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DrainController watches for nodes in the NodeDraining state and gracefully
// evicts instances placed on those nodes. It rate-limits evictions to at most
// one instance per service per reconciliation cycle, preventing a thundering
// herd of replacements. When zero active instances remain on a draining node,
// it writes a drain-complete marker fact.
type DrainController struct{}

// NewDrainController returns a DrainController ready for use with the
// controller runner.
func NewDrainController() *DrainController {
	return &DrainController{}
}

// Name returns "drain", identifying this controller in logs and runner
// bookkeeping.
func (drainController *DrainController) Name() string { return "drain" }

// Watch returns the fact prefixes this controller needs: observed node states,
// scheduler placements, observed instance states, desired services, and
// derived node drain metadata.
func (drainController *DrainController) Watch() []string {
	return []string{
		types.ScanObservedNodes,
		types.ScanPlacements,
		types.ScanObservedInstances,
		types.ScanDesiredServices,
		types.ScanDerivedNodes,
	}
}

// Reconcile examines node states to find draining nodes, then evicts at most
// one instance per service per reconciliation cycle from each draining node.
// When all active instances have been evicted, it writes a drain-complete
// marker. Returns the list of proposed fact store changes.
func (drainController *DrainController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	drainingNodeSet := parseDrainingNodes(facts)
	if len(drainingNodeSet) == 0 {
		return nil, nil
	}

	instancePlacementNode := parsePlacementsByNode(facts)
	instanceStates := parseInstanceStates(facts)
	instanceServices := parseInstanceServices(facts)
	drainCompleteMarkers := parseDrainCompleteMarkers(facts)
	disruptionBudgets := parseDisruptionBudgets(facts)
	desiredInstanceCounts := parseDesiredInstanceCounts(facts)
	existingEvictions := parseDrainEvictionMarkers(facts)
	globalActiveCountByService := countGlobalActiveInstancesByService(instanceStates, instanceServices, existingEvictions)

	var proposedChanges []Change

	for _, drainingNodeID := range sortedKeys(drainingNodeSet) {
		evictedPerService, activeCount := drainController.evictInstancesFromNode(
			drainingNodeID, instancePlacementNode, instanceStates,
			instanceServices, disruptionBudgets, desiredInstanceCounts,
			globalActiveCountByService, existingEvictions,
		)
		proposedChanges = append(proposedChanges, evictedPerService...)

		if activeCount == 0 && !drainCompleteMarkers[drainingNodeID] {
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedNodeDrainComplete(drainingNodeID),
				Value: []byte("true"),
			})
		}
	}

	return proposedChanges, nil
}

// evictInstancesFromNode finds all active instances on the given draining node
// and evicts at most one per service per call, respecting disruption budgets.
// It writes eviction markers to derived/ rather than observed/ to avoid racing
// with agent state writes. Returns the proposed changes and the count of
// non-evicted active instances that remain.
func (drainController *DrainController) evictInstancesFromNode(
	drainingNodeID string,
	instancePlacementNode map[string]string,
	instanceStates map[string]string,
	instanceServices map[string]string,
	disruptionBudgets map[string]disruptionBudget,
	desiredInstanceCounts map[string]int,
	globalActiveCountByService map[string]int,
	existingEvictions map[string]bool,
) ([]Change, int) {
	// Collect active instances on this node grouped by service, skipping
	// instances that already have eviction markers.
	activeInstancesByService := make(map[string][]string)
	totalActiveCount := 0

	sortedInstanceIDs := sortedKeys(instancePlacementNode)
	for _, instanceID := range sortedInstanceIDs {
		placedNodeID := instancePlacementNode[instanceID]
		if placedNodeID != drainingNodeID {
			continue
		}
		if existingEvictions[instanceID] {
			continue
		}
		instanceState := types.InstanceState(instanceStates[instanceID])
		if !isActiveInstanceState(instanceState) {
			continue
		}
		totalActiveCount++
		serviceName := instanceServices[instanceID]
		activeInstancesByService[serviceName] = append(activeInstancesByService[serviceName], instanceID)
	}

	// Evict at most one instance per service (rate-limiting), respecting
	// disruption budgets.
	var evictionChanges []Change
	sortedServiceNames := sortedKeys(activeInstancesByService)
	for _, serviceName := range sortedServiceNames {
		instanceIDs := activeInstancesByService[serviceName]
		if len(instanceIDs) == 0 {
			continue
		}
		if !canEvictUnderDisruptionBudget(serviceName, disruptionBudgets, desiredInstanceCounts, globalActiveCountByService) {
			continue
		}
		evictionChanges = append(evictionChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedNodeDrainEvict(drainingNodeID, instanceIDs[0]),
			Value: []byte("true"),
		})
		globalActiveCountByService[serviceName]--
	}

	return evictionChanges, totalActiveCount
}

// parseDrainingNodes scans observed node facts and returns a set of node IDs
// that are currently in the NodeDraining state.
func parseDrainingNodes(facts []store.Fact) map[string]bool {
	drainingNodeSet := make(map[string]bool)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		nodeID, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanObservedNodes)
		if hasSuffix && suffix == "state" {
			if string(factEntry.Value) == string(types.NodeDraining) {
				drainingNodeSet[nodeID] = true
			}
		}
	}
	return drainingNodeSet
}

// parsePlacementsByNode scans placement facts and returns a map from instance
// ID to the node ID where that instance is placed.
func parsePlacementsByNode(facts []store.Fact) map[string]string {
	instancePlacementNode := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(factEntry.Key, types.ScanPlacements)
		instancePlacementNode[instanceID] = string(factEntry.Value)
	}
	return instancePlacementNode
}

// parseInstanceStates scans observed instance facts and returns a map from
// instance ID to its current lifecycle state string.
func parseInstanceStates(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedInstances, "state")
}

// parseInstanceServices scans observed instance facts and returns a map from
// instance ID to the service name it belongs to.
func parseInstanceServices(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedInstances, "service")
}

// parseDrainCompleteMarkers scans derived node facts and returns a set of node
// IDs that have a drain-complete marker already written.
func parseDrainCompleteMarkers(facts []store.Fact) map[string]bool {
	completeMarkers := make(map[string]bool)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDerivedNodes) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDerivedNodes)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) == 3 && pathParts[1] == "drain" && pathParts[2] == "complete" {
			completeMarkers[pathParts[0]] = true
		}
	}
	return completeMarkers
}

// disruptionBudget holds the parsed disruption budget for a single service.
// Only one of the two fields will be non-zero.
type disruptionBudget struct {
	minAvailable   int // minimum instances that must remain running
	maxUnavailable int // maximum instances that may be simultaneously unavailable
}

// parseDisruptionBudgets scans desired service facts and returns disruption
// budgets keyed by service name.
func parseDisruptionBudgets(facts []store.Fact) map[string]disruptionBudget {
	budgetsByService := make(map[string]disruptionBudget)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}
		budget := budgetsByService[serviceName]
		switch suffix {
		case "disruption/min_available":
			parsedMinAvailable, parseErr := strconv.Atoi(string(factEntry.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt disruption min_available fact", "service", serviceName, "value", string(factEntry.Value))
			}
			budget.minAvailable = parsedMinAvailable
		case "disruption/max_unavailable":
			parsedMaxUnavailable, parseErr := strconv.Atoi(string(factEntry.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt disruption max_unavailable fact", "service", serviceName, "value", string(factEntry.Value))
			}
			budget.maxUnavailable = parsedMaxUnavailable
		default:
			continue
		}
		budgetsByService[serviceName] = budget
	}
	return budgetsByService
}

// countGlobalActiveInstancesByService counts all active instances per service
// across all nodes in the cluster, using observed instance states and service
// membership. Instances with existing eviction markers are excluded since they
// are being drained and will be stopped by the agent.
func countGlobalActiveInstancesByService(
	instanceStates map[string]string,
	instanceServices map[string]string,
	evictedInstances map[string]bool,
) map[string]int {
	activeCountByService := make(map[string]int)
	for instanceID, stateValue := range instanceStates {
		if evictedInstances[instanceID] {
			continue
		}
		if isActiveInstanceState(types.InstanceState(stateValue)) {
			serviceName := instanceServices[instanceID]
			if serviceName != "" {
				activeCountByService[serviceName]++
			}
		}
	}
	return activeCountByService
}

// parseDesiredInstanceCounts scans desired service facts and returns the
// desired instance count for each service.
func parseDesiredInstanceCounts(facts []store.Fact) map[string]int {
	return collectIntValuesBySuffix(facts, types.ScanDesiredServices, "instances")
}

// canEvictUnderDisruptionBudget checks whether evicting one more instance of
// the given service is permitted by its disruption budget. If no budget is
// configured, eviction is always allowed.
func canEvictUnderDisruptionBudget(
	serviceName string,
	disruptionBudgets map[string]disruptionBudget,
	desiredInstanceCounts map[string]int,
	globalActiveCountByService map[string]int,
) bool {
	budget, hasBudget := disruptionBudgets[serviceName]
	if !hasBudget {
		return true
	}
	currentActiveCount := globalActiveCountByService[serviceName]
	if budget.minAvailable > 0 {
		return currentActiveCount-1 >= budget.minAvailable
	}
	if budget.maxUnavailable > 0 {
		desiredCount := desiredInstanceCounts[serviceName]
		if desiredCount == 0 {
			return true
		}
		currentlyUnavailable := desiredCount - currentActiveCount
		return currentlyUnavailable+1 <= budget.maxUnavailable
	}
	return true
}

// parseDrainEvictionMarkers scans derived node facts and returns a set of
// instance IDs that have already been marked for eviction by a prior cycle.
func parseDrainEvictionMarkers(facts []store.Fact) map[string]bool {
	evictedInstances := make(map[string]bool)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDerivedNodes) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDerivedNodes)
		pathParts := strings.SplitN(relativePath, "/", 4)
		if len(pathParts) == 4 && pathParts[1] == "drain" && pathParts[2] == "evict" {
			evictedInstances[pathParts[3]] = true
		}
	}
	return evictedInstances
}

// isActiveInstanceState returns true if the instance state represents a
// workload that is still active (not yet stopped or failed).
func isActiveInstanceState(instanceState types.InstanceState) bool {
	return instanceState == types.InstanceRunning ||
		instanceState == types.InstancePending ||
		instanceState == types.InstanceStarting
}

// sortedKeys returns the keys of a map[string]T sorted alphabetically. This
// ensures deterministic iteration order for controllers.
func sortedKeys[T any](inputMap map[string]T) []string {
	keys := make([]string, 0, len(inputMap))
	for key := range inputMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
