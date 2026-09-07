package controllers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NodeFailureController watches node heartbeat leases and marks nodes as
// unreachable when their lease expires. It also marks all instances placed
// on unreachable nodes as failed, so the failure controller can reschedule them.
type NodeFailureController struct {
	// LeaseTimeout is how long since the last heartbeat before a node is
	// considered unreachable. Defaults to 5 seconds.
	LeaseTimeout time.Duration

	// Now returns the current time. Defaults to time.Now but can be overridden
	// in tests for deterministic behavior.
	Now func() time.Time
}

// NewNodeFailureController returns a NodeFailureController with a 5-second
// default lease timeout and time.Now as the clock source.
func NewNodeFailureController() *NodeFailureController {
	return &NodeFailureController{
		LeaseTimeout: 5 * time.Second,
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

	// Parse lease timestamps: nodeID -> unix-millisecond timestamp of last heartbeat.
	lastHeartbeatMillisByNode := make(map[string]int64)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanLeaseNodes) {
			continue
		}
		nodeID := strings.TrimPrefix(factEntry.Key, types.ScanLeaseNodes)
		if milliTimestamp, parseErr := strconv.ParseInt(string(factEntry.Value), 10, 64); parseErr == nil {
			lastHeartbeatMillisByNode[nodeID] = milliTimestamp
		}
	}

	// Parse current node states: nodeID -> state string (e.g. "alive", "unreachable").
	currentNodeStates := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedNodes) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedNodes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "state" {
			currentNodeStates[pathParts[0]] = string(factEntry.Value)
		}
	}

	// Identify nodes whose lease has expired past the timeout threshold.
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

	var proposedChanges []Change

	// Emit state changes to mark expired nodes as unreachable.
	for nodeID := range unreachableNodeSet {
		if currentNodeStates[nodeID] != string(types.NodeUnreachable) {
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedNodeState(nodeID),
				Value: []byte(string(types.NodeUnreachable)),
			})
		}
	}

	// Parse placements: instanceID -> target nodeID.
	instancePlacementNode := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanPlacements) {
			continue
		}
		instanceID := strings.TrimPrefix(factEntry.Key, types.ScanPlacements)
		instancePlacementNode[instanceID] = string(factEntry.Value)
	}

	// Parse instance states: instanceID -> current lifecycle state.
	currentInstanceStates := make(map[string]string)
	for _, factEntry := range facts {
		if !strings.HasPrefix(factEntry.Key, types.ScanObservedInstances) {
			continue
		}
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) == 2 && pathParts[1] == "state" {
			currentInstanceStates[pathParts[0]] = string(factEntry.Value)
		}
	}

	// Mark running or pending instances on unreachable nodes as failed.
	for instanceID, placedNodeID := range instancePlacementNode {
		if !unreachableNodeSet[placedNodeID] {
			continue
		}
		instanceState := types.InstanceState(currentInstanceStates[instanceID])
		if instanceState == types.InstanceRunning || instanceState == types.InstancePending {
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceState(instanceID),
				Value: []byte(string(types.InstanceFailed)),
			})
		}
	}

	return proposedChanges, nil
}
