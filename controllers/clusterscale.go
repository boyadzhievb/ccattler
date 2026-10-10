// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultMaxClusterNodes is the maximum number of nodes the cluster autoscaler
// will provision when no explicit max_nodes configuration is set.
const defaultMaxClusterNodes = 100

// ClusterAutoscaleController watches for unsatisfied scheduling demand and
// manages durable capacity requests via an InfrastructureProvider. When the
// scheduler emits unplaced-demand facts with reason "insufficient_capacity",
// the autoscaler creates capacity request facts in "pending" state. The
// post-commit executor calls the provider and transitions requests through
// launching → ready (or failed). Implements PostCommitController.
type ClusterAutoscaleController struct {
	infraProvider infra.InfrastructureProvider // infraProvider provisions and decommissions nodes.
	factStore     store.StateStore             // factStore is used by the post-commit executor.
}

// NewClusterAutoscaleController returns a new ClusterAutoscaleController
// with the given infrastructure provider and fact store.
func NewClusterAutoscaleController(infraProvider infra.InfrastructureProvider, factStore store.StateStore) *ClusterAutoscaleController {
	return &ClusterAutoscaleController{
		infraProvider: infraProvider,
		factStore:     factStore,
	}
}

// Name returns "cluster-autoscale", identifying this controller in logs.
func (clusterAutoscaleController *ClusterAutoscaleController) Name() string {
	return "cluster-autoscale"
}

// Watch returns the fact prefixes that drive cluster autoscaling decisions.
func (clusterAutoscaleController *ClusterAutoscaleController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanObservedNodes,
		types.ScanPlacements,
		types.ScanDesiredServices,
		types.ScanDerivedSchedulerUnplaced,
		types.ScanDerivedCapacityRequests,
	}
}

// Reconcile reads unplaced-demand and existing capacity requests, then emits
// new capacity requests for instances that need more nodes, marks idle
// auto-nodes for removal, and cleans up completed requests.
func (clusterAutoscaleController *ClusterAutoscaleController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	clusterConfig := extractClusterAutoscaleConfig(facts)
	if clusterConfig.maxNodes == 0 {
		clusterConfig.maxNodes = defaultMaxClusterNodes
	}

	existingRequests := extractCapacityRequests(facts)
	nodeStates := extractClusterNodeStates(facts)
	currentAliveCount := countAliveAutoNodes(nodeStates)
	inFlightCount := countInFlightCapacityRequests(existingRequests)

	var changes []Change
	changes = append(changes, emitScaleUpRequests(facts, existingRequests, clusterConfig, currentAliveCount, inFlightCount)...)
	changes = append(changes, emitScaleDownRequests(facts, nodeStates, existingRequests, clusterConfig)...)
	changes = append(changes, cleanupReadyCapacityRequests(existingRequests)...)
	return changes, nil
}

// ExecutePostCommitOperations processes pending capacity requests by calling
// the infrastructure provider, checks launching requests for node readiness,
// and executes pending removal requests.
func (clusterAutoscaleController *ClusterAutoscaleController) ExecutePostCommitOperations(ctx context.Context) error {
	requestFacts, scanError := clusterAutoscaleController.factStore.Scan(ctx, types.ScanDerivedCapacityRequests)
	if scanError != nil {
		return scanError
	}

	requests := parseCapacityRequestsFromFacts(requestFacts)
	for _, requestID := range sortedRequestIDs(requests) {
		request := requests[requestID]
		switch request.state {
		case types.CapacityRequestPending:
			clusterAutoscaleController.executePendingRequest(ctx, requestID, request)
		case types.CapacityRequestLaunching:
			clusterAutoscaleController.checkLaunchingRequest(ctx, requestID, request)
		case types.CapacityRequestRemoving:
			clusterAutoscaleController.executeRemovalRequest(ctx, requestID, request)
		}
	}
	return nil
}

// capacityRequestInfo holds the parsed state of a single capacity request.
type capacityRequestInfo struct {
	state        types.CapacityRequestState        // state is the current lifecycle state.
	requirements infra.CapacityRequestRequirements // requirements are the resource needs.
	reason       string                            // reason is a human-readable explanation.
	nodeID       string                            // nodeID is set after the provider assigns a node.
}

