package tenant

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// QuotaAdmission checks whether a resource change would exceed a tenant's quota.
// It implements the arithmetic: usage + request <= quota → ALLOW, else → DENY.
type QuotaAdmission struct {
	factStore store.StateStore
	registry  *TenantRegistry
}

// NewQuotaAdmission creates a quota admission controller backed by the given store.
func NewQuotaAdmission(factStore store.StateStore, registry *TenantRegistry) *QuotaAdmission {
	return &QuotaAdmission{
		factStore: factStore,
		registry:  registry,
	}
}

// AdmissionResult contains the outcome of a quota admission check.
type AdmissionResult struct {
	Allowed    bool   // whether the request is within quota
	Reason     string // human-readable explanation when denied
	TenantName string // resolved tenant name
	Usage      int    // current usage of the checked resource
	Quota      int    // configured quota for the checked resource
}

// CheckInstanceAdmission verifies that creating additionalInstances more
// instances for the given service would not exceed the tenant's instance quota.
func (admission *QuotaAdmission) CheckInstanceAdmission(ctx context.Context, serviceName string, additionalInstances int) (*AdmissionResult, error) {
	tenantName, err := admission.registry.ResolveTenantForService(ctx, serviceName)
	if err != nil {
		return &AdmissionResult{Allowed: true, Reason: "no tenant, quota not enforced"}, nil
	}

	tenant, err := admission.registry.GetTenant(ctx, tenantName)
	if err != nil {
		return &AdmissionResult{Allowed: true, Reason: "tenant not found, quota not enforced"}, nil
	}

	if tenant.Quota.Instances == 0 {
		return &AdmissionResult{Allowed: true, TenantName: tenantName, Reason: "no instance quota set"}, nil
	}

	currentUsage := admission.countTenantInstances(ctx, tenantName)
	newTotal := currentUsage + additionalInstances

	result := &AdmissionResult{
		TenantName: tenantName,
		Usage:      currentUsage,
		Quota:      tenant.Quota.Instances,
	}

	if newTotal > tenant.Quota.Instances {
		result.Allowed = false
		result.Reason = fmt.Sprintf("instance quota exceeded: %d + %d = %d > %d",
			currentUsage, additionalInstances, newTotal, tenant.Quota.Instances)
	} else {
		result.Allowed = true
		result.Reason = fmt.Sprintf("within quota: %d + %d = %d <= %d",
			currentUsage, additionalInstances, newTotal, tenant.Quota.Instances)
	}

	return result, nil
}

// CheckVolumeAdmission verifies that creating one more volume for the given
// tenant would not exceed the tenant's volume quota.
func (admission *QuotaAdmission) CheckVolumeAdmission(ctx context.Context, tenantName string) (*AdmissionResult, error) {
	tenant, err := admission.registry.GetTenant(ctx, tenantName)
	if err != nil {
		return &AdmissionResult{Allowed: true, Reason: "tenant not found, quota not enforced"}, nil
	}

	if tenant.Quota.Volumes == 0 {
		return &AdmissionResult{Allowed: true, TenantName: tenantName, Reason: "no volume quota set"}, nil
	}

	currentUsage := admission.countTenantVolumes(ctx, tenantName)
	newTotal := currentUsage + 1

	result := &AdmissionResult{
		TenantName: tenantName,
		Usage:      currentUsage,
		Quota:      tenant.Quota.Volumes,
	}

	if newTotal > tenant.Quota.Volumes {
		result.Allowed = false
		result.Reason = fmt.Sprintf("volume quota exceeded: %d + 1 = %d > %d",
			currentUsage, newTotal, tenant.Quota.Volumes)
	} else {
		result.Allowed = true
		result.Reason = fmt.Sprintf("within quota: %d + 1 = %d <= %d",
			currentUsage, newTotal, tenant.Quota.Volumes)
	}

	return result, nil
}

