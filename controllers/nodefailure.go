// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultNodeFailureLeaseTimeout is the duration since the last heartbeat
// before a node is considered unreachable by the failure controller. Set to
// 30 seconds to tolerate real-world operations like container image pulls,
// network configuration, and cgroup setup that can take tens of seconds.
const defaultNodeFailureLeaseTimeout = 30 * time.Second

// defaultMaxTotalChangesPerCycle is the total change budget for the
// NodeFailureController. Node state changes are emitted first (capped at
// defaultMaxNodeStateChangesPerCycle), and the remaining budget is used
// for instance state changes. This ensures the combined output never
// exceeds the runner's 60-change transaction limit.
const defaultMaxTotalChangesPerCycle = 58

// defaultMaxNodeStateChangesPerCycle limits how many node state transitions
// (alive→unreachable) the controller emits per cycle. Node state changes
// take priority because detecting unreachable nodes is prerequisite to
// marking their instances as failed.
const defaultMaxNodeStateChangesPerCycle = 12

// NodeFailureController watches node heartbeat leases and marks nodes as
// unreachable when their lease expires. It also marks all instances placed
// on unreachable nodes as failed, so the failure controller can reschedule them.
type NodeFailureController struct {
	// LeaseTimeout is how long since the last heartbeat before a node is
	// considered unreachable. Defaults to 30 seconds.
	LeaseTimeout time.Duration

	// Now returns the current time. Defaults to time.Now but can be overridden
	// in tests for deterministic behavior.
	Now func() time.Time

	// MaxTotalChangesPerCycle is the total change budget for this controller.
	// Node state changes consume from this budget first, and the remainder
	// is available for instance state changes.
	MaxTotalChangesPerCycle int

	// MaxNodeStateChangesPerCycle limits how many node state transitions
	// are emitted per cycle. Excess unreachable nodes are detected on the
	// next cycle via watch re-trigger.
	MaxNodeStateChangesPerCycle int
}

// NewNodeFailureController returns a NodeFailureController with a 30-second
// default lease timeout and time.Now as the clock source.
func NewNodeFailureController() *NodeFailureController {
	return &NodeFailureController{
		LeaseTimeout:                defaultNodeFailureLeaseTimeout,
		Now:                         time.Now,
		MaxTotalChangesPerCycle:     defaultMaxTotalChangesPerCycle,
		MaxNodeStateChangesPerCycle: defaultMaxNodeStateChangesPerCycle,
	}
}

// Name returns "node-failure", identifying this controller in logs and
// runner bookkeeping.
func (nodeFailureController *NodeFailureController) Name() string { return "node-failure" }

// Watch returns the fact prefixes this controller needs: lease timestamps,
// observed node states, observed and derived instance facts, and placements.
func (nodeFailureController *NodeFailureController) Watch() []string {
	return []string{
		types.ScanLeaseNodes,
		types.ScanObservedNodes,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanPlacements,
	}
}

// Reconcile examines heartbeat lease timestamps against the current time.
// For each node whose lease has expired beyond LeaseTimeout, it emits a
// change to mark the node as unreachable. It then scans placements and
// marks any running or pending instances on those unreachable nodes as
// failed, triggering the failure controller's replacement pipeline.
func (nodeFailureController *NodeFailureController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	currentTime := nodeFailureController.Now()

	lastHeartbeatMillisByNode := parseNodeLeaseTimestamps(facts)
	currentNodeStates := parseCurrentNodeStates(facts)

	unreachableNodeSet, nodeStateChanges := nodeFailureController.identifyUnreachableNodes(
		currentTime, lastHeartbeatMillisByNode, currentNodeStates,
	)

	// Cap node state changes to their budget.
	if len(nodeStateChanges) > nodeFailureController.MaxNodeStateChangesPerCycle {
		nodeStateChanges = nodeStateChanges[:nodeFailureController.MaxNodeStateChangesPerCycle]
	}

	// Remaining budget: failure markers + cleanup of stale markers.
	remainingBudget := nodeFailureController.MaxTotalChangesPerCycle - len(nodeStateChanges)
	if remainingBudget < 0 {
		remainingBudget = 0
	}

	instanceFailureChanges := markInstancesOnUnreachableNodesAsFailed(
		facts, unreachableNodeSet, remainingBudget,
	)

	cleanupBudget := remainingBudget - len(instanceFailureChanges)
	if cleanupBudget < 0 {
		cleanupBudget = 0
	}
	cleanupChanges := cleanupStaleNodeFailureMarkers(facts, unreachableNodeSet, cleanupBudget)

	var allChanges []Change
	allChanges = append(allChanges, nodeStateChanges...)
	allChanges = append(allChanges, instanceFailureChanges...)
	allChanges = append(allChanges, cleanupChanges...)
	return allChanges, nil
}

// parseNodeLeaseTimestamps extracts the last heartbeat unix-millisecond
// timestamp for each node from lease/ prefix facts.
func parseNodeLeaseTimestamps(facts []store.Fact) map[string]int64 {
	lastHeartbeatMillisByNode := make(map[string]int64)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanLeaseNodes) {
		nodeID := strings.TrimPrefix(factEntry.Key, types.ScanLeaseNodes)
		milliTimestamp, parseErr := strconv.ParseInt(string(factEntry.Value), 10, 64)
		if parseErr != nil {
			logging.Default().Warn("corrupt lease timestamp, node invisible to failure detection",
				"node", nodeID, "value", string(factEntry.Value))
			continue
		}
		lastHeartbeatMillisByNode[nodeID] = milliTimestamp
	}
	return lastHeartbeatMillisByNode
}

