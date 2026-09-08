package tenant

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
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

// --- Fair Scheduling Tests ---

func TestFairSchedulerComputeShares(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	// platform weight=5, payments weight=3, frontend weight=2
	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("platform"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("payments"), []byte("3"))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("frontend"), []byte("2"))

	registry := NewTenantRegistry(memoryStore)
	fairScheduler := NewFairScheduler(memoryStore, registry)

	shares, err := fairScheduler.ComputeFairShares(ctx, 1000)
	if err != nil {
		t.Fatalf("compute shares: %v", err)
	}

	if len(shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(shares))
	}

	shareMap := make(map[string]TenantShare)
	for _, share := range shares {
		shareMap[share.TenantName] = share
	}

	// platform: 5/10 * 1000 = 500
	if shareMap["platform"].GuaranteedCPU != 500 {
		t.Errorf("platform guaranteed CPU = %d, want 500", shareMap["platform"].GuaranteedCPU)
	}
	// payments: 3/10 * 1000 = 300
	if shareMap["payments"].GuaranteedCPU != 300 {
		t.Errorf("payments guaranteed CPU = %d, want 300", shareMap["payments"].GuaranteedCPU)
	}
	// frontend: 2/10 * 1000 = 200
	if shareMap["frontend"].GuaranteedCPU != 200 {
		t.Errorf("frontend guaranteed CPU = %d, want 200", shareMap["frontend"].GuaranteedCPU)
	}
}

func TestFairSchedulerPrioritizesBelowGuarantee(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("payments"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("frontend"), []byte("5"))

	registry := NewTenantRegistry(memoryStore)
	fairScheduler := NewFairScheduler(memoryStore, registry)

	// Neither tenant has any instances, so both are below guarantee.
	priorityPayments := fairScheduler.PrioritizeTenant(ctx, "payments", 1000)
	priorityFrontend := fairScheduler.PrioritizeTenant(ctx, "frontend", 1000)

	// Both should have high priority (above 1000 base).
	if priorityPayments < 1000 {
		t.Errorf("payments priority %d should be >= 1000 (below guarantee)", priorityPayments)
	}
	if priorityFrontend < 1000 {
		t.Errorf("frontend priority %d should be >= 1000 (below guarantee)", priorityFrontend)
	}
}

func TestFairSchedulerBorrowableGuarantees(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("payments"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantWeight("frontend"), []byte("5"))

	// Give payments a service using 600 CPU out of 1000 total (guarantee is 500).
	memoryStore.Put(ctx, types.KeyDesiredService("payments/api"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/api"), []byte("api:v1"))
	memoryStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("payments/api"), []byte("100"))
	memoryStore.Put(ctx, types.KeyDesiredServiceInstances("payments/api"), []byte("6"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/api"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	fairScheduler := NewFairScheduler(memoryStore, registry)

	borrowing, amount := fairScheduler.IsBorrowing(ctx, "payments", 1000)
	if !borrowing {
		t.Fatal("payments should be borrowing (600 > 500 guarantee)")
	}
	if amount != 100 {
		t.Errorf("borrowing amount = %d, want 100", amount)
	}

	// frontend is not borrowing (0 usage, 500 guarantee).
	borrowing, _ = fairScheduler.IsBorrowing(ctx, "frontend", 1000)
	if borrowing {
		t.Fatal("frontend should not be borrowing (0 usage)")
	}
}

func TestFairSchedulerDefaultWeight(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	// Tenant with no explicit weight defaults to 1.
	memoryStore.Put(ctx, types.KeyDesiredTenant("unweighted"), []byte(""))

	registry := NewTenantRegistry(memoryStore)
	fairScheduler := NewFairScheduler(memoryStore, registry)

	shares, _ := fairScheduler.ComputeFairShares(ctx, 1000)
	if len(shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(shares))
	}
	if shares[0].GuaranteedCPU != 1000 {
		t.Errorf("single tenant should get all CPU, got %d", shares[0].GuaranteedCPU)
	}
}

// --- Network Isolation Tests ---

func TestSPIFFEIdentity(t *testing.T) {
	tests := []struct {
		tenant   string
		service  string
		expected string
	}{
		{"payments", "payments/checkout", "spiffe://ccattler/payments/checkout"},
		{"frontend", "frontend/web", "spiffe://ccattler/frontend/web"},
		{"platform", "dns", "spiffe://ccattler/platform/dns"},
	}
	for _, testCase := range tests {
		result := SPIFFEIdentity(testCase.tenant, testCase.service)
		if result != testCase.expected {
			t.Errorf("SPIFFEIdentity(%q, %q) = %q, want %q",
				testCase.tenant, testCase.service, result, testCase.expected)
		}
	}
}

func TestNetworkIsolationSameTenantAllowed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/database"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	policyEngine := security.NewNetworkPolicyEngine(memoryStore)
	isolation := NewTenantNetworkIsolation(memoryStore, registry, policyEngine)

	action, reason, err := isolation.EvaluateTraffic(ctx, "payments/checkout", "payments/database", 5432)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if action != security.PolicyAllow {
		t.Fatalf("same-tenant traffic should be allowed, got %s (%s)", action, reason)
	}
	if reason != "same-tenant" {
		t.Errorf("reason = %q, want same-tenant", reason)
	}
}

func TestNetworkIsolationCrossTenantDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/web"), []byte("frontend"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/database"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	policyEngine := security.NewNetworkPolicyEngine(memoryStore)
	isolation := NewTenantNetworkIsolation(memoryStore, registry, policyEngine)

	action, reason, err := isolation.EvaluateTraffic(ctx, "frontend/web", "payments/database", 5432)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if action != security.PolicyDeny {
		t.Fatalf("cross-tenant traffic should be denied, got %s (%s)", action, reason)
	}
}

