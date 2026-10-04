// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package tenant

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Tenant represents a tenant with its quotas and scheduling weight.
type Tenant struct {
	Name   string // unique tenant identifier
	Quota  Quota  // resource limits for this tenant
	Weight int    // scheduling weight for fair scheduling (higher = more resources)
}

// Quota defines resource limits for a tenant. Zero values mean unlimited.
type Quota struct {
	CPU       int    // maximum CPU in millicores
	Memory    string // maximum memory (e.g. "256Gi")
	Instances int    // maximum instance count
	Volumes   int    // maximum volume count
	Storage   string // maximum storage (e.g. "10Ti")
}

// Usage tracks current resource consumption for a tenant.
type Usage struct {
	CPU       int // current CPU usage in millicores
	Memory    int // current memory usage in bytes
	Instances int // current instance count
	Volumes   int // current volume count
}

// TenantRegistry manages tenants and resolves ownership from hierarchical service names.
type TenantRegistry struct {
	factStore store.StateStore
}

// NewTenantRegistry creates a registry backed by the given store.
func NewTenantRegistry(factStore store.StateStore) *TenantRegistry {
	return &TenantRegistry{factStore: factStore}
}

// ResolveTenantForService determines the owning tenant for a service.
// It first checks the explicit owner fact, then falls back to deriving
// the tenant from the hierarchical service name (first path segment).
func (registry *TenantRegistry) ResolveTenantForService(ctx context.Context, serviceName string) (string, error) {
	ownerFact, err := registry.factStore.Get(ctx, types.KeyDesiredServiceOwner(serviceName))
	if err == nil && len(ownerFact.Value) > 0 {
		return string(ownerFact.Value), nil
	}

	if tenantName := ExtractTenantFromName(serviceName); tenantName != "" {
		return tenantName, nil
	}

	return "", fmt.Errorf("service %q has no tenant owner", serviceName)
}

// ExtractTenantFromName derives the tenant from a hierarchical service name.
// "payments/checkout" → "payments". Returns empty string for flat names.
func ExtractTenantFromName(serviceName string) string {
	if slashIndex := strings.IndexByte(serviceName, '/'); slashIndex > 0 {
		return serviceName[:slashIndex]
	}
	return ""
}

// ListTenants returns all registered tenants by scanning the fact store.
func (registry *TenantRegistry) ListTenants(ctx context.Context) ([]Tenant, error) {
	facts, err := registry.factStore.Scan(ctx, types.ScanDesiredTenants)
	if err != nil {
		return nil, err
	}

	tenantNames := make(map[string]bool)
	for _, fact := range facts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredTenants)
		tenantName := strings.SplitN(relativePath, "/", 2)[0]
		tenantNames[tenantName] = true
	}

	tenants := make([]Tenant, 0, len(tenantNames))
	for tenantName := range tenantNames {
		tenant, loadErr := registry.GetTenant(ctx, tenantName)
		if loadErr != nil {
			continue
		}
		tenants = append(tenants, *tenant)
	}
	return tenants, nil
}

// GetTenant loads a single tenant's configuration from the fact store.
func (registry *TenantRegistry) GetTenant(ctx context.Context, tenantName string) (*Tenant, error) {
	_, err := registry.factStore.Get(ctx, types.KeyDesiredTenant(tenantName))
	if err != nil {
		return nil, fmt.Errorf("tenant %q not found", tenantName)
	}

	tenant := &Tenant{Name: tenantName}

	if cpuFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantQuotaCPU(tenantName)); getErr == nil {
		parsedCPU, parseError := strconv.Atoi(string(cpuFact.Value))
		if parseError != nil {
			return nil, fmt.Errorf("tenant %s: corrupt quota CPU value %q", tenantName, string(cpuFact.Value))
		}
		tenant.Quota.CPU = parsedCPU
	}
	if memFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantQuotaMemory(tenantName)); getErr == nil {
		tenant.Quota.Memory = string(memFact.Value)
	}
	if instFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantQuotaInstances(tenantName)); getErr == nil {
		parsedInstances, parseError := strconv.Atoi(string(instFact.Value))
		if parseError != nil {
			return nil, fmt.Errorf("tenant %s: corrupt quota instances value %q", tenantName, string(instFact.Value))
		}
		tenant.Quota.Instances = parsedInstances
	}
	if volFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantQuotaVolumes(tenantName)); getErr == nil {
		parsedVolumes, parseError := strconv.Atoi(string(volFact.Value))
		if parseError != nil {
			return nil, fmt.Errorf("tenant %s: corrupt quota volumes value %q", tenantName, string(volFact.Value))
		}
		tenant.Quota.Volumes = parsedVolumes
	}
	if storageFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantQuotaStorage(tenantName)); getErr == nil {
		tenant.Quota.Storage = string(storageFact.Value)
	}
	if weightFact, getErr := registry.factStore.Get(ctx, types.KeyDesiredTenantWeight(tenantName)); getErr == nil {
		parsedWeight, parseError := strconv.Atoi(string(weightFact.Value))
		if parseError != nil {
			return nil, fmt.Errorf("tenant %s: corrupt weight value %q", tenantName, string(weightFact.Value))
		}
		tenant.Weight = parsedWeight
	}

	return tenant, nil
}

// ListServicesForTenant returns all service names owned by the given tenant.
// This scans all services and checks ownership via explicit owner fact or
// hierarchical name derivation.
func (registry *TenantRegistry) ListServicesForTenant(ctx context.Context, tenantName string) ([]string, error) {
	allServiceFacts, err := registry.factStore.Scan(ctx, types.ScanDesiredServices)
	if err != nil {
		return nil, err
	}

	serviceNames := make(map[string]bool)
	for _, fact := range allServiceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		serviceName := parts[0]
		if len(parts) == 2 && strings.Contains(parts[0], "") {
			fullServicePath := strings.TrimPrefix(fact.Key, types.PrefixDesired+"/service/")
			pathParts := strings.Split(fullServicePath, "/")
			if len(pathParts) >= 2 {
				serviceName = pathParts[0] + "/" + pathParts[1]
			}
		}
		serviceNames[serviceName] = true
	}

	var ownedServices []string
	for serviceName := range serviceNames {
		owner, err := registry.ResolveTenantForService(ctx, serviceName)
		if err == nil && owner == tenantName {
			ownedServices = append(ownedServices, serviceName)
		}
	}
	return ownedServices, nil
}
