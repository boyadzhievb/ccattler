package controllers

import (
	"context"
	"fmt"
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
	failureController.LeaseTimeout = 5 * time.Second
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(990)},  // 10s ago — expired
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
	}

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
	failureController.LeaseTimeout = 5 * time.Second
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(998)},  // 2s ago — fresh
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
	}

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
	failureController.LeaseTimeout = 5 * time.Second
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		// node-1: expired lease, two running instances placed on it.
		{Key: types.KeyLeaseNode("node-1"), Value: millis(990)},
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

	proposedChanges, err := failureController.Reconcile(context.Background(), inputFacts)
	if err != nil {
		t.Fatal(err)
	}

	instancesMarkedFailed := make(map[string]bool)
	for _, change := range proposedChanges {
		if string(change.Value) == "failed" {
			instancesMarkedFailed[change.Key] = true
		}
	}

	if !instancesMarkedFailed[types.KeyObservedInstanceState("aaa")] {
		t.Error("expected instance aaa to be marked failed")
	}
	if !instancesMarkedFailed[types.KeyObservedInstanceState("bbb")] {
		t.Error("expected instance bbb to be marked failed")
	}
	if instancesMarkedFailed[types.KeyObservedInstanceState("ccc")] {
		t.Error("instance ccc on healthy node should not be marked failed")
	}
}

func TestNodeFailureSkipsAlreadyUnreachable(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = 5 * time.Second
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(990)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("unreachable")},
	}

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

func TestNodeFailureSkipsStoppedInstances(t *testing.T) {
	failureController := NewNodeFailureController()
	failureController.LeaseTimeout = 5 * time.Second
	failureController.Now = func() time.Time { return time.Unix(1000, 0) }

	inputFacts := []store.Fact{
		{Key: types.KeyLeaseNode("node-1"), Value: millis(990)},
		{Key: types.KeyObservedNodeState("node-1"), Value: []byte("alive")},
		{Key: types.KeyPlacementInstance("aaa"), Value: []byte("node-1")},
		{Key: types.KeyObservedInstanceState("aaa"), Value: []byte("stopped")},
	}

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
