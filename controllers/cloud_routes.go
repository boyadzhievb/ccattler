package controllers

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// CloudRouteController watches node subnet assignments and programs VPC routes
// so that traffic destined for a node's pod CIDR is forwarded to the correct
// cloud instance. When nodes are added or removed, routes are created or
// deleted accordingly.
type CloudRouteController struct {
	cloudProvider cloud.CloudProvider
}

// NewCloudRouteController creates a CloudRouteController with the given cloud provider.
func NewCloudRouteController(cloudProvider cloud.CloudProvider) *CloudRouteController {
	return &CloudRouteController{cloudProvider: cloudProvider}
}

// Name returns "cloud-routes".
func (cloudRouteController *CloudRouteController) Name() string {
	return "cloud-routes"
}

// Watch returns the fact prefixes needed to reconcile VPC routes: node subnet
// assignments, observed node states, and node-to-cloud-instance mappings.
func (cloudRouteController *CloudRouteController) Watch() []string {
	return []string{
		types.ScanNetworkNodeSubnets,
		types.ScanObservedNodes,
		types.ScanObservedCloudRoutes,
	}
}

// Reconcile compares desired routes (from node subnet assignments) against
// existing cloud VPC routes and creates, updates, or deletes them as needed.
func (cloudRouteController *CloudRouteController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	nodeSubnets := extractNodeSubnetsForRoutes(facts)
	nodeProviderInstances := extractNodeToProviderInstance(facts)
	currentNodeStates := extractNodeStatesFromFacts(facts)
	existingRoutes := extractObservedCloudRoutes(facts)

	desiredRoutes := make(map[string]cloud.RouteConfig)
	for nodeID, subnetCIDR := range nodeSubnets {
		nodeState := currentNodeStates[nodeID]
		if nodeState != string(types.NodeAlive) {
			continue
		}
		providerInstanceID := nodeProviderInstances[nodeID]
		if providerInstanceID == "" {
			continue
		}
		desiredRoutes[subnetCIDR] = cloud.RouteConfig{
			DestinationCIDR:  subnetCIDR,
			TargetNodeID:     nodeID,
			TargetInstanceID: providerInstanceID,
		}
	}

	var proposedChanges []Change

	for cidr, desiredRoute := range desiredRoutes {
		existingNodeID, exists := existingRoutes[cidr]
		if exists && existingNodeID == desiredRoute.TargetNodeID {
			continue
		}

		ensureError := cloudRouteController.cloudProvider.EnsureRoute(ctx, desiredRoute)
		if ensureError != nil {
			continue
		}

		proposedChanges = append(proposedChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedCloudRoute(cidr),
			Value: []byte(desiredRoute.TargetNodeID),
		})
	}

	for cidr := range existingRoutes {
		if _, stillDesired := desiredRoutes[cidr]; !stillDesired {
			deleteError := cloudRouteController.cloudProvider.DeleteRoute(ctx, cidr)
			if deleteError != nil {
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedCloudRoute(cidr),
			})
		}
	}

	return proposedChanges, nil
}

// extractNodeSubnetsForRoutes builds a map from node ID to assigned subnet CIDR.
func extractNodeSubnetsForRoutes(facts []store.Fact) map[string]string {
	nodeSubnets := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanNetworkNodeSubnets) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanNetworkNodeSubnets)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "subnet" {
			nodeSubnets[pathParts[0]] = string(factEntry.Value)
		}
	}
	return nodeSubnets
}

// extractObservedCloudRoutes returns a map from destination CIDR to target
// node ID for all cloud routes currently tracked in the store.
func extractObservedCloudRoutes(facts []store.Fact) map[string]string {
	routes := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedCloudRoutes) {
			continue
		}
		cidr := strings.TrimPrefix(factEntry.Key, types.ScanObservedCloudRoutes)
		routes[cidr] = string(factEntry.Value)
	}
	return routes
}
