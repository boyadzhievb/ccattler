package network

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestRoundRobinDistributesEvenly verifies that 9 requests across 3 backends
// are distributed 3-3-3 using round-robin.
func TestRoundRobinDistributesEvenly(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "bbb"), []byte("10.100.1.3:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "ccc"), []byte("10.100.2.2:8080"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	for requestIndex := 0; requestIndex < 9; requestIndex++ {
		_, err := simulatorProxy.RouteRequest(ctx, "web")
		if err != nil {
			t.Fatalf("request %d failed: %v", requestIndex, err)
		}
	}

	// Count how many times each instance was selected.
	routingDecisions := simulatorProxy.RoutingDecisionsForService("web")
	instanceHitCounts := make(map[string]int)
	for _, selectedEndpoint := range routingDecisions {
		instanceHitCounts[selectedEndpoint.InstanceID]++
	}

	for _, instanceID := range []string{"aaa", "bbb", "ccc"} {
		if instanceHitCounts[instanceID] != 3 {
			t.Errorf("instance %s got %d requests, want 3", instanceID, instanceHitCounts[instanceID])
		}
	}
}

// TestSingleBackendReceivesAllRequests verifies that when a service has only
// one endpoint, all requests go to it.
func TestSingleBackendReceivesAllRequests(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("api", "aaa"), []byte("10.100.1.2:3000"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	for requestIndex := 0; requestIndex < 5; requestIndex++ {
		selectedEndpoint, err := simulatorProxy.RouteRequest(ctx, "api")
		if err != nil {
			t.Fatalf("request %d failed: %v", requestIndex, err)
		}
		if selectedEndpoint.InstanceID != "aaa" {
			t.Errorf("request %d: instance = %s, want aaa", requestIndex, selectedEndpoint.InstanceID)
		}
	}
}

// TestNoBackendsReturnsError verifies that routing to a service with no
// endpoints returns an error.
func TestNoBackendsReturnsError(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	_, err := simulatorProxy.RouteRequest(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for service with no endpoints")
	}
}

// TestBackendDisappearsIsHandled verifies that when a backend endpoint is
// removed, subsequent requests only go to the remaining backends.
func TestBackendDisappearsIsHandled(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "bbb"), []byte("10.100.1.3:8080"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	// Route a few requests with both backends.
	for requestIndex := 0; requestIndex < 4; requestIndex++ {
		simulatorProxy.RouteRequest(ctx, "web")
	}

	// Remove one backend.
	factStore.Delete(ctx, types.KeyEndpoint("web", "bbb"))

	// Subsequent requests should only go to the remaining backend.
	for requestIndex := 0; requestIndex < 3; requestIndex++ {
		selectedEndpoint, err := simulatorProxy.RouteRequest(ctx, "web")
		if err != nil {
			t.Fatalf("request after removal failed: %v", err)
		}
		if selectedEndpoint.InstanceID != "aaa" {
			t.Errorf("after removal, instance = %s, want aaa", selectedEndpoint.InstanceID)
		}
	}
}

// TestNewBackendAppearsIsIncluded verifies that when a new endpoint is
// added, it starts receiving requests.
func TestNewBackendAppearsIsIncluded(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	// Route with single backend.
	simulatorProxy.RouteRequest(ctx, "web")

	// Add a second backend.
	factStore.Put(ctx, types.KeyEndpoint("web", "bbb"), []byte("10.100.1.3:8080"))

	// Route more requests — both backends should now receive traffic.
	instanceHitCounts := make(map[string]int)
	for requestIndex := 0; requestIndex < 6; requestIndex++ {
		selectedEndpoint, _ := simulatorProxy.RouteRequest(ctx, "web")
		instanceHitCounts[selectedEndpoint.InstanceID]++
	}

	if instanceHitCounts["bbb"] == 0 {
		t.Error("new backend bbb should receive at least one request")
	}
}

// TestMultipleServicesHaveIndependentCounters verifies that round-robin
// counters are independent per service.
func TestMultipleServicesHaveIndependentCounters(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "w1"), []byte("10.100.1.2:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "w2"), []byte("10.100.1.3:8080"))
	factStore.Put(ctx, types.KeyEndpoint("api", "a1"), []byte("10.100.2.2:3000"))
	factStore.Put(ctx, types.KeyEndpoint("api", "a2"), []byte("10.100.2.3:3000"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	// Route 4 requests to web and 4 to api.
	for requestIndex := 0; requestIndex < 4; requestIndex++ {
		simulatorProxy.RouteRequest(ctx, "web")
		simulatorProxy.RouteRequest(ctx, "api")
	}

	// Both services should have 4 decisions each.
	webDecisions := simulatorProxy.RoutingDecisionsForService("web")
	apiDecisions := simulatorProxy.RoutingDecisionsForService("api")

	if len(webDecisions) != 4 {
		t.Errorf("web decisions = %d, want 4", len(webDecisions))
	}
	if len(apiDecisions) != 4 {
		t.Errorf("api decisions = %d, want 4", len(apiDecisions))
	}

	// Each service's backends should each get 2 requests.
	webHits := make(map[string]int)
	for _, decision := range webDecisions {
		webHits[decision.InstanceID]++
	}
	apiHits := make(map[string]int)
	for _, decision := range apiDecisions {
		apiHits[decision.InstanceID]++
	}

	if webHits["w1"] != 2 || webHits["w2"] != 2 {
		t.Errorf("web distribution: w1=%d, w2=%d, want 2,2", webHits["w1"], webHits["w2"])
	}
	if apiHits["a1"] != 2 || apiHits["a2"] != 2 {
		t.Errorf("api distribution: a1=%d, a2=%d, want 2,2", apiHits["a1"], apiHits["a2"])
	}
}

// TestRoutingDecisionsForServiceReturnsCopy verifies that the returned
// decisions slice is a copy, not a reference to the internal state.
func TestRoutingDecisionsForServiceReturnsCopy(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	simulatorProxy := NewSimulatorProxy(storeBackedResolver)

	simulatorProxy.RouteRequest(ctx, "web")
	firstSnapshot := simulatorProxy.RoutingDecisionsForService("web")

	simulatorProxy.RouteRequest(ctx, "web")
	secondSnapshot := simulatorProxy.RoutingDecisionsForService("web")

	if len(firstSnapshot) != 1 {
		t.Errorf("first snapshot should have 1 decision, got %d", len(firstSnapshot))
	}
	if len(secondSnapshot) != 2 {
		t.Errorf("second snapshot should have 2 decisions, got %d", len(secondSnapshot))
	}
}
