// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
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
// provisions or decommissions nodes via an InfrastructureProvider. When pending
// instances cannot be placed because no node has sufficient capacity, it
// requests new nodes. When nodes are idle (no placed instances), it removes
// them down to the configured minimum.
type ClusterAutoscaleController struct {
	infraProvider infra.InfrastructureProvider
}

// NewClusterAutoscaleController returns a new ClusterAutoscaleController
// with the given infrastructure provider.
func NewClusterAutoscaleController(infraProvider infra.InfrastructureProvider) *ClusterAutoscaleController {
	return &ClusterAutoscaleController{infraProvider: infraProvider}
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
	}
}

// Reconcile checks for unplaceable instances and idle nodes, then provisions
// or removes nodes accordingly.
func (clusterAutoscaleController *ClusterAutoscaleController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	clusterConfig := extractClusterAutoscaleConfig(facts)
	if clusterConfig.maxNodes == 0 {
		clusterConfig.maxNodes = defaultMaxClusterNodes
	}

	unplacedPending := countUnplacedPendingInstances(facts)
	nodeStates := extractClusterNodeStates(facts)
	instancesPerNode := countInstancesPerNode(facts)

	currentNodeCount, _ := clusterAutoscaleController.infraProvider.NodeCount(ctx)

	if unplacedPending > 0 && currentNodeCount < clusterConfig.maxNodes {
		nodesToAdd := unplacedPending
		if currentNodeCount+nodesToAdd > clusterConfig.maxNodes {
			nodesToAdd = clusterConfig.maxNodes - currentNodeCount
		}
		for range nodesToAdd {
			if _, requestError := clusterAutoscaleController.infraProvider.RequestNode(ctx); requestError != nil {
				logging.Default().Error("failed to request node from infrastructure provider", "error", requestError.Error())
			}
		}
	}

	if clusterConfig.minNodes > 0 {
		aliveCount := 0
		for _, state := range nodeStates {
			if state == types.NodeAlive {
				aliveCount++
			}
		}

		var idleNodes []string
		for nodeID, state := range nodeStates {
			if state == types.NodeAlive && instancesPerNode[nodeID] == 0 {
				idleNodes = append(idleNodes, nodeID)
			}
		}

		for _, nodeID := range idleNodes {
			if aliveCount <= clusterConfig.minNodes {
				break
			}
			if !strings.HasPrefix(nodeID, "auto-node-") {
				continue
			}
			if removeError := clusterAutoscaleController.infraProvider.RemoveNode(ctx, nodeID); removeError != nil {
				logging.Default().Error("failed to remove node from infrastructure provider", "node", nodeID, "error", removeError.Error())
			}
			aliveCount--
		}
	}

	return nil, nil
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

// countUnplacedPendingInstances counts instances in pending state without a placement.
func countUnplacedPendingInstances(facts []store.Fact) int {
	placedInstances := make(map[string]bool)
	pendingInstances := make(map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if !strings.Contains(instanceID, "/") {
			placedInstances[instanceID] = true
		}
	}

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		instanceID, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanObservedInstances)
		if hasSuffix && suffix == "state" && types.InstanceState(fact.Value) == types.InstancePending {
			pendingInstances[instanceID] = true
		}
	}

	unplaced := 0
	for instanceID := range pendingInstances {
		if !placedInstances[instanceID] {
			unplaced++
		}
	}
	return unplaced
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