func TestNetworkIsolationExplicitCrossTenantAllow(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/web"), []byte("frontend"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	policyEngine := security.NewNetworkPolicyEngine(memoryStore)
	isolation := NewTenantNetworkIsolation(memoryStore, registry, policyEngine)

	// Add explicit cross-tenant allow rule.
	policyEngine.AddRule(ctx, security.NetworkPolicyRule{
		Name:          "frontend-to-checkout",
		SourceService: "frontend/web",
		TargetService: "payments/checkout",
		Port:          443,
		Action:        security.PolicyAllow,
	})

	action, reason, err := isolation.EvaluateTraffic(ctx, "frontend/web", "payments/checkout", 443)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if action != security.PolicyAllow {
		t.Fatalf("explicitly allowed cross-tenant should be allowed, got %s (%s)", action, reason)
	}
	if reason != "explicit-allow-cross-tenant" {
		t.Errorf("reason = %q, want explicit-allow-cross-tenant", reason)
	}
}

func TestNetworkIsolationDeriveFirewallRules(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredService("payments/checkout"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))
	memoryStore.Put(ctx, types.KeyDesiredService("payments/database"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/database"), []byte("postgres:16"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/database"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	policyEngine := security.NewNetworkPolicyEngine(memoryStore)
	isolation := NewTenantNetworkIsolation(memoryStore, registry, policyEngine)

	rules, err := isolation.DeriveFirewallRules(ctx)
	if err != nil {
		t.Fatalf("derive rules: %v", err)
	}

	// 2 services × 1 peer each = 2 same-tenant allow rules.
	sameTenantCount := 0
	for _, rule := range rules {
		if rule.Reason == "same-tenant" && rule.Action == security.PolicyAllow {
			sameTenantCount++
		}
	}
	if sameTenantCount != 2 {
		t.Errorf("expected 2 same-tenant allow rules, got %d", sameTenantCount)
	}
}

// --- Secret Isolation Tests ---

func TestSecretIsolationSameTenantAllowed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	secretStore, _ := security.NewSecretStore(memoryStore, masterKey)

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	tenantSecrets := NewTenantSecretStore(secretStore, registry)

	// Store a payments secret.
	tenantSecrets.PutSecret(ctx, "payments", "db-password", []byte("s3cret"))

	// Same-tenant service can access it.
	plaintext, err := tenantSecrets.GetSecret(ctx, "payments/checkout", "payments", "db-password")
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(plaintext) != "s3cret" {
		t.Fatalf("expected s3cret, got %s", string(plaintext))
	}
}

func TestSecretIsolationCrossTenantDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	secretStore, _ := security.NewSecretStore(memoryStore, masterKey)

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/web"), []byte("frontend"))

	registry := NewTenantRegistry(memoryStore)
	tenantSecrets := NewTenantSecretStore(secretStore, registry)

	tenantSecrets.PutSecret(ctx, "payments", "db-password", []byte("s3cret"))

	// Cross-tenant access should be denied.
	_, err := tenantSecrets.GetSecret(ctx, "frontend/web", "payments", "db-password")
	if err == nil {
		t.Fatal("cross-tenant secret access should be denied")
	}
}

func TestSecretIsolationListForTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	secretStore, _ := security.NewSecretStore(memoryStore, masterKey)

	registry := NewTenantRegistry(memoryStore)
	tenantSecrets := NewTenantSecretStore(secretStore, registry)

	tenantSecrets.PutSecret(ctx, "payments", "db-password", []byte("pw1"))
	tenantSecrets.PutSecret(ctx, "payments", "api-key", []byte("key1"))
	tenantSecrets.PutSecret(ctx, "frontend", "cdn-token", []byte("tok1"))

	paymentSecrets, err := tenantSecrets.ListSecretsForTenant(ctx, "payments")
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(paymentSecrets) != 2 {
		t.Fatalf("expected 2 payments secrets, got %d", len(paymentSecrets))
	}

	frontendSecrets, _ := tenantSecrets.ListSecretsForTenant(ctx, "frontend")
	if len(frontendSecrets) != 1 {
		t.Fatalf("expected 1 frontend secret, got %d", len(frontendSecrets))
	}
}

// --- Shared Service Tests ---

func TestSharedServiceExportAndImport(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("frontend"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/dns"), []byte("platform"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("frontend/web"), []byte("frontend"))

	registry := NewTenantRegistry(memoryStore)
	manager := NewSharedServiceManager(memoryStore, registry)

	// Export platform/dns, allowing frontend and payments.
	err := manager.ExportService(ctx, "platform/dns", []string{"frontend", "payments"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// frontend/web can import platform/dns.
	err = manager.ImportService(ctx, "frontend/web", "platform/dns")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// Verify import recorded.
	imports, _ := manager.ListImportsForService(ctx, "frontend/web")
	if len(imports) != 1 || imports[0] != "platform/dns" {
		t.Fatalf("expected [platform/dns], got %v", imports)
	}
}

func TestSharedServiceImportDeniedWithoutAllow(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenant("unauthorized"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/dns"), []byte("platform"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("unauthorized/app"), []byte("unauthorized"))

	registry := NewTenantRegistry(memoryStore)
	manager := NewSharedServiceManager(memoryStore, registry)

	// Export platform/dns, only allowing frontend.
	manager.ExportService(ctx, "platform/dns", []string{"frontend"})

	// unauthorized/app should be rejected.
	err := manager.ImportService(ctx, "unauthorized/app", "platform/dns")
	if err == nil {
		t.Fatal("import should be denied for unauthorized tenant")
	}
}

func TestSharedServiceSameTenantAlwaysAllowed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/dns"), []byte("platform"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/monitoring"), []byte("platform"))

	registry := NewTenantRegistry(memoryStore)
	manager := NewSharedServiceManager(memoryStore, registry)

	manager.ExportService(ctx, "platform/dns", []string{})

	// Same tenant should always be allowed even with empty allow list.
	allowed, err := manager.IsImportAllowed(ctx, "platform/monitoring", "platform/dns")
	if err != nil {
		t.Fatalf("check import: %v", err)
	}
	if !allowed {
		t.Fatal("same-tenant import should be allowed")
	}
}

func TestSharedServiceGetExported(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/dns"), []byte("platform"))

	registry := NewTenantRegistry(memoryStore)
	manager := NewSharedServiceManager(memoryStore, registry)

	manager.ExportService(ctx, "platform/dns", []string{"frontend", "payments"})

	exported, err := manager.GetExportedService(ctx, "platform/dns")
	if err != nil {
		t.Fatalf("get exported: %v", err)
	}
	if exported.OwnerTenant != "platform" {
		t.Errorf("owner = %q, want platform", exported.OwnerTenant)
	}
	if len(exported.AllowedTenants) != 2 {
		t.Errorf("expected 2 allowed tenants, got %d", len(exported.AllowedTenants))
	}
}

func TestSharedServiceUnexport(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("platform"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("platform/dns"), []byte("platform"))

	registry := NewTenantRegistry(memoryStore)
	manager := NewSharedServiceManager(memoryStore, registry)

	manager.ExportService(ctx, "platform/dns", []string{"frontend"})
	manager.UnexportService(ctx, "platform/dns")

	_, err := manager.GetExportedService(ctx, "platform/dns")
	if err == nil {
		t.Fatal("unexported service should not be found")
	}
}

// --- Tenant Lifecycle Tests ---

func TestTenantLifecycleCreate(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	lifecycle := NewTenantLifecycle(memoryStore, registry)

	result, err := lifecycle.CreateTenant(ctx, "payments", &Quota{
		CPU:       100,
		Instances: 500,
	}, 3)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if result.Name != "payments" {
		t.Errorf("name = %q, want payments", result.Name)
	}
	if !result.QuotaProvisioned {
		t.Error("quota should be provisioned")
	}
	if !result.NetworkBoundary {
		t.Error("network boundary should be provisioned")
	}
	if !result.SecretSpace {
		t.Error("secret space should be provisioned")
	}
	if !result.AuditStream {
		t.Error("audit stream should be provisioned")
	}

	// Verify state.
	state, err := lifecycle.GetTenantState(ctx, "payments")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if state != TenantActive {
		t.Errorf("state = %q, want active", state)
	}

	// Verify tenant is retrievable.
	tenant, err := registry.GetTenant(ctx, "payments")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if tenant.Quota.CPU != 100 {
		t.Errorf("cpu = %d, want 100", tenant.Quota.CPU)
	}
	if tenant.Weight != 3 {
		t.Errorf("weight = %d, want 3", tenant.Weight)
	}
}

func TestTenantLifecycleCreateDuplicate(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	lifecycle := NewTenantLifecycle(memoryStore, registry)

	lifecycle.CreateTenant(ctx, "payments", nil, 0)
	_, err := lifecycle.CreateTenant(ctx, "payments", nil, 0)
	if err == nil {
		t.Fatal("duplicate tenant creation should fail")
	}
}

func TestTenantLifecycleDelete(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	lifecycle := NewTenantLifecycle(memoryStore, registry)

	lifecycle.CreateTenant(ctx, "payments", &Quota{Instances: 100}, 3)

	// Add a service owned by the tenant.
	memoryStore.Put(ctx, types.KeyDesiredService("payments/checkout"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	memoryStore.Put(ctx, types.KeyDesiredServiceInstances("payments/checkout"), []byte("3"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))
	memoryStore.Put(ctx, types.KeyEffectiveServiceInstances("payments/checkout"), []byte("3"))

	// Delete the tenant.
	result, err := lifecycle.DeleteTenant(ctx, "payments")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if result.ServicesDeleted < 1 {
		t.Errorf("expected at least 1 service deleted, got %d", result.ServicesDeleted)
	}

	// Tenant should be gone.
	_, err = registry.GetTenant(ctx, "payments")
	if err == nil {
		t.Fatal("deleted tenant should not be found")
	}

	// Service should be gone.
	_, err = memoryStore.Get(ctx, types.KeyDesiredServiceImage("payments/checkout"))
	if err == nil {
		t.Fatal("service facts should be deleted")
	}

	// Effective state should be gone.
	_, err = memoryStore.Get(ctx, types.KeyEffectiveServiceInstances("payments/checkout"))
	if err == nil {
		t.Fatal("effective facts should be deleted")
	}
}

func TestTenantLifecycleDeleteNonexistent(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	lifecycle := NewTenantLifecycle(memoryStore, registry)

	_, err := lifecycle.DeleteTenant(ctx, "nonexistent")
	if err == nil {
		t.Fatal("deleting nonexistent tenant should fail")
	}
}

func TestSecretIsolationDeleteTenantScoped(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i)
	}
	secretStore, _ := security.NewSecretStore(memoryStore, masterKey)

	registry := NewTenantRegistry(memoryStore)
	tenantSecrets := NewTenantSecretStore(secretStore, registry)

	tenantSecrets.PutSecret(ctx, "payments", "db-password", []byte("pw1"))
	tenantSecrets.DeleteSecret(ctx, "payments", "db-password")

	secrets, _ := tenantSecrets.ListSecretsForTenant(ctx, "payments")
	if len(secrets) != 0 {
		t.Fatalf("expected 0 secrets after delete, got %d", len(secrets))
	}
}

// --- Policy Gate Tests ---

func TestPolicyGateAllowsValidDSL(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("100"))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte(string(TenantActive)))

	registry := NewTenantRegistry(memoryStore)
	quotaAdmission := NewQuotaAdmission(memoryStore, registry)
	auditLog := security.NewInMemoryAuditLog(100)

	gate := NewPolicyGate(memoryStore, registry, quotaAdmission, nil, auditLog)

	dsl := `service payments/checkout {
  image checkout:v1
  instances 3
}`

	result, err := gate.Evaluate(ctx, "user:alice", dsl)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected ALLOW, got DENY at stage %q: %s", result.Stage, result.Reason)
	}
	if result.Stage != "commit" {
		t.Errorf("stage = %q, want commit", result.Stage)
	}
	if len(result.Facts) == 0 {
		t.Error("expected compiled facts")
	}
}