// UpdateUsageFacts computes the current resource usage for a tenant and writes
// the observed usage facts to the store.
func (admission *QuotaAdmission) UpdateUsageFacts(ctx context.Context, tenantName string) error {
	instanceCount := admission.countTenantInstances(ctx, tenantName)
	volumeCount := admission.countTenantVolumes(ctx, tenantName)
	cpuUsage := admission.computeTenantCPUUsage(ctx, tenantName)

	admission.factStore.Put(ctx, types.KeyObservedTenantUsageInstances(tenantName),
		[]byte(strconv.Itoa(instanceCount)))
	admission.factStore.Put(ctx, types.KeyObservedTenantUsageVolumes(tenantName),
		[]byte(strconv.Itoa(volumeCount)))
	admission.factStore.Put(ctx, types.KeyObservedTenantUsageCPU(tenantName),
		[]byte(strconv.Itoa(cpuUsage)))

	return nil
}

// countTenantInstances counts all active (non-stopped) instances belonging to
// services owned by the given tenant.
func (admission *QuotaAdmission) countTenantInstances(ctx context.Context, tenantName string) int {
	instanceFacts, err := admission.factStore.Scan(ctx, types.ScanObservedInstances)
	if err != nil {
		return 0
	}

	instanceServices := make(map[string]string)
	instanceStates := make(map[string]string)
	for _, fact := range instanceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		fieldName := parts[1]
		switch fieldName {
		case "service":
			instanceServices[instanceID] = string(fact.Value)
		case "state":
			instanceStates[instanceID] = string(fact.Value)
		}
	}

	count := 0
	for instanceID, serviceName := range instanceServices {
		if instanceStates[instanceID] == "stopped" {
			continue
		}
		owner, err := admission.registry.ResolveTenantForService(ctx, serviceName)
		if err == nil && owner == tenantName {
			count++
		}
	}
	return count
}

// countTenantVolumes counts volumes owned by the given tenant. Volumes use
// hierarchical naming just like services: "payments/db" belongs to "payments".
func (admission *QuotaAdmission) countTenantVolumes(ctx context.Context, tenantName string) int {
	volumeFacts, err := admission.factStore.Scan(ctx, types.ScanDesiredVolumes)
	if err != nil {
		return 0
	}

	// Collect volume root keys (those whose relative path after the volume
	// prefix has no further sub-fields beyond the name segments).
	volumeNames := make(map[string]bool)
	for _, fact := range volumeFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredVolumes)
		// Volume keys: {name}, {name}/size, {name}/persistent, or
		// {tenant}/{name}, {tenant}/{name}/size, etc.
		// A root key stores "" as value and has no field suffix.
		if string(fact.Value) == "" && !strings.HasSuffix(relativePath, "/size") &&
			!strings.HasSuffix(relativePath, "/persistent") {
			volumeNames[relativePath] = true
		}
	}

	count := 0
	for volumeName := range volumeNames {
		volumeTenant := ExtractTenantFromName(volumeName)
		if volumeTenant == tenantName {
			count++
		}
	}
	return count
}

// computeTenantCPUUsage sums the CPU requirements of all services owned by
// the given tenant, multiplied by their effective instance counts.
func (admission *QuotaAdmission) computeTenantCPUUsage(ctx context.Context, tenantName string) int {
	serviceFacts, err := admission.factStore.Scan(ctx, types.ScanDesiredServices)
	if err != nil {
		return 0
	}

	serviceImages := make(map[string]bool)
	serviceCPU := make(map[string]int)
	serviceInstances := make(map[string]int)

	for _, fact := range serviceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		fieldName := parts[1]

		switch fieldName {
		case "image":
			serviceImages[serviceName] = true
		case "resources/cpu":
			cpuValue := strings.TrimSuffix(string(fact.Value), "m")
			parsed, _ := strconv.Atoi(cpuValue)
			serviceCPU[serviceName] = parsed
		case "instances":
			parsed, _ := strconv.Atoi(string(fact.Value))
			serviceInstances[serviceName] = parsed
		}
	}

	totalCPU := 0
	for serviceName := range serviceImages {
		owner, err := admission.registry.ResolveTenantForService(ctx, serviceName)
		if err != nil || owner != tenantName {
			continue
		}
		cpuPerInstance := serviceCPU[serviceName]
		instanceCount := serviceInstances[serviceName]
		totalCPU += cpuPerInstance * instanceCount
	}
	return totalCPU
}
