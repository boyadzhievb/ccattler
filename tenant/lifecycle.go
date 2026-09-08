package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TenantState represents the lifecycle state of a tenant.
type TenantState string

const (
	// TenantActive indicates the tenant is operational.
	TenantActive TenantState = "active"

	// TenantDeleting indicates the tenant is being garbage collected.
	TenantDeleting TenantState = "deleting"
)

// TenantLifecycle manages tenant creation and deletion, including provisioning
// boundaries on create and ownership-driven garbage collection on delete.
type TenantLifecycle struct {
	factStore store.StateStore
	registry  *TenantRegistry
}

// NewTenantLifecycle creates a lifecycle manager backed by the given store.
func NewTenantLifecycle(factStore store.StateStore, registry *TenantRegistry) *TenantLifecycle {
	return &TenantLifecycle{
		factStore: factStore,
		registry:  registry,
	}
}

// ProvisionedTenant describes what was created when a tenant was provisioned.
type ProvisionedTenant struct {
	Name           string // tenant identifier
	State          TenantState
	QuotaProvisioned bool // whether quota facts were written
	NetworkBoundary  bool // whether network isolation boundary was created
	SecretSpace      bool // whether secret namespace was reserved
	AuditStream      bool // whether audit stream marker was created
}

// CreateTenant provisions a new tenant with all required boundaries:
// identity scope, quota, network boundary, secret space, audit stream.
func (lifecycle *TenantLifecycle) CreateTenant(ctx context.Context, tenantName string, quota *Quota, weight int) (*ProvisionedTenant, error) {
	existing, _ := lifecycle.factStore.Get(ctx, types.KeyDesiredTenant(tenantName))
	if existing != nil {
		return nil, fmt.Errorf("tenant %q already exists", tenantName)
	}

	result := &ProvisionedTenant{Name: tenantName, State: TenantActive}

	// Core tenant marker.
	lifecycle.factStore.Put(ctx, types.KeyDesiredTenant(tenantName), []byte(""))
	lifecycle.factStore.Put(ctx, types.KeyDesiredTenantState(tenantName), []byte(string(TenantActive)))

	// Quota boundaries.
	if quota != nil {
		if quota.CPU > 0 {
			lifecycle.factStore.Put(ctx, types.KeyDesiredTenantQuotaCPU(tenantName), []byte(fmt.Sprintf("%d", quota.CPU)))
		}
		if quota.Memory != "" {
			lifecycle.factStore.Put(ctx, types.KeyDesiredTenantQuotaMemory(tenantName), []byte(quota.Memory))
		}
		if quota.Instances > 0 {
			lifecycle.factStore.Put(ctx, types.KeyDesiredTenantQuotaInstances(tenantName), []byte(fmt.Sprintf("%d", quota.Instances)))
		}
		if quota.Volumes > 0 {
			lifecycle.factStore.Put(ctx, types.KeyDesiredTenantQuotaVolumes(tenantName), []byte(fmt.Sprintf("%d", quota.Volumes)))
		}
		if quota.Storage != "" {
			lifecycle.factStore.Put(ctx, types.KeyDesiredTenantQuotaStorage(tenantName), []byte(quota.Storage))
		}
		result.QuotaProvisioned = true
	}

	if weight > 0 {
		lifecycle.factStore.Put(ctx, types.KeyDesiredTenantWeight(tenantName), []byte(fmt.Sprintf("%d", weight)))
	}

	// Network isolation boundary marker.
	networkBoundaryKey := fmt.Sprintf("/ccattler/tenant/%s/network/boundary", tenantName)
	lifecycle.factStore.Put(ctx, networkBoundaryKey, []byte("isolated"))
	result.NetworkBoundary = true

	// Secret namespace reservation.
	secretSpaceKey := fmt.Sprintf("/ccattler/tenant/%s/secrets/namespace", tenantName)
	lifecycle.factStore.Put(ctx, secretSpaceKey, []byte("reserved"))
	result.SecretSpace = true

	// Audit stream marker.
	auditStreamKey := fmt.Sprintf("/ccattler/tenant/%s/audit/stream", tenantName)
	lifecycle.factStore.Put(ctx, auditStreamKey, []byte("active"))
	result.AuditStream = true

	return result, nil
}

