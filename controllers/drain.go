package controllers

import (
	"context"
	"sort"
	"strconv"
	"strings"

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
	globalActiveCountByService := countGlobalActiveInstancesByService(instanceStates, instanceServices)

	var proposedChanges []Change

	for _, drainingNodeID := range sortedKeys(drainingNodeSet) {
		evictedPerService, activeCount := drainController.evictInstancesFromNode(
			drainingNodeID, instancePlacementNode, instanceStates,
			instanceServices, disruptionBudgets, desiredInstanceCounts,
			globalActiveCountByService,
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
// Returns the proposed changes and the count of active instances that remain
// (including ones being evicted this cycle).
func (drainController *DrainController) evictInstancesFromNode(
	drainingNodeID string,
	instancePlacementNode map[string]string,
	instanceStates map[string]string,
	instanceServices map[string]string,
	disruptionBudgets map[string]disruptionBudget,
	desiredInstanceCounts map[string]int,
	globalActiveCountByService map[string]int,
) ([]Change, int) {
	// Collect active instances on this node grouped by service.
	activeInstancesByService := make(map[string][]string)
	totalActiveCount := 0

	sortedInstanceIDs := sortedKeys(instancePlacementNode)
	for _, instanceID := range sortedInstanceIDs {
		placedNodeID := instancePlacementNode[instanceID]
		if placedNodeID != drainingNodeID {
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
		// Evict the first (deterministically sorted) instance.
		evictionChanges = append(evictionChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(instanceIDs[0]),
			Value: []byte(string(types.InstanceStopped)),
		})
		// Decrement global count so subsequent draining nodes in this cycle
		// see the updated budget headroom.
		globalActiveCountByService[serviceName]--
	}

	return evictionChanges, totalActiveCount
}

// parseDrainingNodes scans observed node facts and returns a set of node IDs
// that are currently in the NodeDraining state.
func parseDrainingNodes(facts []store.Fact) map[string]bool {
	drainingNodeSet := make(map[string]bool)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedNodes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "state" {
			if string(factEntry.Value) == string(types.NodeDraining) {
				drainingNodeSet[pathParts[0]] = true
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
	currentInstanceStates := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "state" {
			currentInstanceStates[pathParts[0]] = string(factEntry.Value)
		}
	}
	return currentInstanceStates
}

// parseInstanceServices scans observed instance facts and returns a map from
// instance ID to the service name it belongs to.
func parseInstanceServices(facts []store.Fact) map[string]string {
	instanceServiceMap := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "service" {
			instanceServiceMap[pathParts[0]] = string(factEntry.Value)
		}
	}
	return instanceServiceMap
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
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDesiredServices)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		serviceName := pathParts[0]
		suffix := pathParts[1]
		budget := budgetsByService[serviceName]
		switch suffix {
		case "disruption/min_available":
			budget.minAvailable, _ = strconv.Atoi(string(factEntry.Value))
		case "disruption/max_unavailable":
			budget.maxUnavailable, _ = strconv.Atoi(string(factEntry.Value))
		default:
			continue
		}
		budgetsByService[serviceName] = budget
	}
	return budgetsByService
}

// countGlobalActiveInstancesByService counts all active instances per service
// across all nodes in the cluster, using observed instance states and service
// membership.
func countGlobalActiveInstancesByService(
	instanceStates map[string]string,
	instanceServices map[string]string,
) map[string]int {
	activeCountByService := make(map[string]int)
	for instanceID, stateValue := range instanceStates {
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
	desiredCounts := make(map[string]int)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDesiredServices)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "instances" {
			desiredCounts[pathParts[0]], _ = strconv.Atoi(string(factEntry.Value))
		}
	}
	return desiredCounts
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