func TestPolicyGateDenySyntaxError(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	auditLog := security.NewInMemoryAuditLog(100)

	gate := NewPolicyGate(memoryStore, registry, nil, nil, auditLog)

	result, err := gate.Evaluate(ctx, "user:alice", "service {{{invalid")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected DENY for syntax error")
	}
	if result.Stage != "syntax" {
		t.Errorf("stage = %q, want syntax", result.Stage)
	}

	entries := auditLog.Entries()
	if len(entries) == 0 {
		t.Fatal("expected audit entry for syntax denial")
	}
	if entries[0].Decision != "DENY" {
		t.Errorf("audit decision = %q, want DENY", entries[0].Decision)
	}
}

func TestPolicyGateDenyRBAC(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	auditLog := security.NewInMemoryAuditLog(100)

	rbac := security.NewRBACAuthorizer()
	rbac.AddRole(security.Role{
		Name: "reader",
		Rules: []security.Rule{
			{KeyPrefix: "/ccattler/", Operations: []security.Permission{security.PermissionRead}},
		},
	})
	rbac.BindRole(security.RoleBinding{Principal: "user:bob", RoleName: "reader"})

	gate := NewPolicyGate(memoryStore, registry, nil, rbac, auditLog)

	dsl := `service web {
  image web:v1
  instances 1
}`

	result, err := gate.Evaluate(ctx, "user:bob", dsl)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected DENY for RBAC violation")
	}
	if result.Stage != "authorization" {
		t.Errorf("stage = %q, want authorization", result.Stage)
	}
}

