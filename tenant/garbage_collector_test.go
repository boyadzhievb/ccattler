package tenant

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestGarbageCollectorCollectsTenantHierarchy(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	// Create a tenant with services, instances, endpoints, and placements.
	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte("active"))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaCPU("payments"), []byte("100"))

	// Service owned by payments.
	memoryStore.Put(ctx, types.KeyDesiredService("payments/checkout"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))

	// Instance belonging to that service.
	memoryStore.Put(ctx, types.KeyObservedInstanceService("inst-1"), []byte("payments/checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("inst-1"), []byte("running"))

	// Endpoint for the service.
	memoryStore.Put(ctx, types.KeyEndpoint("payments/checkout", "inst-1", 8080), []byte("10.0.1.4:8080"))

	// Placement for the instance.
	memoryStore.Put(ctx, types.KeyPlacementInstance("inst-1"), []byte("node-1"))

	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, "payments")
	if collectError != nil {
		t.Fatalf("collect: %v", collectError)
	}

	if len(resourceLevels) != 4 {
		t.Fatalf("expected 4 BFS levels, got %d", len(resourceLevels))
	}

	// Level 0: tenant root.
	if resourceLevels[0][0].resourceType != "tenant" {
		t.Errorf("level 0: expected tenant, got %s", resourceLevels[0][0].resourceType)
	}

	// Level 1: service.
	foundService := false
	for _, node := range resourceLevels[1] {
		if node.resourceType == "service" && node.resourceName == "payments/checkout" {
			foundService = true
		}
	}
	if !foundService {
		t.Error("level 1: expected to find service payments/checkout")
	}

	// Level 2: instance.
	foundInstance := false
	for _, node := range resourceLevels[2] {
		if node.resourceType == "instance" && node.resourceName == "inst-1" {
			foundInstance = true
		}
	}
	if !foundInstance {
		t.Error("level 2: expected to find instance inst-1")
	}

	// Level 3: endpoint + placement.
	foundEndpoint := false
	foundPlacement := false
	for _, node := range resourceLevels[3] {
		if node.resourceType == "endpoint" {
			foundEndpoint = true
		}
		if node.resourceType == "placement" {
			foundPlacement = true
		}
	}
	if !foundEndpoint {
		t.Error("level 3: expected to find endpoint")
	}
	if !foundPlacement {
		t.Error("level 3: expected to find placement")
	}
}

func TestGarbageCollectorDeletesInReverseOrder(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	// Set up a full hierarchy.
	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte("active"))
	memoryStore.Put(ctx, types.KeyDesiredService("payments/api"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/api"), []byte("api:v2"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/api"), []byte("payments"))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("inst-a"), []byte("payments/api"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("inst-a"), []byte("running"))
	memoryStore.Put(ctx, types.KeyEndpoint("payments/api", "inst-a", 8080), []byte("10.0.1.1:8080"))
	memoryStore.Put(ctx, types.KeyPlacementInstance("inst-a"), []byte("node-1"))

	result, deleteError := garbageCollector.DeleteTenantResources(ctx, "payments")
	if deleteError != nil {
		t.Fatalf("delete: %v", deleteError)
	}

	if result.TotalKeysDeleted == 0 {
		t.Fatal("expected keys to be deleted")
	}

	// Verify everything is gone.
	if _, err := memoryStore.Get(ctx, types.KeyDesiredTenant("payments")); err == nil {
		t.Error("tenant marker should be deleted")
	}
	if _, err := memoryStore.Get(ctx, types.KeyDesiredServiceImage("payments/api")); err == nil {
		t.Error("service image should be deleted")
	}
	if _, err := memoryStore.Get(ctx, types.KeyObservedInstanceState("inst-a")); err == nil {
		t.Error("instance state should be deleted")
	}
	if _, err := memoryStore.Get(ctx, types.KeyEndpoint("payments/api", "inst-a", 8080)); err == nil {
		t.Error("endpoint should be deleted")
	}
	if _, err := memoryStore.Get(ctx, types.KeyPlacementInstance("inst-a")); err == nil {
		t.Error("placement should be deleted")
	}
}

func TestGarbageCollectorNonexistentTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, "nonexistent")
	if collectError != nil {
		t.Fatalf("unexpected error: %v", collectError)
	}
	if resourceLevels != nil {
		t.Errorf("expected nil for nonexistent tenant, got %d levels", len(resourceLevels))
	}
}

