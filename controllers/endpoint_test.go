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

// TestEndpointControllerReadinessGating verifies that when a service has a
// readiness probe configured, only instances whose readiness state is "ready"
// receive endpoints. An instance that is running but not-ready must be excluded.
func TestEndpointControllerReadinessGating(t *testing.T) {
	endpointController := NewEndpointController()

	readyInstanceID := "inst-ready-001"
	notReadyInstanceID := "inst-notready-002"

	factsForReadinessGating := buildFacts(
		// Service "web" exposes port 8080 and has a readiness probe configured.
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
		kv(types.KeyDesiredServiceProbeMethod("web", "readiness"), "http"),

		// First instance: running, has IP, readiness = ready.
		kv(types.KeyObservedInstanceService(readyInstanceID), "web"),
		kv(types.KeyObservedInstanceState(readyInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(readyInstanceID), "10.0.1.10"),
		kv(types.KeyObservedInstanceProbeState(readyInstanceID, "readiness"), string(types.ReadinessProbeReady)),

		// Second instance: running, has IP, readiness = not-ready.
		kv(types.KeyObservedInstanceService(notReadyInstanceID), "web"),
		kv(types.KeyObservedInstanceState(notReadyInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(notReadyInstanceID), "10.0.2.20"),
		kv(types.KeyObservedInstanceProbeState(notReadyInstanceID, "readiness"), string(types.ReadinessProbeNotReady)),
	)

	changes, reconcileError := endpointController.Reconcile(context.Background(), factsForReadinessGating)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	// Only the ready instance should get an endpoint.
	if len(changes) != 1 {
		t.Fatalf("expected 1 endpoint (ready instance only), got %d", len(changes))
	}
	if changes[0].Type != store.OpPut {
		t.Fatalf("expected OpPut, got %d", changes[0].Type)
	}
	expectedEndpointKey := types.KeyEndpoint("web", readyInstanceID)
	if changes[0].Key != expectedEndpointKey {
		t.Errorf("key: got %s, want %s", changes[0].Key, expectedEndpointKey)
	}
	expectedEndpointAddress := "10.0.1.10:8080"
	if string(changes[0].Value) != expectedEndpointAddress {
		t.Errorf("value: got %s, want %s", changes[0].Value, expectedEndpointAddress)
	}
}

// TestEndpointControllerNoReadinessProbe verifies backward-compatible behavior:
// when a service does NOT have a readiness probe configured, all running
// instances with an IP get endpoints regardless of any probe/readiness field.
func TestEndpointControllerNoReadinessProbe(t *testing.T) {
	endpointController := NewEndpointController()

	firstInstanceID := "inst-api-001"
	secondInstanceID := "inst-api-002"

	factsWithoutReadinessProbe := buildFacts(
		// Service "api" exposes port 3000 but has NO readiness probe method configured.
		kv(types.KeyDesiredServiceExpose("api", 3000), ""),

		// First instance: running with IP, no probe/readiness field at all.
		kv(types.KeyObservedInstanceService(firstInstanceID), "api"),
		kv(types.KeyObservedInstanceState(firstInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(firstInstanceID), "10.0.3.30"),

		// Second instance: running with IP, no probe/readiness field at all.
		kv(types.KeyObservedInstanceService(secondInstanceID), "api"),
		kv(types.KeyObservedInstanceState(secondInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(secondInstanceID), "10.0.4.40"),
	)

	changes, reconcileError := endpointController.Reconcile(context.Background(), factsWithoutReadinessProbe)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}

	// Both instances should get endpoints since there is no readiness gate.
	if len(changes) != 2 {
		t.Fatalf("expected 2 endpoints (no readiness gate), got %d", len(changes))
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })

	expectedFirstKey := types.KeyEndpoint("api", firstInstanceID)
	expectedSecondKey := types.KeyEndpoint("api", secondInstanceID)
	if changes[0].Key != expectedFirstKey {
		t.Errorf("first endpoint key: got %s, want %s", changes[0].Key, expectedFirstKey)
	}
	if changes[1].Key != expectedSecondKey {
		t.Errorf("second endpoint key: got %s, want %s", changes[1].Key, expectedSecondKey)
	}
	if string(changes[0].Value) != "10.0.3.30:3000" {
		t.Errorf("first endpoint address: got %s, want 10.0.3.30:3000", changes[0].Value)
	}
	if string(changes[1].Value) != "10.0.4.40:3000" {
		t.Errorf("second endpoint address: got %s, want 10.0.4.40:3000", changes[1].Value)
	}
}

// TestEndpointControllerReadinessBecomesReady verifies that an instance
// initially gated by a not-ready readiness probe gets an endpoint once its
// readiness state transitions to "ready" on a subsequent reconciliation.
func TestEndpointControllerReadinessBecomesReady(t *testing.T) {
	endpointController := NewEndpointController()

	transitioningInstanceID := "inst-transition-001"

	// First reconciliation: instance is not-ready, so no endpoint should be created.
	factsBeforeReadiness := buildFacts(
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
		kv(types.KeyDesiredServiceProbeMethod("web", "readiness"), "http"),
		kv(types.KeyObservedInstanceService(transitioningInstanceID), "web"),
		kv(types.KeyObservedInstanceState(transitioningInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(transitioningInstanceID), "10.0.5.50"),
		kv(types.KeyObservedInstanceProbeState(transitioningInstanceID, "readiness"), string(types.ReadinessProbeNotReady)),
	)

	changesBeforeReadiness, reconcileErrorBefore := endpointController.Reconcile(context.Background(), factsBeforeReadiness)
	if reconcileErrorBefore != nil {
		t.Fatal(reconcileErrorBefore)
	}
	if len(changesBeforeReadiness) != 0 {
		t.Fatalf("expected 0 endpoints for not-ready instance, got %d", len(changesBeforeReadiness))
	}

	// Second reconciliation: instance readiness transitions to "ready".
	factsAfterReadiness := buildFacts(
		kv(types.KeyDesiredServiceExpose("web", 8080), ""),
		kv(types.KeyDesiredServiceProbeMethod("web", "readiness"), "http"),
		kv(types.KeyObservedInstanceService(transitioningInstanceID), "web"),
		kv(types.KeyObservedInstanceState(transitioningInstanceID), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceIP(transitioningInstanceID), "10.0.5.50"),
		kv(types.KeyObservedInstanceProbeState(transitioningInstanceID, "readiness"), string(types.ReadinessProbeReady)),
	)

	changesAfterReadiness, reconcileErrorAfter := endpointController.Reconcile(context.Background(), factsAfterReadiness)
	if reconcileErrorAfter != nil {
		t.Fatal(reconcileErrorAfter)
	}
	if len(changesAfterReadiness) != 1 {
		t.Fatalf("expected 1 endpoint after readiness transition, got %d", len(changesAfterReadiness))
	}

	expectedEndpointKey := types.KeyEndpoint("web", transitioningInstanceID)
	if changesAfterReadiness[0].Type != store.OpPut {
		t.Fatalf("expected OpPut, got %d", changesAfterReadiness[0].Type)
	}
	if changesAfterReadiness[0].Key != expectedEndpointKey {
		t.Errorf("key: got %s, want %s", changesAfterReadiness[0].Key, expectedEndpointKey)
	}
	expectedEndpointAddress := "10.0.5.50:8080"
	if string(changesAfterReadiness[0].Value) != expectedEndpointAddress {
		t.Errorf("value: got %s, want %s", changesAfterReadiness[0].Value, expectedEndpointAddress)
	}
}
