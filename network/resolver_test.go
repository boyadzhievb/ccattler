package network

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestResolveEndpointsReturnsAllBackends verifies that the resolver returns
// all endpoint facts for a service.
func TestResolveEndpointsReturnsAllBackends(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "bbb"), []byte("10.100.1.3:8080"))
	factStore.Put(ctx, types.KeyEndpoint("web", "ccc"), []byte("10.100.2.2:8080"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	resolvedEndpoints, err := storeBackedResolver.ResolveEndpoints(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}

	if len(resolvedEndpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(resolvedEndpoints))
	}

	endpointsByInstance := make(map[string]types.Endpoint)
	for _, endpoint := range resolvedEndpoints {
		endpointsByInstance[endpoint.InstanceID] = endpoint
	}

	if endpointsByInstance["aaa"].IP != "10.100.1.2" {
		t.Errorf("aaa IP = %s, want 10.100.1.2", endpointsByInstance["aaa"].IP)
	}
	if endpointsByInstance["bbb"].Port != 8080 {
		t.Errorf("bbb port = %d, want 8080", endpointsByInstance["bbb"].Port)
	}
	if endpointsByInstance["ccc"].Service != "web" {
		t.Errorf("ccc service = %s, want web", endpointsByInstance["ccc"].Service)
	}
}

// TestResolveEndpointsReturnsEmptyForUnknownService verifies that resolving
// a service with no endpoints returns an empty slice, not an error.
func TestResolveEndpointsReturnsEmptyForUnknownService(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	storeBackedResolver := NewStoreBackedResolver(factStore)
	resolvedEndpoints, err := storeBackedResolver.ResolveEndpoints(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolvedEndpoints) != 0 {
		t.Errorf("expected 0 endpoints, got %d", len(resolvedEndpoints))
	}
}

// TestResolveEndpointsReflectsChanges verifies that when endpoints are added
// or removed from the store, subsequent resolutions reflect the new state.
func TestResolveEndpointsReflectsChanges(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	storeBackedResolver := NewStoreBackedResolver(factStore)

	// Initially empty.
	resolvedEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
	if len(resolvedEndpoints) != 0 {
		t.Fatalf("expected 0 initially, got %d", len(resolvedEndpoints))
	}

	// Add an endpoint.
	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))
	resolvedEndpoints, _ = storeBackedResolver.ResolveEndpoints(ctx, "web")
	if len(resolvedEndpoints) != 1 {
		t.Fatalf("expected 1 after add, got %d", len(resolvedEndpoints))
	}

	// Remove it.
	factStore.Delete(ctx, types.KeyEndpoint("web", "aaa"))
	resolvedEndpoints, _ = storeBackedResolver.ResolveEndpoints(ctx, "web")
	if len(resolvedEndpoints) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(resolvedEndpoints))
	}
}

// TestResolveVIPReturnsServiceVIP verifies that the resolver returns the
// correct VIP for a service.
func TestResolveVIPReturnsServiceVIP(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyNetworkVIPService("web"), []byte("10.200.0.1"))

	storeBackedResolver := NewStoreBackedResolver(factStore)
	virtualIP, err := storeBackedResolver.ResolveVIP(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if virtualIP != "10.200.0.1" {
		t.Errorf("VIP = %s, want 10.200.0.1", virtualIP)
	}
}

// TestResolveVIPReturnsErrorForUnknownService verifies that resolving the VIP
// of a service that has no VIP returns an error.
func TestResolveVIPReturnsErrorForUnknownService(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	storeBackedResolver := NewStoreBackedResolver(factStore)
	_, err := storeBackedResolver.ResolveVIP(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for service without VIP")
	}
}

// TestResolveEndpointsDoesNotCrossPollute verifies that resolving one
// service does not include endpoints from another service.
func TestResolveEndpointsDoesNotCrossPollute(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))
	factStore.Put(ctx, types.KeyEndpoint("api", "bbb"), []byte("10.100.1.3:3000"))

	storeBackedResolver := NewStoreBackedResolver(factStore)

	webEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
	if len(webEndpoints) != 1 {
		t.Errorf("web should have 1 endpoint, got %d", len(webEndpoints))
	}
	if webEndpoints[0].InstanceID != "aaa" {
		t.Errorf("web endpoint instance = %s, want aaa", webEndpoints[0].InstanceID)
	}

	apiEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "api")
	if len(apiEndpoints) != 1 {
		t.Errorf("api should have 1 endpoint, got %d", len(apiEndpoints))
	}
	if apiEndpoints[0].InstanceID != "bbb" {
		t.Errorf("api endpoint instance = %s, want bbb", apiEndpoints[0].InstanceID)
	}
}