func TestPolicyGateDenyQuotaExceeded(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("5"))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte(string(TenantActive)))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/checkout"), []byte("payments"))

	// Simulate existing 4 instances.
	memoryStore.Put(ctx, types.KeyDesiredService("payments/existing"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredServiceInstances("payments/existing"), []byte("4"))
	memoryStore.Put(ctx, types.KeyDesiredServiceOwner("payments/existing"), []byte("payments"))

	registry := NewTenantRegistry(memoryStore)
	quotaAdmission := NewQuotaAdmission(memoryStore, registry)
	auditLog := security.NewInMemoryAuditLog(100)

	gate := NewPolicyGate(memoryStore, registry, quotaAdmission, nil, auditLog)

	dsl := `service payments/checkout {
  image checkout:v1
  instances 10
}`

	result, err := gate.Evaluate(ctx, "user:alice", dsl)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected DENY for quota exceeded")
	}
	if result.Stage != "quota" {
		t.Errorf("stage = %q, want quota", result.Stage)
	}
}

func TestPolicyGateDenyDeletingTenant(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte(string(TenantDeleting)))

	registry := NewTenantRegistry(memoryStore)
	auditLog := security.NewInMemoryAuditLog(100)

	gate := NewPolicyGate(memoryStore, registry, nil, nil, auditLog)

	dsl := `service payments/checkout {
  image checkout:v1
  instances 1
}`

	result, err := gate.Evaluate(ctx, "user:alice", dsl)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected DENY for deleting tenant")
	}
	if result.Stage != "security" {
		t.Errorf("stage = %q, want security", result.Stage)
	}
}

func TestPolicyGateEvaluateAndCommit(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, types.KeyDesiredTenant("payments"), []byte(""))
	memoryStore.Put(ctx, types.KeyDesiredTenantQuotaInstances("payments"), []byte("100"))
	memoryStore.Put(ctx, types.KeyDesiredTenantState("payments"), []byte(string(TenantActive)))

	registry := NewTenantRegistry(memoryStore)
	quotaAdmission := NewQuotaAdmission(memoryStore, registry)
	auditLog := security.NewInMemoryAuditLog(100)

	gate := NewPolicyGate(memoryStore, registry, quotaAdmission, nil, auditLog)

	dsl := `service payments/checkout {
  image checkout:v1
  instances 2
}`

	result, err := gate.EvaluateAndCommit(ctx, "user:alice", dsl)
	if err != nil {
		t.Fatalf("evaluate and commit: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected ALLOW, got DENY: %s", result.Reason)
	}

	// Verify facts were committed to the store.
	imageFact, err := memoryStore.Get(ctx, types.KeyDesiredServiceImage("payments/checkout"))
	if err != nil {
		t.Fatalf("committed image fact not found: %v", err)
	}
	if string(imageFact.Value) != "checkout:v1" {
		t.Errorf("image = %q, want checkout:v1", string(imageFact.Value))
	}
}

