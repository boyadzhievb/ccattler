// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// millis converts a unix-second value to a millisecond string for lease timestamps.
func millis(unixSeconds int64) []byte {
	return []byte(fmt.Sprintf("%d", unixSeconds*1000))
}

func TestNodeFailureDetectsExpiredLease(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(960)}, // 10s ago — expired
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	markedUnreachable := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("node-1") && string(change.Value) == "unreachable" {
			markedUnreachable = true
		}
	}
	if !markedUnreachable {
		t.Error("expected node-1 to be marked unreachable")
	}
}

func TestNodeFailureIgnoresFreshLease(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(998)}, // 2s ago — fresh
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	if len(proposedChanges) != 0 {
		t.Errorf("expected no changes for fresh lease, got %d", len(proposedChanges))
	}
}

func TestNodeFailureMarksInstancesAsFailed(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		// node-1: expired lease, two running instances placed on it.
		{Key: types.KeyLeaseNode("node-1"), Value: millis(960)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
		{Key: types.KeyPlacementInstance("aaa"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("aaa"), Value: []byte("running")},
		{Key: types.KeyPlacementInstance("bbb"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("bbb"), Value: []byte("running")},
		// node-2: fresh lease, one running instance — should be unaffected.
		{Key: types.KeyLeaseNode("node-2"), Value: millis(999)},
		{Key: types.KeyObservedNodeState("node-2"), Value: []byte("alive")},
		{Key: types.KeyPlacementInstance("ccc"), Value: []byte("node-2")},
		{Key: types.KeyObservedInstanceState("ccc"), Value: []byte("running")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	nodeFailureMarkers := make(map[string]bool)
	for _, change := range proposedChanges {
		if string(change.Value) == "true" {
			nodeFailureMarkers[change.Key] = true
		}
	}

	if !nodeFailureMarkers[types.KeyDerivedInstanceNodeFailure("aaa")] {
		t.Error("expected instance aaa to be marked failed")
	}
	if !nodeFailureMarkers[types.KeyDerivedInstanceNodeFailure("bbb")] {
		t.Error("expected instance bbb to be marked failed")
	}
	if nodeFailureMarkers[types.KeyDerivedInstanceNodeFailure("ccc")] {
		t.Error("instance ccc on healthy node should not be marked failed")
	}
}

func TestNodeFailureSkipsAlreadyUnreachable(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(960)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("unreachable")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("node-1") {
			t.Error("should not re-mark an already unreachable node")
		}
	}
}

// TestNodeFailureRecoversFreshHeartbeatOnUnreachableNode verifies that a
// node with state "unreachable" but a fresh heartbeat is recognized as
// recovered and marked back to "alive".
func TestNodeFailureRecoversFreshHeartbeatOnUnreachableNode(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		// node-1: unreachable state, but fresh heartbeat (2s ago — within timeout).
		{Key: types.KeyLeaseNode("node-1"), Value: millis(998)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("unreachable")},
		// Instance on node-1 should NOT be marked failed.
		{Key: types.KeyPlacementInstance("aaa"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("aaa"), Value: []byte("running")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	markedAlive := false
	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedNodeState("node-1") && string(change.Value) == "alive" {
			markedAlive = true
		}
		if strings.HasSuffix(change.Key, "/node_failure") {
			t.Error("should not mark instances as failed when node has fresh heartbeat")
		}
	}
	if !markedAlive {
		t.Error("expected node-1 to be marked alive (recovered from unreachable)")
	}
}

func TestNodeFailureSkipsStoppedInstances(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(960)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
		{Key: types.KeyPlacementInstance("aaa"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("aaa"), Value: []byte("stopped")},
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	for _, change := range proposedChanges {
		if change.Key == types.KeyObservedInstanceState("aaa") {
			t.Error("stopped instance should not be marked failed")
		}
	}
}

func TestNodeFailureControllerImplementsInterface(t *testing.T) {
	var _ Controller = NewNodeFailureController()
}

func TestNodeFailureControllerName(t *testing.T) {
	failureController := NewNodeFailureController()
	if failureController.Name() != "node-failure" {
		t.Errorf("name: got %s, want node-failure", failureController.Name())
	}
}

// TestNodeFailureRateLimitsInstanceStateChanges verifies that the controller
// caps instance state changes per cycle within the total budget.
func TestNodeFailureRateLimitsInstanceStateChanges(t *testing.T) {
	nodeFailureController := NewNodeFailureController()
	nodeFailureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	nodeFailureController.Now = func() time.Time { return time.Unix(1000, 0) }
	// Total budget 6: 1 node state change leaves 5 for instances.
	nodeFailureController.MaxTotalChangesPerCycle = 6
	nodeFailureController.MaxNodeStateChangesPerCycle = 12

	var inputFacts []store.Fact
	// One unreachable node with 20 running instances.
	inputFacts = append(inputFacts,
		store.Fact{Key: types.KeyLeaseNode("node-1"), Value: millis(960)},
		store.Fact{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
	)
	for instanceIndex := 0; instanceIndex < 20; instanceIndex++ {
		instanceID := fmt.Sprintf("inst-%03d", instanceIndex)
		inputFacts = append(inputFacts,
			store.Fact{Key: types.KeyPlacementInstance(instanceID), Value: []byte("node-1")},
			store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte("running")},
		)
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := nodeFailureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	nodeFailureMarkerCount := 0
	for _, change := range proposedChanges {
		if strings.Contains(change.Key, "/node_failure") && string(change.Value) == "true" {
			nodeFailureMarkerCount++
		}
	}
	if nodeFailureMarkerCount != 5 {
		t.Fatalf("expected 5 node_failure markers (rate-limited), got %d", nodeFailureMarkerCount)
	}
}

// TestNodeFailureTotalOutputWithinBudget verifies that the total output
// (node state + instance state changes) stays within the budget when many
// nodes fail simultaneously with many instances each.
func TestNodeFailureTotalOutputWithinBudget(t *testing.T) {
	nodeFailureController := NewNodeFailureController()
	nodeFailureController.LeaseTimeout = defaultNodeFailureLeaseTimeout
	nodeFailureController.Now = func() time.Time { return time.Unix(1000, 0) }
	// Use defaults — total budget should cap the combined output.

	var inputFacts []store.Fact
	// 15 unreachable nodes with 10 running instances each.
	for nodeIndex := 0; nodeIndex < 15; nodeIndex++ {
		nodeID := fmt.Sprintf("node-%02d", nodeIndex)
		inputFacts = append(inputFacts,
			store.Fact{Key: types.KeyLeaseNode(nodeID), Value: millis(960)},
			store.Fact{Key: types.KeyObservedNodeState(nodeID), Value: []byte("alive")},
		)
		for instanceIndex := 0; instanceIndex < 10; instanceIndex++ {
			instanceID := fmt.Sprintf("inst-%02d-%03d", nodeIndex, instanceIndex)
			inputFacts = append(inputFacts,
				store.Fact{Key: types.KeyPlacementInstance(instanceID), Value: []byte(nodeID)},
				store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte("running")},
			)
		}
	}

	store.SortFacts(inputFacts)
	proposedChanges, err := nodeFailureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	if len(proposedChanges) > nodeFailureController.MaxTotalChangesPerCycle {
		t.Fatalf("total changes %d exceeds budget %d",
			len(proposedChanges), nodeFailureController.MaxTotalChangesPerCycle)
	}
}

// TestNodeFailureDefaultBudgets verifies the default budget constants.
func TestNodeFailureDefaultBudgets(t *testing.T) {
	nodeFailureController := NewNodeFailureController()
	if nodeFailureController.MaxTotalChangesPerCycle != defaultMaxTotalChangesPerCycle {
		t.Fatalf("expected default total %d, got %d",
			defaultMaxTotalChangesPerCycle, nodeFailureController.MaxTotalChangesPerCycle)
	}
	if nodeFailureController.MaxNodeStateChangesPerCycle != defaultMaxNodeStateChangesPerCycle {
		t.Fatalf("expected default node state %d, got %d",
			defaultMaxNodeStateChangesPerCycle, nodeFailureController.MaxNodeStateChangesPerCycle)
	}
}
