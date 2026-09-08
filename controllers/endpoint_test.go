package controllers

import (
	"context"
	"sort"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestEndpointCreatedForRunningInstance(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceIP("aaa"), "10.0.1.4"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(changes))
	}
	if changes[0].Type != store.OpPut {
		t.Fatalf("expected OpPut, got %d", changes[0].Type)
	}
	if changes[0].Key != types.KeyEndpoint("web", "aaa") {
		t.Errorf("key: got %s, want %s", changes[0].Key, types.KeyEndpoint("web", "aaa"))
	}
	if string(changes[0].Value) != "10.0.1.4:8080" {
		t.Errorf("value: got %s, want 10.0.1.4:8080", changes[0].Value)
	}
}

func TestEndpointNotCreatedForPending(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 endpoints for pending instance, got %d", len(changes))
	}
}

func TestEndpointNotCreatedWithoutIP(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 endpoints without IP, got %d", len(changes))
	}
}

func TestEndpointNotCreatedWithoutExpose(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceIP("aaa"), "10.0.1.4"),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 endpoints without expose, got %d", len(changes))
	}
}

func TestStaleEndpointRemoved(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		// Instance is now stopped, but endpoint still exists.
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "stopped"),
		kv(types.KeyObservedInstanceIP("aaa"), "10.0.1.4"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
		kv(types.KeyEndpoint("web", "aaa"), "10.0.1.4:8080"),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 delete, got %d", len(changes))
	}
	if changes[0].Type != store.OpDelete {
		t.Fatalf("expected OpDelete, got %d", changes[0].Type)
	}
	if changes[0].Key != types.KeyEndpoint("web", "aaa") {
		t.Errorf("key: got %s, want %s", changes[0].Key, types.KeyEndpoint("web", "aaa"))
	}
}

func TestEndpointMultipleInstances(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceIP("aaa"), "10.0.1.4"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyObservedInstanceIP("bbb"), "10.0.2.8"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 endpoints (running only), got %d", len(changes))
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	if changes[0].Key != types.KeyEndpoint("web", "aaa") {
		t.Errorf("first: got %s", changes[0].Key)
	}
	if changes[1].Key != types.KeyEndpoint("web", "bbb") {
		t.Errorf("second: got %s", changes[1].Key)
	}
}

func TestEndpointAlreadyExists(t *testing.T) {
	endpointController := NewEndpointController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceIP("aaa"), "10.0.1.4"),
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
		kv(types.KeyEndpoint("web", "aaa"), "10.0.1.4:8080"),
	)

	changes, err := endpointController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes when endpoint exists, got %d", len(changes))
	}
}

func TestEndpointControllerInterface(t *testing.T) {
	endpointController := NewEndpointController()
	var _ Controller = endpointController
	if endpointController.Name() != "endpoint" {
		t.Fatalf("name: got %s, want endpoint", endpointController.Name())
	}
}