// extractCapacityRequests parses all capacity request facts into a map keyed
// by request ID.
func extractCapacityRequests(facts []store.Fact) map[string]*capacityRequestInfo {
	requests := make(map[string]*capacityRequestInfo)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedCapacityRequests) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedCapacityRequests)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		requestID := parts[0]
		if requests[requestID] == nil {
			requests[requestID] = &capacityRequestInfo{}
		}
		parseCapacityRequestField(requests[requestID], parts[1], fact.Value)
	}
	return requests
}

// parseCapacityRequestField sets a field on a capacityRequestInfo from a
// fact suffix and value.
func parseCapacityRequestField(request *capacityRequestInfo, suffix string, factValue []byte) {
	switch suffix {
	case "state":
		request.state = types.CapacityRequestState(factValue)
	case "requirements":
		var requirements infra.CapacityRequestRequirements
		if unmarshalErr := json.Unmarshal(factValue, &requirements); unmarshalErr == nil {
			request.requirements = requirements
		}
	case "reason":
		request.reason = string(factValue)
	case "node_id":
		request.nodeID = string(factValue)
	}
}

// parseCapacityRequestsFromFacts is like extractCapacityRequests but takes
// raw store.Fact slices from a Scan call (used by post-commit).
func parseCapacityRequestsFromFacts(facts []store.Fact) map[string]*capacityRequestInfo {
	requests := make(map[string]*capacityRequestInfo)
	for _, fact := range facts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedCapacityRequests)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		requestID := parts[0]
		if requests[requestID] == nil {
			requests[requestID] = &capacityRequestInfo{}
		}
		parseCapacityRequestField(requests[requestID], parts[1], fact.Value)
	}
	return requests
}

// countInFlightCapacityRequests counts requests that are pending or launching.
func countInFlightCapacityRequests(requests map[string]*capacityRequestInfo) int {
	count := 0
	for _, request := range requests {
		if request.state == types.CapacityRequestPending || request.state == types.CapacityRequestLaunching {
			count++
		}
	}
	return count
}

// countAliveAutoNodes counts auto-provisioned nodes in alive state.
func countAliveAutoNodes(nodeStates map[string]types.NodeState) int {
	count := 0
	for nodeID, state := range nodeStates {
		if state == types.NodeAlive && strings.HasPrefix(nodeID, "auto-node-") {
			count++
		}
	}
	return count
}

// emitScaleUpRequests creates capacity request facts for unplaced instances
// that need more nodes (insufficient_capacity only).
func emitScaleUpRequests(
	facts []store.Fact,
	existingRequests map[string]*capacityRequestInfo,
	clusterConfig clusterAutoscaleConfig,
	currentAliveCount int,
	inFlightCount int,
) []Change {
	unplacedDemand := extractUnplacedDemand(facts)
	insufficientCapacityDemand := filterInsufficientCapacityDemand(unplacedDemand)
	if len(insufficientCapacityDemand) == 0 {
		return nil
	}

	effectiveNodeCount := currentAliveCount + inFlightCount
	availableSlots := clusterConfig.maxNodes - effectiveNodeCount
	if availableSlots <= 0 {
		return nil
	}

	alreadyCoveredInstances := collectCoveredInstances(existingRequests)
	return buildNewCapacityRequests(insufficientCapacityDemand, alreadyCoveredInstances, availableSlots)
}

// unplacedDemandEntry holds the reason and requirements for one unplaced instance.
type unplacedDemandEntry struct {
	reason       types.UnplacedReason
	requirements infra.CapacityRequestRequirements
}

// extractUnplacedDemand parses scheduler unplaced-demand facts.
func extractUnplacedDemand(facts []store.Fact) map[string]*unplacedDemandEntry {
	demand := make(map[string]*unplacedDemandEntry)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedSchedulerUnplaced) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedSchedulerUnplaced)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		if demand[instanceID] == nil {
			demand[instanceID] = &unplacedDemandEntry{}
		}
		switch parts[1] {
		case "reason":
			demand[instanceID].reason = types.UnplacedReason(fact.Value)
		case "requirements":
			var requirements infra.CapacityRequestRequirements
			if unmarshalErr := json.Unmarshal(fact.Value, &requirements); unmarshalErr == nil {
				demand[instanceID].requirements = requirements
			}
		}
	}
	return demand
}