func TestGarbageCollectorTenantWithNoChildren(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	// Tenant exists but has no services or volumes.
	memoryStore.Put(ctx, types.KeyDesiredTenant("empty"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("empty"), []byte("active"))

	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, "empty")
	if collectError != nil {
		t.Fatalf("collect: %v", collectError)
	}

	if len(resourceLevels) != 1 {
		t.Fatalf("expected 1 BFS level (tenant root only), got %d", len(resourceLevels))
	}
	if resourceLevels[0][0].resourceType != "tenant" {
		t.Errorf("expected tenant root, got %s", resourceLevels[0][0].resourceType)
	}

	// Delete should clean up just the tenant root.
	result, deleteError := garbageCollector.DeleteTenantResources(ctx, "empty")
	if deleteError != nil {
		t.Fatalf("delete: %v", deleteError)
	}
	if result.TotalKeysDeleted < 2 {
		t.Errorf("expected at least 2 keys deleted (marker + state), got %d", result.TotalKeysDeleted)
	}
}

func TestGarbageCollectorMultipleServicesAndInstances(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	// Tenant with two services.
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("frontend"), []byte("active"))

	memoryStore.Put(ctx, types.KeyDesiredService("frontend/web"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("frontend/web"), []byte("nginx:1.27"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/web"), []byte("frontend"))

	memoryStore.Put(ctx, types.KeyDesiredService("frontend/api"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("frontend/api"), []byte("api:v3"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/api"), []byte("frontend"))

	// Instances for web.
	memoryStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("frontend/web"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("web-2"), []byte("frontend/web"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("web-2"), []byte("running"))

	// Instance for api.
	memoryStore.Put(ctx, types.KeyObservedInstanceService("api-1"), []byte("frontend/api"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("api-1"), []byte("running"))

	// Unrelated service should NOT be collected.
	memoryStore.Put(ctx, types.KeyDesiredService("backend/worker"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("backend/worker"), []byte("backend"))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("worker-1"), []byte("backend/worker"))

	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, "frontend")
	if collectError != nil {
		t.Fatalf("collect: %v", collectError)
	}

	// Level 1: 2 services.
	serviceCount := 0
	for _, node := range resourceLevels[1] {
		if node.resourceType == "service" {
			serviceCount++
		}
	}
	if serviceCount != 2 {
		t.Errorf("expected 2 services at level 1, got %d", serviceCount)
	}

	// Level 2: 3 instances.
	instanceCount := 0
	for _, node := range resourceLevels[2] {
		if node.resourceType == "instance" {
			instanceCount++
		}
	}
	if instanceCount != 3 {
		t.Errorf("expected 3 instances at level 2, got %d", instanceCount)
	}

	// Delete and verify unrelated service is untouched.
	_, deleteError := garbageCollector.DeleteTenantResources(ctx, "frontend")
	if deleteError != nil {
		t.Fatalf("delete: %v", deleteError)
	}

	// Unrelated service should still exist.
	if _, err := memoryStore.Get(ctx, types.KeyDesiredService("backend/worker")); err != nil {
		t.Error("unrelated service should not be deleted")
	}
	if _, err := memoryStore.Get(ctx, types.KeyObservedInstanceService("worker-1")); err != nil {
		t.Error("unrelated instance should not be deleted")
	}
}

func TestGarbageCollectorIncludesInfraFacts(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	garbageCollector := NewGarbageCollector(memoryStore, registry)

	// Create tenant with infrastructure facts (as CreateTenant does).
	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("platform"), []byte("active"))
	memoryStore.Put(ctx, "tenant/platform/network/boundary", []byte("isolated"))
	memoryStore.Put(ctx, "tenant/platform/secrets/namespace", []byte("reserved"))
	memoryStore.Put(ctx, "tenant/platform/audit/stream", []byte("active"))

	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, "platform")
	if collectError != nil {
		t.Fatalf("collect: %v", collectError)
	}

	// Tenant root should include infra facts.
	tenantNode := resourceLevels[0][0]
	if len(tenantNode.factKeys) < 5 {
		t.Errorf("expected at least 5 keys (marker + state + 3 infra), got %d", len(tenantNode.factKeys))
	}

	// Delete should remove infra facts.
	garbageCollector.DeleteTenantResources(ctx, "platform")
	if _, err := memoryStore.Get(ctx, "tenant/platform/network/boundary"); err == nil {
		t.Error("network boundary should be deleted")
	}
	if _, err := memoryStore.Get(ctx, "tenant/platform/secrets/namespace"); err == nil {
		t.Error("secret namespace should be deleted")
	}
}