// DeleteTenant marks a tenant for deletion and garbage collects all owned
// resources: services, volumes, secrets, network policies, exports, imports,
// and usage facts.
func (lifecycle *TenantLifecycle) DeleteTenant(ctx context.Context, tenantName string) (*DeletionResult, error) {
	_, err := lifecycle.factStore.Get(ctx, types.KeyDesiredTenant(tenantName))
	if err != nil {
		return nil, fmt.Errorf("tenant %q not found", tenantName)
	}

	// Mark as deleting.
	lifecycle.factStore.Put(ctx, types.KeyDesiredTenantState(tenantName), []byte(string(TenantDeleting)))

	result := &DeletionResult{TenantName: tenantName}

	// Delete owned services (all facts under service/{tenant}/... or services with owner={tenant}).
	result.ServicesDeleted = lifecycle.deleteOwnedServices(ctx, tenantName)

	// Delete owned volumes.
	result.VolumesDeleted = lifecycle.deleteOwnedVolumes(ctx, tenantName)

	// Delete exports.
	result.ExportsDeleted = lifecycle.deleteExports(ctx, tenantName)

	// Delete tenant infrastructure facts.
	lifecycle.deleteTenantInfraFacts(ctx, tenantName)

	// Delete tenant desired facts (quota, weight, state, marker).
	lifecycle.deleteTenantDesiredFacts(ctx, tenantName)

	// Delete observed usage facts.
	lifecycle.deleteObservedUsageFacts(ctx, tenantName)

	return result, nil
}

// DeletionResult describes what was removed when a tenant was deleted.
type DeletionResult struct {
	TenantName      string
	ServicesDeleted int
	VolumesDeleted  int
	ExportsDeleted  int
}

// GetTenantState returns the lifecycle state of a tenant.
func (lifecycle *TenantLifecycle) GetTenantState(ctx context.Context, tenantName string) (TenantState, error) {
	stateFact, err := lifecycle.factStore.Get(ctx, types.KeyDesiredTenantState(tenantName))
	if err != nil {
		return "", fmt.Errorf("tenant %q not found", tenantName)
	}
	return TenantState(stateFact.Value), nil
}

// deleteOwnedServices removes all service facts for services owned by the tenant.
func (lifecycle *TenantLifecycle) deleteOwnedServices(ctx context.Context, tenantName string) int {
	serviceFacts, err := lifecycle.factStore.Scan(ctx, types.ScanDesiredServices)
	if err != nil {
		return 0
	}

	// Find all keys belonging to services owned by this tenant.
	keysToDelete := make([]string, 0)
	for _, fact := range serviceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName, _ := extractServiceNameAndField(relativePath)
		owner, err := lifecycle.registry.ResolveTenantForService(ctx, serviceName)
		if err != nil || owner != tenantName {
			continue
		}
		keysToDelete = append(keysToDelete, fact.Key)
	}

	// Also delete effective and intent facts for these services.
	serviceNames := make(map[string]bool)
	for _, key := range keysToDelete {
		relativePath := strings.TrimPrefix(key, types.ScanDesiredServices)
		serviceName, _ := extractServiceNameAndField(relativePath)
		serviceNames[serviceName] = true
	}

	count := len(serviceNames)
	for _, key := range keysToDelete {
		lifecycle.factStore.Delete(ctx, key)
	}

	// Clean up effective and intent facts.
	for serviceName := range serviceNames {
		lifecycle.factStore.Delete(ctx, types.KeyEffectiveServiceInstances(serviceName))
		lifecycle.factStore.Delete(ctx, types.KeyIntentUserServiceInstances(serviceName))
		lifecycle.factStore.Delete(ctx, types.KeyIntentAutoscalerServiceInstances(serviceName))
	}

	return count
}

