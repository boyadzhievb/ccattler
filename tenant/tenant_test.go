package tenant

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestExtractTenantFromName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"payments/checkout", "payments"},
		{"frontend/web", "frontend"},
		{"platform/dns", "platform"},
		{"web", ""},
		{"", ""},
	}
	for _, testCase := range tests {
		result := ExtractTenantFromName(testCase.name)
		if result != testCase.expected {
			t.Errorf("ExtractTenantFromName(%q) = %q, want %q", testCase.name, result, testCase.expected)
		}
	}
}

func TestTenantDSLParsing(t *testing.T) {
	input := `tenant payments {
  quota {
    cpu 100
    memory 256Gi
    instances 500
    volumes 50
    storage 10Ti
  }
  weight 3
}`

	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(file.Tenants) != 1 {
		t.Fatalf("expected 1 tenant, got %d", len(file.Tenants))
	}

	tenantDecl := file.Tenants[0]
	if tenantDecl.Name != "payments" {
		t.Fatalf("expected payments, got %s", tenantDecl.Name)
	}
	if tenantDecl.Weight != 3 {
		t.Fatalf("expected weight 3, got %d", tenantDecl.Weight)
	}
	if tenantDecl.Quota == nil {
		t.Fatal("expected quota block")
	}
	if tenantDecl.Quota.CPU != 100 {
		t.Fatalf("expected cpu 100, got %d", tenantDecl.Quota.CPU)
	}
	if tenantDecl.Quota.Memory != "256Gi" {
		t.Fatalf("expected memory 256Gi, got %s", tenantDecl.Quota.Memory)
	}
	if tenantDecl.Quota.Instances != 500 {
		t.Fatalf("expected instances 500, got %d", tenantDecl.Quota.Instances)
	}
	if tenantDecl.Quota.Volumes != 50 {
		t.Fatalf("expected volumes 50, got %d", tenantDecl.Quota.Volumes)
	}
	if tenantDecl.Quota.Storage != "10Ti" {
		t.Fatalf("expected storage 10Ti, got %s", tenantDecl.Quota.Storage)
	}
}

func TestTenantDSLCompilation(t *testing.T) {
	input := `tenant payments {
  quota {
    cpu 100
    instances 500
  }
  weight 3
}`

	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	facts, err := lang.Compile(file)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	expectedKeys := map[string]string{
		types.KeyDesiredTenant("payments"):               "",
		types.KeyDesiredTenantQuotaCPU("payments"):       "100",
		types.KeyDesiredTenantQuotaInstances("payments"): "500",
		types.KeyDesiredTenantWeight("payments"):         "3",
	}

	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	for key, expectedValue := range expectedKeys {
		actualValue, exists := factMap[key]
		if !exists {
			t.Errorf("missing fact: %s", key)
		} else if actualValue != expectedValue {
			t.Errorf("fact %s = %q, want %q", key, actualValue, expectedValue)
		}
	}
}

func TestHierarchicalServiceName(t *testing.T) {
	input := `service payments/checkout {
  image checkout:v1
  instances 2
}`

	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}
	if file.Services[0].Name != "payments/checkout" {
		t.Fatalf("expected payments/checkout, got %s", file.Services[0].Name)
	}

	facts, err := lang.Compile(file)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	ownerKey := types.KeyDesiredServiceOwner("payments/checkout")
	if ownerValue, exists := factMap[ownerKey]; !exists {
		t.Error("missing owner fact for hierarchical service")
	} else if ownerValue != "payments" {
		t.Errorf("owner = %q, want payments", ownerValue)
	}

	imageKey := types.KeyDesiredServiceImage("payments/checkout")
	if _, exists := factMap[imageKey]; !exists {
		t.Error("missing image fact for hierarchical service")
	}
}

func TestServiceWithExplicitOwner(t *testing.T) {
	input := `service web {
  image nginx:1.28
  instances 3
  owner frontend
}`

	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if file.Services[0].Owner != "frontend" {
		t.Fatalf("expected owner frontend, got %s", file.Services[0].Owner)
	}

	facts, err := lang.Compile(file)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	ownerKey := types.KeyDesiredServiceOwner("web")
	if ownerValue := factMap[ownerKey]; ownerValue != "frontend" {
		t.Errorf("owner = %q, want frontend", ownerValue)
	}
}

func TestTenantRegistryResolveTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("web"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)

	// Explicit owner fact.
	owner, err := registry.ResolveTenantForService(ctx, "web")
	if err != nil {
		t.Fatalf("resolve explicit owner: %v", err)
	}
	if owner != "payments" {
		t.Fatalf("expected payments, got %s", owner)
	}

	// Hierarchical name derivation.
	owner, err = registry.ResolveTenantForService(ctx, "frontend/web")
	if err != nil {
		t.Fatalf("resolve hierarchical: %v", err)
	}
	if owner != "frontend" {
		t.Fatalf("expected frontend, got %s", owner)
	}

	// No owner — should fail.
	_, err = registry.ResolveTenantForService(ctx, "orphan")
	if err == nil {
		t.Fatal("expected error for service with no owner")
	}
}

func TestTenantRegistryGetTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaCPU("payments"), []byte("100"))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("500"))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("payments"), []byte("3"))

	registry := NewTenantRegistry(memoryStore)

	tenant, err := registry.GetTenant(ctx, "payments")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if tenant.Name != "payments" {
		t.Fatalf("expected payments, got %s", tenant.Name)
	}
	if tenant.Quota.CPU != 100 {
		t.Fatalf("expected cpu 100, got %d", tenant.Quota.CPU)
	}
	if tenant.Quota.Instances != 500 {
		t.Fatalf("expected instances 500, got %d", tenant.Quota.Instances)
	}
	if tenant.Weight != 3 {
		t.Fatalf("expected weight 3, got %d", tenant.Weight)
	}

	// Non-existent tenant.
	_, err = registry.GetTenant(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
}

func TestTenantRegistryListTenants(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaCPU("payments"), []byte("100"))

	registry := NewTenantRegistry(memoryStore)

	tenants, err := registry.ListTenants(ctx)
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(tenants))
	}
}

func TestQuotaAdmissionInstancesAllowed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("10"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	// 3 existing instances for a payments service.
	memoryStore.Put(ctx, types.KeyObservedInstance("i1"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i1"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i1"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i2"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i2"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i2"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i3"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i3"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i3"), []byte("running"))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	result, err := admission.CheckInstanceAdmission(ctx, "checkout", 5)
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed, got denied: %s", result.Reason)
	}
	if result.Usage != 3 {
		t.Fatalf("expected usage 3, got %d", result.Usage)
	}
}

func TestQuotaAdmissionInstancesDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	// 3 existing instances.
	memoryStore.Put(ctx, types.KeyObservedInstance("i1"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i1"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i1"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i2"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i2"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i2"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i3"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i3"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i3"), []byte("running"))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	// Requesting 3 more would make 6, exceeding quota of 5.
	result, err := admission.CheckInstanceAdmission(ctx, "checkout", 3)
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied, got allowed")
	}
	if result.Usage != 3 {
		t.Fatalf("expected usage 3, got %d", result.Usage)
	}
	if result.Quota != 5 {
		t.Fatalf("expected quota 5, got %d", result.Quota)
	}
}

func TestQuotaAdmissionStoppedInstancesNotCounted(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	// 2 running + 1 stopped.
	memoryStore.Put(ctx, types.KeyObservedInstance("i1"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i1"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i1"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i2"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i2"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i2"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i3"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i3"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i3"), []byte("stopped"))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	// Only 2 active, so requesting 3 more = 5 total, within quota.
	result, err := admission.CheckInstanceAdmission(ctx, "checkout", 3)
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed (stopped instances excluded), got denied: %s", result.Reason)
	}
	if result.Usage != 2 {
		t.Fatalf("expected usage 2, got %d", result.Usage)
	}
}

func TestQuotaAdmissionNoQuota(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	result, err := admission.CheckInstanceAdmission(ctx, "checkout", 1000)
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected allowed (no quota), got denied: %s", result.Reason)
	}
}

func TestQuotaAdmissionNoTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	result, err := admission.CheckInstanceAdmission(ctx, "orphan-service", 10)
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if !result.Allowed {
		t.Fatal("expected allowed (no tenant), got denied")
	}
}

func TestQuotaAdmissionVolumesDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaVolumes("payments"), []byte("2"))

	// 2 existing volumes with hierarchical names under payments/.
	memoryStore.Put(ctx, types.KeyDesiredVolume("payments/db"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredVolume("payments/cache"), []byte(""))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	result, err := admission.CheckVolumeAdmission(ctx, "payments")
	if err != nil {
		t.Fatalf("admission check: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected denied (volume quota reached), got allowed")
	}
}

func TestQuotaUpdateUsageFacts(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("100"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	memoryStore.Put(ctx, types.KeyObservedInstance("i1"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i1"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i1"), []byte("running"))
	memoryStore.Put(ctx, types.KeyObservedInstance("i2"), []byte(""))
	memoryStore.Put(ctx, types.KeyObservedInstanceService("i2"), []byte("checkout"))
	memoryStore.Put(ctx, types.KeyObservedInstanceState("i2"), []byte("running"))

	registry := NewTenantRegistry(memoryStore)
	admission := NewQuotaAdmission(memoryStore, registry)

	admission.UpdateUsageFacts(ctx, "payments")

	usageFact, err := memoryStore.Get(ctx, types.KeyObservedTenantUsageInstances("payments"))
	if err != nil {
		t.Fatalf("get usage fact: %v", err)
	}
	if string(usageFact.Value) != "2" {
		t.Fatalf("expected usage 2, got %s", string(usageFact.Value))
	}
}

func TestFullTenantAndServiceDSL(t *testing.T) {
	input := `tenant payments {
  quota {
    instances 100
  }
}

service payments/checkout {
  image checkout:v1
  instances 3
}

service payments/database {
  image postgres:16
  instances 1
}`

	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	err := lang.Apply(ctx, memoryStore, input)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	registry := NewTenantRegistry(memoryStore)

	// Tenant should exist.
	tenant, err := registry.GetTenant(ctx, "payments")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if tenant.Quota.Instances != 100 {
		t.Fatalf("expected instances quota 100, got %d", tenant.Quota.Instances)
	}

	// Both services should resolve to payments tenant.
	owner1, _ := registry.ResolveTenantForService(ctx, "payments/checkout")
	if owner1 != "payments" {
		t.Fatalf("checkout owner = %q, want payments", owner1)
	}
	owner2, _ := registry.ResolveTenantForService(ctx, "payments/database")
	if owner2 != "payments" {
		t.Fatalf("database owner = %q, want payments", owner2)
	}
}
