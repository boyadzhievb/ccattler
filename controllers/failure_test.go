package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestFailureReplacesFailedInstance(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "failed"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// 1 stop + 3 create (marker, service, state) = 4
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes, got %d", len(changes))
	}

	// First change: mark failed as stopped.
	if changes[0].Key != types.KeyObservedInstanceState("aaa") {
		t.Errorf("expected state key for aaa, got %s", changes[0].Key)
	}
	if string(changes[0].Value) != "stopped" {
		t.Errorf("expected stopped, got %s", changes[0].Value)
	}

	// Remaining: new pending instance.
	if string(changes[2].Value) != "web" {
		t.Errorf("replacement service: got %s, want web", changes[2].Value)
	}
	if string(changes[3].Value) != "pending" {
		t.Errorf("replacement state: got %s, want pending", changes[3].Value)
	}
}

func TestFailureIgnoresRunning(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for running instance, got %d", len(changes))
	}
}

func TestFailureIgnoresPending(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for pending instance, got %d", len(changes))
	}
}

func TestFailureIgnoresStopped(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "stopped"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for stopped instance, got %d", len(changes))
	}
}

func TestFailureMultipleFailed(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "failed"),
		kv(types.KeyObservedInstanceService("bbb"), "api"),
		kv(types.KeyObservedInstanceState("bbb"), "failed"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// 2 failed × 4 changes each = 8
	if len(changes) != 8 {
		t.Fatalf("expected 8 changes (2 replacements), got %d", len(changes))
	}

	stopped := 0
	pending := 0
	for _, ch := range changes {
		if ch.Type == store.OpPut && string(ch.Value) == "stopped" {
			stopped++
		}
		if ch.Type == store.OpPut && string(ch.Value) == "pending" {
			pending++
		}
	}
	if stopped != 2 {
		t.Errorf("expected 2 stopped, got %d", stopped)
	}
	if pending != 2 {
		t.Errorf("expected 2 pending replacements, got %d", pending)
	}
}

func TestFailureControllerInterface(t *testing.T) {
	failureController := NewFailureController()
	var _ Controller = failureController
	if failureController.Name() != "failure" {
		t.Fatalf("name: got %s, want failure", failureController.Name())
	}
}