// deleteOwnedVolumes removes volume facts for volumes with hierarchical names
// under this tenant.
func (lifecycle *TenantLifecycle) deleteOwnedVolumes(ctx context.Context, tenantName string) int {
	volumeFacts, err := lifecycle.factStore.Scan(ctx, types.ScanDesiredVolumes)
	if err != nil {
		return 0
	}

	keysToDelete := make([]string, 0)
	volumeNames := make(map[string]bool)
	for _, fact := range volumeFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredVolumes)
		volumeName := strings.SplitN(relativePath, "/", 2)[0]
		// For hierarchical volume names, check if the first segment is the tenant.
		if idx := strings.Index(relativePath, "/"); idx > 0 {
			possibleTenant := relativePath[:idx]
			if possibleTenant == tenantName {
				keysToDelete = append(keysToDelete, fact.Key)
				volumeName = relativePath
				if fieldIdx := strings.Index(relativePath[idx+1:], "/"); fieldIdx >= 0 {
					volumeName = relativePath[:idx+1+fieldIdx]
				}
				volumeNames[volumeName] = true
			}
		}
	}

	for _, key := range keysToDelete {
		lifecycle.factStore.Delete(ctx, key)
	}

	return len(volumeNames)
}

// deleteExports removes all export facts where the exported service is owned
// by this tenant.
func (lifecycle *TenantLifecycle) deleteExports(ctx context.Context, tenantName string) int {
	exportFacts, err := lifecycle.factStore.Scan(ctx, types.PrefixExport)
	if err != nil {
		return 0
	}

	count := 0
	for _, fact := range exportFacts {
		if string(fact.Value) == tenantName {
			count++
		}
		relativePath := strings.TrimPrefix(fact.Key, types.PrefixExport)
		serviceName := strings.SplitN(relativePath, "/allow/", 2)[0]
		owner := ExtractTenantFromName(serviceName)
		if owner == tenantName {
			lifecycle.factStore.Delete(ctx, fact.Key)
		}
	}

	return count
}

// deleteTenantInfraFacts removes tenant infrastructure facts (network boundary,
// secret namespace, audit stream).
func (lifecycle *TenantLifecycle) deleteTenantInfraFacts(ctx context.Context, tenantName string) {
	infraPrefix := fmt.Sprintf("/ccattler/tenant/%s/", tenantName)
	infraFacts, err := lifecycle.factStore.Scan(ctx, infraPrefix)
	if err != nil {
		return
	}
	for _, fact := range infraFacts {
		lifecycle.factStore.Delete(ctx, fact.Key)
	}
}

// deleteTenantDesiredFacts removes all desired tenant facts.
func (lifecycle *TenantLifecycle) deleteTenantDesiredFacts(ctx context.Context, tenantName string) {
	tenantPrefix := types.PrefixDesiredTenant + tenantName
	tenantFacts, err := lifecycle.factStore.Scan(ctx, tenantPrefix)
	if err != nil {
		return
	}
	for _, fact := range tenantFacts {
		lifecycle.factStore.Delete(ctx, fact.Key)
	}
	lifecycle.factStore.Delete(ctx, types.KeyDesiredTenant(tenantName))
}

// deleteObservedUsageFacts removes observed tenant usage metrics.
func (lifecycle *TenantLifecycle) deleteObservedUsageFacts(ctx context.Context, tenantName string) {
	lifecycle.factStore.Delete(ctx, types.KeyObservedTenantUsageCPU(tenantName))
	lifecycle.factStore.Delete(ctx, types.KeyObservedTenantUsageMemory(tenantName))
	lifecycle.factStore.Delete(ctx, types.KeyObservedTenantUsageInstances(tenantName))
	lifecycle.factStore.Delete(ctx, types.KeyObservedTenantUsageVolumes(tenantName))
}

// extractServiceNameAndField is imported from fair_scheduler.go (same package).
