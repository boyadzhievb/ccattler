package controllers

import (
	"context"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

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
		clusterConfig.maxNodes = 100
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
			clusterAutoscaleController.infraProvider.RequestNode(ctx)
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
			clusterAutoscaleController.infraProvider.RemoveNode(ctx, nodeID)
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
	for _, fact := range facts {
		switch fact.Key {
		case types.KeyDesiredClusterAutoscaleMinNodes():
			config.minNodes, _ = strconv.Atoi(string(fact.Value))
		case types.KeyDesiredClusterAutoscaleMaxNodes():
			config.maxNodes, _ = strconv.Atoi(string(fact.Value))
		}
	}
	return config
}

// countUnplacedPendingInstances counts instances in pending state without a placement.
func countUnplacedPendingInstances(facts []store.Fact) int {
	placedInstances := make(map[string]bool)
	pendingInstances := make(map[string]bool)

	for _, fact := range facts {
		switch {
		case strings.HasPrefix(fact.Key, types.ScanPlacements):
			instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
			if !strings.Contains(instanceID, "/") {
				placedInstances[instanceID] = true
			}
		case strings.HasPrefix(fact.Key, types.ScanObservedInstances):
			relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
			parts := strings.SplitN(relativePath, "/", 2)
			if len(parts) == 2 && parts[1] == "state" && types.InstanceState(fact.Value) == types.InstancePending {
				pendingInstances[parts[0]] = true
			}
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
	nodeStates := make(map[string]types.NodeState)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedNodes) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "state" {
			nodeStates[parts[0]] = types.NodeState(fact.Value)
		}
	}
	return nodeStates
}

// countInstancesPerNode counts the number of active placed instances per node.
func countInstancesPerNode(facts []store.Fact) map[string]int {
	instanceStates := make(map[string]types.InstanceState)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "state" {
			instanceStates[parts[0]] = types.InstanceState(fact.Value)
		}
	}

	counts := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanPlacements) {
			continue
		}
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if strings.Contains(instanceID, "/") {
			continue
		}
		state := instanceStates[instanceID]
		if state != types.InstanceStopped && state != "" {
			counts[string(fact.Value)]++
		}
	}
	return counts
}
