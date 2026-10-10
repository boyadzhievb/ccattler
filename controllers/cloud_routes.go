// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// CloudRouteController watches node subnet assignments and programs VPC routes
// so that traffic destined for a node's pod CIDR is forwarded to the correct
// cloud instance. When nodes are added or removed, routes are created or
// deleted accordingly. Implements PostCommitController to defer provider
// calls until after the store transaction succeeds.
type CloudRouteController struct {
	cloudProvider cloud.CloudProvider // cloudProvider is the backend for route API calls.
	factStore     store.StateStore    // factStore is used by the post-commit executor to read pending ops.
}

// NewCloudRouteController creates a CloudRouteController with the given cloud
// provider and fact store.
func NewCloudRouteController(cloudProvider cloud.CloudProvider, factStore store.StateStore) *CloudRouteController {
	return &CloudRouteController{
		cloudProvider: cloudProvider,
		factStore:     factStore,
	}
}

// Name returns "cloud-routes".
func (cloudRouteController *CloudRouteController) Name() string {
	return "cloud-routes"
}

// Watch returns the fact prefixes needed to reconcile VPC routes: node subnet
// assignments, observed node states, node-to-cloud-instance mappings,
// existing cloud routes, and derived pending operations.
func (cloudRouteController *CloudRouteController) Watch() []string {
	return []string{
		types.ScanNetworkNodeSubnets,
		types.ScanObservedNodes,
		types.ScanObservedCloudRoutes,
		types.ScanDerivedCloudRoutes,
	}
}

// pendingCloudRouteOperation describes a cloud route operation that must
// execute after a successful CAS commit.
type pendingCloudRouteOperation struct {
	Kind             string `json:"kind"`               // Kind is "ensure" or "delete".
	DestinationCIDR  string `json:"destination_cidr"`   // DestinationCIDR is the route's target CIDR.
	TargetNodeID     string `json:"target_node_id"`     // TargetNodeID is the CCattler node (ensure only).
	TargetInstanceID string `json:"target_instance_id"` // TargetInstanceID is the cloud instance (ensure only).
}

// Reconcile compares desired routes (from node subnet assignments) against
// existing cloud VPC routes and emits pending operations for the post-commit
// executor.
func (cloudRouteController *CloudRouteController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	nodeSubnets := extractNodeSubnetsForRoutes(facts)
	nodeProviderInstances, currentNodeStates := extractNodeProviderAndStates(facts)
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

		pendingOp := pendingCloudRouteOperation{
			Kind:             "ensure",
			DestinationCIDR:  cidr,
			TargetNodeID:     desiredRoute.TargetNodeID,
			TargetInstanceID: desiredRoute.TargetInstanceID,
		}
		pendingJSON, marshalErr := json.Marshal(pendingOp)
		if marshalErr != nil {
			logging.Default().Error("failed to marshal pending route operation",
				"cidr", cidr, "error", marshalErr.Error())
			continue
		}
		proposedChanges = append(proposedChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedCloudRoutePendingOperation(cidr),
			Value: pendingJSON,
		})
	}

	for cidr := range existingRoutes {
		if _, stillDesired := desiredRoutes[cidr]; !stillDesired {
			pendingOp := pendingCloudRouteOperation{
				Kind:            "delete",
				DestinationCIDR: cidr,
			}
			pendingJSON, marshalErr := json.Marshal(pendingOp)
			if marshalErr != nil {
				logging.Default().Error("failed to marshal pending route delete",
					"cidr", cidr, "error", marshalErr.Error())
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedCloudRoutePendingOperation(cidr),
				Value: pendingJSON,
			})
		}
	}

	pendingEnsureRoutes := extractPendingEnsureRoutes(facts)
	for cidr := range pendingEnsureRoutes {
		if _, stillDesired := desiredRoutes[cidr]; !stillDesired {
			if _, hasObservedRoute := existingRoutes[cidr]; hasObservedRoute {
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type: store.OpDelete,
				Key:  types.KeyDerivedCloudRoutePendingOperation(cidr),
			})
		}
	}

	return proposedChanges, nil
}