func TestPolicyGateNoRBACSkipsAuthorization(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	registry := NewTenantRegistry(memoryStore)
	gate := NewPolicyGate(memoryStore, registry, nil, nil, nil)

	dsl := `service web {
  image web:v1
  instances 1
}`

	result, err := gate.Evaluate(ctx, "user:anyone", dsl)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("expected ALLOW when no RBAC configured, got DENY at %q: %s", result.Stage, result.Reason)
	}
}

// --- Tenant Audit View Tests ---

func TestAuditViewEntriesForTenant(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "apply", Target: "/ccattler/desired/service/payments/checkout/image", Decision: "ALLOW"})
	auditLog.Log(security.AuditEntry{Principal: "user:bob", Action: "apply", Target: "/ccattler/desired/service/frontend/web/image", Decision: "ALLOW"})
	auditLog.Log(security.AuditEntry{Principal: "user:carol", Action: "apply", Target: "payments/checkout", Decision: "DENY"})

	paymentsEntries := view.EntriesForTenant("payments")
	if len(paymentsEntries) != 2 {
		t.Fatalf("expected 2 payments entries, got %d", len(paymentsEntries))
	}

	frontendEntries := view.EntriesForTenant("frontend")
	if len(frontendEntries) != 1 {
		t.Fatalf("expected 1 frontend entry, got %d", len(frontendEntries))
	}
}

func TestAuditViewEntriesForPrincipal(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "apply", Target: "payments/checkout", Decision: "ALLOW"})
	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "delete", Target: "payments/checkout", Decision: "DENY"})
	auditLog.Log(security.AuditEntry{Principal: "user:bob", Action: "apply", Target: "frontend/web", Decision: "ALLOW"})

	aliceEntries := view.EntriesForPrincipal("user:alice")
	if len(aliceEntries) != 2 {
		t.Fatalf("expected 2 alice entries, got %d", len(aliceEntries))
	}

	bobEntries := view.EntriesForPrincipal("user:bob")
	if len(bobEntries) != 1 {
		t.Fatalf("expected 1 bob entry, got %d", len(bobEntries))
	}
}

func TestAuditViewDeniedEntries(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "apply", Target: "/ccattler/desired/service/payments/checkout", Decision: "ALLOW"})
	auditLog.Log(security.AuditEntry{Principal: "user:bob", Action: "apply", Target: "/ccattler/desired/service/payments/api", Decision: "DENY"})
	auditLog.Log(security.AuditEntry{Principal: "user:carol", Action: "apply", Target: "frontend/web", Decision: "DENY"})

	// Denied entries for payments.
	paymentsDenied := view.DeniedEntries("payments")
	if len(paymentsDenied) != 1 {
		t.Fatalf("expected 1 payments denied entry, got %d", len(paymentsDenied))
	}

	// All denied entries.
	allDenied := view.DeniedEntries("")
	if len(allDenied) != 2 {
		t.Fatalf("expected 2 total denied entries, got %d", len(allDenied))
	}
}

func TestAuditViewAllEntries(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "admin", Action: "apply", Target: "payments", Decision: "ALLOW"})
	auditLog.Log(security.AuditEntry{Principal: "admin", Action: "apply", Target: "frontend", Decision: "ALLOW"})

	all := view.AllEntries()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}

func TestAuditViewTenantVisibilityByExactName(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "create", Target: "payments", Decision: "ALLOW"})

	entries := view.EntriesForTenant("payments")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for exact tenant name match, got %d", len(entries))
	}
}

func TestAuditViewTenantVisibilityByPrincipal(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "node:payments-node-1", Action: "heartbeat", Target: "/ccattler/lease/node/n1", Decision: "ALLOW"})

	entries := view.EntriesForTenant("payments")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for principal containing tenant name, got %d", len(entries))
	}
}

func TestAuditViewIsolation(t *testing.T) {
	auditLog := security.NewInMemoryAuditLog(100)
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	registry := NewTenantRegistry(memoryStore)

	view := NewTenantAuditView(auditLog, registry)

	auditLog.Log(security.AuditEntry{Principal: "user:alice", Action: "apply", Target: "frontend/web", Decision: "ALLOW"})

	// payments should NOT see frontend entries.
	entries := view.EntriesForTenant("payments")
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for unrelated tenant, got %d", len(entries))
	}
}