// filterInsufficientCapacityDemand returns only demand entries where the
// reason is insufficient_capacity or no_nodes — these are the cases where
// provisioning more nodes would help.
func filterInsufficientCapacityDemand(demand map[string]*unplacedDemandEntry) map[string]*unplacedDemandEntry {
	filtered := make(map[string]*unplacedDemandEntry)
	for instanceID, entry := range demand {
		if entry.reason == types.UnplacedInsufficientCapacity || entry.reason == types.UnplacedNoNodes {
			filtered[instanceID] = entry
		}
	}
	return filtered
}

// collectCoveredInstances returns a set of instance IDs that already have
// a capacity request in any state. Including terminal states (ready, failed)
// prevents duplicate-key conflicts when cleanup deletes and new-request puts
// target the same keys in a single transaction. The next cycle after cleanup
// commits will create a fresh request if the instance is still unplaced.
func collectCoveredInstances(existingRequests map[string]*capacityRequestInfo) map[string]bool {
	covered := make(map[string]bool)
	for requestID := range existingRequests {
		covered[requestID] = true
	}
	return covered
}

// buildNewCapacityRequests creates grouped changes for new capacity requests,
// one per uncovered unplaced instance, up to the available slot limit.
func buildNewCapacityRequests(
	demand map[string]*unplacedDemandEntry,
	alreadyCovered map[string]bool,
	maxNew int,
) []Change {
	sortedInstanceIDs := make([]string, 0, len(demand))
	for instanceID := range demand {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	var changes []Change
	created := 0
	for _, instanceID := range sortedInstanceIDs {
		if created >= maxNew {
			break
		}
		if alreadyCovered[instanceID] {
			continue
		}
		entry := demand[instanceID]
		requestChanges := buildCapacityRequestChanges(instanceID, entry.requirements)
		changes = append(changes, requestChanges...)
		created++
	}
	return changes
}

// buildCapacityRequestChanges creates the store changes for a new capacity
// request in pending state. The requestID is the instance ID that triggered it.
func buildCapacityRequestChanges(requestID string, requirements infra.CapacityRequestRequirements) []Change {
	requirementsJSON, marshalError := json.Marshal(requirements)
	if marshalError != nil {
		logging.Default().Error("failed to marshal capacity requirements", "request", requestID, "error", marshalError.Error())
		requirementsJSON = []byte("{}")
	}
	return groupedChanges("cap-req-"+requestID,
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestState(requestID), Value: []byte(string(types.CapacityRequestPending))},
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestRequirements(requestID), Value: requirementsJSON},
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestReason(requestID), Value: []byte("insufficient capacity for instance " + requestID)},
	)
}

// emitScaleDownRequests detects idle auto-nodes and creates removal capacity
// requests for them, respecting the minNodes threshold.
func emitScaleDownRequests(
	facts []store.Fact,
	nodeStates map[string]types.NodeState,
	existingRequests map[string]*capacityRequestInfo,
	clusterConfig clusterAutoscaleConfig,
) []Change {
	if clusterConfig.minNodes <= 0 {
		return nil
	}
	instancesPerNode := countInstancesPerNode(facts)

	aliveCount := 0
	for _, state := range nodeStates {
		if state == types.NodeAlive {
			aliveCount++
		}
	}

	pendingRemovals := make(map[string]bool)
	for _, request := range existingRequests {
		if request.state == types.CapacityRequestRemoving && request.nodeID != "" {
			pendingRemovals[request.nodeID] = true
		}
	}

	idleNodes := findIdleAutoNodes(nodeStates, instancesPerNode)
	var changes []Change
	for _, nodeID := range idleNodes {
		if aliveCount <= clusterConfig.minNodes {
			break
		}
		if pendingRemovals[nodeID] {
			continue
		}
		removalChanges := buildRemovalRequestChanges(nodeID)
		changes = append(changes, removalChanges...)
		aliveCount--
	}
	return changes
}