// ExecutePostCommitOperations reads pending cloud route operations from the
// store and calls the cloud provider. On success the pending key is deleted
// and observed state is written. On failure the pending key is left for retry.
func (cloudRouteController *CloudRouteController) ExecutePostCommitOperations(ctx context.Context) error {
	pendingFacts, scanError := cloudRouteController.factStore.Scan(ctx, types.ScanDerivedCloudRoutes)
	if scanError != nil {
		return scanError
	}

	for _, pendingFact := range pendingFacts {
		if !strings.HasSuffix(pendingFact.Key, "/pending_operation") {
			continue
		}

		var pendingOp pendingCloudRouteOperation
		if unmarshalErr := json.Unmarshal(pendingFact.Value, &pendingOp); unmarshalErr != nil {
			logging.Default().Error("corrupt pending route operation",
				"key", pendingFact.Key, "error", unmarshalErr.Error())
			continue
		}

		switch pendingOp.Kind {
		case "ensure":
			nodeStateFact, nodeStateErr := cloudRouteController.factStore.Get(ctx, types.KeyObservedNodeState(pendingOp.TargetNodeID))
			if nodeStateErr != nil || string(nodeStateFact.Value) != string(types.NodeAlive) {
				logging.Default().Warn("canceling pending route for non-alive node",
					"cidr", pendingOp.DestinationCIDR, "node", pendingOp.TargetNodeID)
				if deleteErr := cloudRouteController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
					logging.Default().Error("failed to clean up stale pending route", "cidr", pendingOp.DestinationCIDR, "error", deleteErr.Error())
				}
				continue
			}
			ensureError := cloudRouteController.cloudProvider.EnsureRoute(ctx, cloud.RouteConfig{
				DestinationCIDR:  pendingOp.DestinationCIDR,
				TargetNodeID:     pendingOp.TargetNodeID,
				TargetInstanceID: pendingOp.TargetInstanceID,
			})
			if ensureError != nil {
				logging.Default().Error("cloud route ensure failed, will retry",
					"cidr", pendingOp.DestinationCIDR, "error", ensureError.Error())
				continue
			}
			if _, putErr := cloudRouteController.factStore.Put(ctx,
				types.KeyObservedCloudRoute(pendingOp.DestinationCIDR),
				[]byte(pendingOp.TargetNodeID)); putErr != nil {
				logging.Default().Error("failed to write observed route", "cidr", pendingOp.DestinationCIDR, "error", putErr.Error())
			}
			if deleteErr := cloudRouteController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
				logging.Default().Error("failed to delete pending route op", "cidr", pendingOp.DestinationCIDR, "error", deleteErr.Error())
			}

		case "delete":
			deleteError := cloudRouteController.cloudProvider.DeleteRoute(ctx, pendingOp.DestinationCIDR)
			if deleteError != nil {
				logging.Default().Error("cloud route delete failed, will retry",
					"cidr", pendingOp.DestinationCIDR, "error", deleteError.Error())
				continue
			}
			if deleteErr := cloudRouteController.factStore.Delete(ctx, types.KeyObservedCloudRoute(pendingOp.DestinationCIDR)); deleteErr != nil {
				logging.Default().Error("failed to delete observed route", "cidr", pendingOp.DestinationCIDR, "error", deleteErr.Error())
			}
			if deleteErr := cloudRouteController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
				logging.Default().Error("failed to delete pending route op", "cidr", pendingOp.DestinationCIDR, "error", deleteErr.Error())
			}

		default:
			logging.Default().Error("unknown pending route operation kind",
				"cidr", pendingOp.DestinationCIDR, "kind", pendingOp.Kind)
		}
	}

	return nil
}

// extractPendingEnsureRoutes returns the set of destination CIDRs that have a
// pending "ensure" operation in the derived cloud route prefix. Used to detect
// orphaned pending operations for routes whose target node is no longer alive.
func extractPendingEnsureRoutes(facts []store.Fact) map[string]bool {
	pendingRoutes := make(map[string]bool)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDerivedCloudRoutes) {
		if !strings.HasSuffix(factEntry.Key, "/pending_operation") {
			continue
		}
		var pendingOp pendingCloudRouteOperation
		if unmarshalErr := json.Unmarshal(factEntry.Value, &pendingOp); unmarshalErr != nil {
			continue
		}
		if pendingOp.Kind == "ensure" {
			pendingRoutes[pendingOp.DestinationCIDR] = true
		}
	}
	return pendingRoutes
}

// extractNodeSubnetsForRoutes builds a map from node ID to assigned subnet CIDR.
func extractNodeSubnetsForRoutes(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanNetworkNodeSubnets, "subnet")
}

// extractObservedCloudRoutes returns a map from destination CIDR to target
// node ID for all cloud routes currently tracked in the store.
func extractObservedCloudRoutes(facts []store.Fact) map[string]string {
	routes := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanObservedCloudRoutes) {
		cidr := strings.TrimPrefix(factEntry.Key, types.ScanObservedCloudRoutes)
		routes[cidr] = string(factEntry.Value)
	}
	return routes
}
