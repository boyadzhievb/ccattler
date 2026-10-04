// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultNodeFailureLeaseTimeout is the duration since the last heartbeat
// before a node is considered unreachable by the failure controller. Set to
// 30 seconds to tolerate real-world operations like container image pulls,
// network configuration, and cgroup setup that can take tens of seconds.
const defaultNodeFailureLeaseTimeout = 30 * time.Second

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
}

// NewNodeFailureController returns a NodeFailureController with a 30-second
// default lease timeout and time.Now as the clock source.
func NewNodeFailureController() *NodeFailureController {
	return &NodeFailureController{
		LeaseTimeout: defaultNodeFailureLeaseTimeout,
		Now:          time.Now,
	}
}

// Name returns "node-failure", identifying this controller in logs and
// runner bookkeeping.
func (nodeFailureController *NodeFailureController) Name() string { return "node-failure" }

// Watch returns the fact prefixes this controller needs: lease timestamps,
// observed node states, observed instance states, and scheduler placements.
func (nodeFailureController *NodeFailureController) Watch() []string {
	return []string{
		types.ScanLeaseNodes,
		types.ScanObservedNodes,
		types.ScanObservedInstances,
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

	instanceFailureChanges := markInstancesOnUnreachableNodesAsFailed(facts, unreachableNodeSet)

	return append(nodeStateChanges, instanceFailureChanges...), nil
}

// parseNodeLeaseTimestamps extracts the last heartbeat unix-millisecond
// timestamp for each node from lease/ prefix facts.
func parseNodeLeaseTimestamps(facts []store.Fact) map[string]int64 {
	lastHeartbeatMillisByNode := make(map[string]int64)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanLeaseNodes) {
		nodeID := strings.TrimPrefix(factEntry.Key, types.ScanLeaseNodes)
		if milliTimestamp, parseErr := strconv.ParseInt(string(factEntry.Value), 10, 64); parseErr == nil {
			lastHeartbeatMillisByNode[nodeID] = milliTimestamp
		}
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
	for nodeID, heartbeatMillis := range lastHeartbeatMillisByNode {
		nodeState := currentNodeStates[nodeID]
		if nodeState == string(types.NodeUnreachable) {
			unreachableNodeSet[nodeID] = true
			continue
		}
		if nodeState != string(types.NodeAlive) {
			continue
		}
		heartbeatTime := time.UnixMilli(heartbeatMillis)
		timeSinceLastHeartbeat := currentTime.Sub(heartbeatTime)
		if timeSinceLastHeartbeat > nodeFailureController.LeaseTimeout {
			unreachableNodeSet[nodeID] = true
		}
	}

	var nodeStateChanges []Change
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

// markInstancesOnUnreachableNodesAsFailed scans placement and instance state
// facts, and for any running, pending, or starting instance placed on an
// unreachable node, emits a change to mark it as failed.
func markInstancesOnUnreachableNodesAsFailed(facts []store.Fact, unreachableNodeSet map[string]bool) []Change {
	instancePlacementNode := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(factEntry.Key, types.ScanPlacements)
		instancePlacementNode[instanceID] = string(factEntry.Value)
	}

	currentInstanceStates := collectStringValuesBySuffix(facts, types.ScanObservedInstances, "state")

	var instanceFailureChanges []Change
	for instanceID, placedNodeID := range instancePlacementNode {
		if !unreachableNodeSet[placedNodeID] {
			continue
		}
		instanceState := types.InstanceState(currentInstanceStates[instanceID])
		if instanceState == types.InstanceRunning || instanceState == types.InstancePending || instanceState == types.InstanceStarting {
			instanceFailureChanges = append(instanceFailureChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceState(instanceID),
				Value: []byte(string(types.InstanceFailed)),
			})
		}
	}

	return instanceFailureChanges
}