// findIdleAutoNodes returns sorted list of auto-provisioned nodes with zero
// placed instances.
func findIdleAutoNodes(nodeStates map[string]types.NodeState, instancesPerNode map[string]int) []string {
	var idleNodes []string
	for nodeID, state := range nodeStates {
		if state == types.NodeAlive && strings.HasPrefix(nodeID, "auto-node-") && instancesPerNode[nodeID] == 0 {
			idleNodes = append(idleNodes, nodeID)
		}
	}
	sort.Strings(idleNodes)
	return idleNodes
}

// buildRemovalRequestChanges creates the store changes for a capacity removal
// request targeting a specific node.
func buildRemovalRequestChanges(nodeID string) []Change {
	requestID := "remove-" + nodeID
	return groupedChanges("cap-rm-"+nodeID,
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestState(requestID), Value: []byte(string(types.CapacityRequestRemoving))},
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestNodeID(requestID), Value: []byte(nodeID)},
		Change{Type: store.OpPut, Key: types.KeyDerivedCapacityRequestReason(requestID), Value: []byte("idle auto-node removal: " + nodeID)},
	)
}

// cleanupReadyCapacityRequests deletes capacity requests in ready or failed
// state so they don't accumulate.
func cleanupReadyCapacityRequests(existingRequests map[string]*capacityRequestInfo) []Change {
	var changes []Change
	sortedIDs := sortedRequestIDs(existingRequests)
	for _, requestID := range sortedIDs {
		request := existingRequests[requestID]
		if request.state == types.CapacityRequestReady || request.state == types.CapacityRequestFailed {
			changes = append(changes, deleteCapacityRequest(requestID)...)
		}
	}
	return changes
}

// deleteCapacityRequest produces delete changes for all keys of a capacity
// request.
func deleteCapacityRequest(requestID string) []Change {
	return groupedChanges("cap-del-"+requestID,
		Change{Type: store.OpDelete, Key: types.KeyDerivedCapacityRequestState(requestID)},
		Change{Type: store.OpDelete, Key: types.KeyDerivedCapacityRequestRequirements(requestID)},
		Change{Type: store.OpDelete, Key: types.KeyDerivedCapacityRequestReason(requestID)},
		Change{Type: store.OpDelete, Key: types.KeyDerivedCapacityRequestNodeID(requestID)},
	)
}

// sortedRequestIDs returns capacity request IDs in deterministic order.
func sortedRequestIDs(requests map[string]*capacityRequestInfo) []string {
	requestIDs := make([]string, 0, len(requests))
	for requestID := range requests {
		requestIDs = append(requestIDs, requestID)
	}
	sort.Strings(requestIDs)
	return requestIDs
}

// executePendingRequest calls the infrastructure provider for a pending
// capacity request and transitions it to launching or failed.
func (clusterAutoscaleController *ClusterAutoscaleController) executePendingRequest(
	ctx context.Context,
	requestID string,
	request *capacityRequestInfo,
) {
	nodeID, providerError := clusterAutoscaleController.infraProvider.RequestNodeWithRequirements(
		ctx, requestID, request.requirements,
	)
	if providerError != nil {
		logging.Default().Error("capacity request failed",
			"request", requestID, "error", providerError.Error())
		clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestFailed, "")
		return
	}
	clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestLaunching, nodeID)
}

// checkLaunchingRequest checks if a launching request's node has appeared
// in the observed state as alive, and transitions to ready.
func (clusterAutoscaleController *ClusterAutoscaleController) checkLaunchingRequest(
	ctx context.Context,
	requestID string,
	request *capacityRequestInfo,
) {
	if request.nodeID == "" {
		return
	}
	nodeStateFact, getError := clusterAutoscaleController.factStore.Get(ctx, types.KeyObservedNodeState(request.nodeID))
	if getError != nil || nodeStateFact == nil || len(nodeStateFact.Value) == 0 {
		return
	}
	if types.NodeState(nodeStateFact.Value) == types.NodeAlive {
		clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestReady, request.nodeID)
	}
}