// parseCurrentNodeStates extracts the current state string (e.g. "alive",
// "unreachable") for each node from observed/node/ prefix facts.
func parseCurrentNodeStates(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedNodes, "state")
}

// identifyUnreachableNodes examines each node's heartbeat timestamp against
// the current time and the configured lease timeout. Returns the set of
// unreachable node IDs and changes to mark newly-unreachable nodes.
func (nodeFailureController *NodeFailureController) identifyUnreachableNodes(
	currentTime time.Time,
	lastHeartbeatMillisByNode map[string]int64,
	currentNodeStates map[string]string,
) (map[string]bool, []Change) {
	unreachableNodeSet := make(map[string]bool)
	var recoveredNodeIDs []string
	for nodeID, heartbeatMillis := range lastHeartbeatMillisByNode {
		nodeState := currentNodeStates[nodeID]
		heartbeatTime := time.UnixMilli(heartbeatMillis)
		timeSinceLastHeartbeat := currentTime.Sub(heartbeatTime)
		heartbeatIsFresh := timeSinceLastHeartbeat <= nodeFailureController.LeaseTimeout

		if nodeState == string(types.NodeUnreachable) && heartbeatIsFresh {
			recoveredNodeIDs = append(recoveredNodeIDs, nodeID)
			continue
		}
		if nodeState == string(types.NodeUnreachable) {
			unreachableNodeSet[nodeID] = true
			continue
		}
		if nodeState != string(types.NodeAlive) {
			continue
		}
		if !heartbeatIsFresh {
			unreachableNodeSet[nodeID] = true
		}
	}

	sort.Strings(recoveredNodeIDs)
	var nodeStateChanges []Change
	for _, nodeID := range recoveredNodeIDs {
		nodeStateChanges = append(nodeStateChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedNodeState(nodeID),
			Value: []byte(string(types.NodeAlive)),
		})
	}
	for nodeID := range unreachableNodeSet {
		if currentNodeStates[nodeID] != string(types.NodeUnreachable) {
			nodeStateChanges = append(nodeStateChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedNodeState(nodeID),
				Value: []byte(string(types.NodeUnreachable)),
			})
		}
	}

	return unreachableNodeSet, nodeStateChanges
}

// markInstancesOnUnreachableNodesAsFailed emits derived/instance/{id}/node_failure
// markers for running/pending/starting instances on unreachable nodes.
// maxChanges caps the number of markers per cycle.
func markInstancesOnUnreachableNodesAsFailed(facts []store.Fact, unreachableNodeSet map[string]bool, maxChanges int) []Change {
	instancePlacementNode := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(factEntry.Key, types.ScanPlacements)
		instancePlacementNode[instanceID] = string(factEntry.Value)
	}

	instanceFields := parseInstanceFieldsFromFacts(facts)

	// Collect affected instance IDs and sort for deterministic output.
	var affectedInstanceIDs []string
	for instanceID, placedNodeID := range instancePlacementNode {
		if !unreachableNodeSet[placedNodeID] {
			continue
		}
		fields := instanceFields[instanceID]
		if fields == nil {
			continue
		}
		instanceState := effectiveInstanceState(fields)
		if instanceState == types.InstanceRunning || instanceState == types.InstancePending || instanceState == types.InstanceStarting {
			affectedInstanceIDs = append(affectedInstanceIDs, instanceID)
		}
	}
	sort.Strings(affectedInstanceIDs)

	if len(affectedInstanceIDs) > maxChanges {
		affectedInstanceIDs = affectedInstanceIDs[:maxChanges]
	}

	instanceFailureChanges := make([]Change, 0, len(affectedInstanceIDs))
	for _, instanceID := range affectedInstanceIDs {
		instanceFailureChanges = append(instanceFailureChanges, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceNodeFailure(instanceID),
			Value: []byte("true"),
		})
	}

	return instanceFailureChanges
}

// cleanupStaleNodeFailureMarkers deletes derived/instance/{id}/node_failure
// markers for instances whose node is no longer unreachable.
func cleanupStaleNodeFailureMarkers(facts []store.Fact, unreachableNodeSet map[string]bool, maxChanges int) []Change {
	instancePlacementNode := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(factEntry.Key, types.ScanPlacements)
		instancePlacementNode[instanceID] = string(factEntry.Value)
	}

	var staleInstanceIDs []string
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDerivedInstances) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDerivedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 || parts[1] != "node_failure" {
			continue
		}
		instanceID := parts[0]
		placedNode := instancePlacementNode[instanceID]
		if placedNode != "" && !unreachableNodeSet[placedNode] {
			staleInstanceIDs = append(staleInstanceIDs, instanceID)
		}
	}
	sort.Strings(staleInstanceIDs)

	if len(staleInstanceIDs) > maxChanges {
		staleInstanceIDs = staleInstanceIDs[:maxChanges]
	}

	changes := make([]Change, 0, len(staleInstanceIDs))
	for _, instanceID := range staleInstanceIDs {
		changes = append(changes, Change{
			Type: store.OpDelete,
			Key:  types.KeyDerivedInstanceNodeFailure(instanceID),
		})
	}
	return changes
}
