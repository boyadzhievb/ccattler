package api

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestDiscoverSortedServiceNamesFlat(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredService("api"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("my-api:v3"))

	serviceNames := discoverSortedServiceNamesFromStore(ctx, factStore)

	if len(serviceNames) != 2 {
		t.Fatalf("expected 2 services, got %d: %v", len(serviceNames), serviceNames)
	}
	if serviceNames[0] != "api" || serviceNames[1] != "web" {
		t.Errorf("expected [api web], got %v", serviceNames)
	}
}

func TestDiscoverSortedServiceNamesHierarchical(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredService("payments/checkout"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("payments/checkout"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredService("payments/database"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("payments/database"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredService("frontend/web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("frontend/web"), []byte("nginx:1.27"))

	serviceNames := discoverSortedServiceNamesFromStore(ctx, factStore)

	if len(serviceNames) != 3 {
		t.Fatalf("expected 3 services, got %d: %v", len(serviceNames), serviceNames)
	}
	expectedNames := []string{"frontend/web", "payments/checkout", "payments/database"}
	for i, expectedName := range expectedNames {
		if serviceNames[i] != expectedName {
			t.Errorf("service[%d]: got %q, want %q", i, serviceNames[i], expectedName)
		}
	}
}