// executeRemovalRequest calls RemoveNode for a removal request and
// transitions to ready on success or failed on error.
func (clusterAutoscaleController *ClusterAutoscaleController) executeRemovalRequest(
	ctx context.Context,
	requestID string,
	request *capacityRequestInfo,
) {
	if request.nodeID == "" {
		logging.Default().Error("removal request missing node_id", "request", requestID)
		clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestFailed, "")
		return
	}
	if removeError := clusterAutoscaleController.infraProvider.RemoveNode(ctx, request.nodeID); removeError != nil {
		logging.Default().Error("node removal failed",
			"request", requestID, "node", request.nodeID, "error", removeError.Error())
		clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestFailed, request.nodeID)
		return
	}
	clusterAutoscaleController.transitionCapacityRequest(ctx, requestID, types.CapacityRequestReady, request.nodeID)
}

// transitionCapacityRequest updates the state (and optionally node_id) of a
// capacity request in the store.
func (clusterAutoscaleController *ClusterAutoscaleController) transitionCapacityRequest(
	ctx context.Context,
	requestID string,
	newState types.CapacityRequestState,
	nodeID string,
) {
	if _, putErr := clusterAutoscaleController.factStore.Put(ctx,
		types.KeyDerivedCapacityRequestState(requestID),
		[]byte(string(newState))); putErr != nil {
		logging.Default().Error("failed to write capacity request state",
			"request", requestID, "state", string(newState), "error", putErr.Error())
	}
	if nodeID != "" {
		if _, putErr := clusterAutoscaleController.factStore.Put(ctx,
			types.KeyDerivedCapacityRequestNodeID(requestID),
			[]byte(nodeID)); putErr != nil {
			logging.Default().Error("failed to write capacity request node_id",
				"request", requestID, "node", nodeID, "error", putErr.Error())
		}
	}
}

// clusterAutoscaleConfig holds the min/max node bounds for cluster autoscaling.
type clusterAutoscaleConfig struct {
	minNodes int
	maxNodes int
}

// extractClusterAutoscaleConfig reads cluster autoscale configuration from facts.
func extractClusterAutoscaleConfig(facts []store.Fact) clusterAutoscaleConfig {
	config := clusterAutoscaleConfig{}
	clusterAutoscalePrefix := types.PrefixDesired + "/cluster/autoscale/"
	for _, fact := range store.FactsWithPrefix(facts, clusterAutoscalePrefix) {
		switch fact.Key {
		case types.KeyDesiredClusterAutoscaleMinNodes():
			parsedValue, parseError := strconv.Atoi(string(fact.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt cluster autoscale min_nodes", "value", string(fact.Value))
				continue
			}
			config.minNodes = parsedValue
		case types.KeyDesiredClusterAutoscaleMaxNodes():
			parsedValue, parseError := strconv.Atoi(string(fact.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt cluster autoscale max_nodes", "value", string(fact.Value))
				continue
			}
			config.maxNodes = parsedValue
		}
	}
	return config
}

// extractClusterNodeStates returns a map of node ID to node state.
func extractClusterNodeStates(facts []store.Fact) map[string]types.NodeState {
	stringStates := collectStringValuesBySuffix(facts, types.ScanObservedNodes, "state")
	nodeStates := make(map[string]types.NodeState, len(stringStates))
	for nodeID, stateValue := range stringStates {
		nodeStates[nodeID] = types.NodeState(stateValue)
	}
	return nodeStates
}

// countInstancesPerNode counts the number of active placed instances per node.
func countInstancesPerNode(facts []store.Fact) map[string]int {
	instanceFields := parseInstanceFieldsFromFacts(facts)

	counts := make(map[string]int)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if strings.Contains(instanceID, "/") {
			continue
		}
		fields := instanceFields[instanceID]
		if fields == nil {
			continue
		}
		state := effectiveInstanceState(fields)
		if state != types.InstanceStopped && state != "" {
			counts[string(fact.Value)]++
		}
	}
	return counts
}
