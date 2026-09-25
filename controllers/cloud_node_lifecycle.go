package controllers

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NodeLifecycleController watches cloud instance state and detects terminated
// instances. When a cloud instance backing a CCattler node is terminated, the
// controller marks the node as draining and then unreachable so the failure
// controller can reschedule its workloads. It also cleans up stale cloud
// instance facts for terminated instances.
type NodeLifecycleController struct {
	cloudProvider cloud.CloudProvider
}

// NewNodeLifecycleController creates a NodeLifecycleController with the given
// cloud provider.
func NewNodeLifecycleController(cloudProvider cloud.CloudProvider) *NodeLifecycleController {
	return &NodeLifecycleController{cloudProvider: cloudProvider}
}

// Name returns "cloud-node-lifecycle".
func (nodeLifecycleController *NodeLifecycleController) Name() string {
	return "cloud-node-lifecycle"
}

// Watch returns the fact prefixes needed to detect terminated cloud instances
// and correlate them with CCattler nodes.
func (nodeLifecycleController *NodeLifecycleController) Watch() []string {
	return []string{
		types.ScanObservedNodes,
		types.ScanObservedCloudInstances,
	}
}

// Reconcile queries the cloud provider for instance state and compares it
// against known node-to-instance mappings. For terminated instances, it marks
// the corresponding node as draining (then unreachable on the next cycle)
// and writes observed cloud instance state facts.
func (nodeLifecycleController *NodeLifecycleController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	cloudInstances, listError := nodeLifecycleController.cloudProvider.ListInstances(ctx)
	if listError != nil {
		return nil, listError
	}

	cloudInstancesByProviderID := make(map[string]cloud.CloudInstance)
	for _, cloudInstance := range cloudInstances {
		cloudInstancesByProviderID[cloudInstance.ProviderInstanceID] = cloudInstance
	}

	nodeToProviderInstance, currentNodeStates := extractNodeProviderAndStates(facts)
	observedCloudStates := extractObservedCloudInstanceStates(facts)

	var proposedChanges []Change

	for nodeID, providerInstanceID := range nodeToProviderInstance {
		cloudInstance, exists := cloudInstancesByProviderID[providerInstanceID]
		if !exists {
			continue
		}

		previousCloudState := observedCloudStates[providerInstanceID]
		currentCloudState := string(cloudInstance.State)
		if previousCloudState != currentCloudState {
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedCloudInstanceState(providerInstanceID),
				Value: []byte(currentCloudState),
			})
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedCloudInstanceNodeID(providerInstanceID),
				Value: []byte(nodeID),
			})
		}

		if cloudInstance.State == cloud.InstanceStateTerminated {
			nodeState := currentNodeStates[nodeID]
			if nodeState == string(types.NodeAlive) {
				proposedChanges = append(proposedChanges, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedNodeState(nodeID),
					Value: []byte(string(types.NodeDraining)),
				})
			} else if nodeState == string(types.NodeDraining) {
				proposedChanges = append(proposedChanges, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedNodeState(nodeID),
					Value: []byte(string(types.NodeUnreachable)),
				})
			}
		}
	}

	return proposedChanges, nil
}

// extractNodeProviderAndStates walks observed node facts once and returns
// both a node-to-provider-instance map and a node-to-state map.
func extractNodeProviderAndStates(facts []store.Fact) (map[string]string, map[string]string) {
	nodeToProviderInstance := make(map[string]string)
	nodeStates := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedNodes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		switch pathParts[1] {
		case "provider_instance_id":
			nodeToProviderInstance[pathParts[0]] = string(factEntry.Value)
		case "state":
			nodeStates[pathParts[0]] = string(factEntry.Value)
		}
	}
	return nodeToProviderInstance, nodeStates
}

// extractObservedCloudInstanceStates builds a map from provider instance ID to
// the last observed cloud state.
func extractObservedCloudInstanceStates(facts []store.Fact) map[string]string {
	cloudStates := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedCloudInstances) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedCloudInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "state" {
			cloudStates[pathParts[0]] = string(factEntry.Value)
		}
	}
	return cloudStates
}
