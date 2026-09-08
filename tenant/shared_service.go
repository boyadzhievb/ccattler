package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// SharedServiceManager handles cross-tenant service exports and imports.
// A service can be exported from one tenant and consumed by others via
// explicit allow lists.
type SharedServiceManager struct {
	factStore store.StateStore
	registry  *TenantRegistry
}

// NewSharedServiceManager creates a shared service manager.
func NewSharedServiceManager(factStore store.StateStore, registry *TenantRegistry) *SharedServiceManager {
	return &SharedServiceManager{
		factStore: factStore,
		registry:  registry,
	}
}

// ExportedService describes a service that has been shared for cross-tenant use.
type ExportedService struct {
	ServiceName    string   // fully qualified service name (e.g. "platform/dns")
	OwnerTenant    string   // tenant that owns the service
	AllowedTenants []string // tenants allowed to consume this service
}

// ExportService declares a service as shared, allowing the listed tenants
// to consume it. The exporting tenant must own the service.
func (manager *SharedServiceManager) ExportService(ctx context.Context, serviceName string, allowedTenants []string) error {
	ownerTenant, err := manager.registry.ResolveTenantForService(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("cannot export %q: %w", serviceName, err)
	}

	_, err = manager.factStore.Put(ctx, types.KeyExportService(serviceName), []byte(ownerTenant))
	if err != nil {
		return fmt.Errorf("store export marker: %w", err)
	}

	for _, tenantName := range allowedTenants {
		_, err := manager.factStore.Put(ctx, types.KeyExportServiceAllowTenant(serviceName, tenantName), []byte(""))
		if err != nil {
			return fmt.Errorf("store allow for %q: %w", tenantName, err)
		}
	}

	return nil
}

// UnexportService removes a shared service export and all its allow entries.
func (manager *SharedServiceManager) UnexportService(ctx context.Context, serviceName string) error {
	allowFacts, _ := manager.factStore.Scan(ctx, types.ScanExportServiceAllowTenants(serviceName))
	for _, fact := range allowFacts {
		manager.factStore.Delete(ctx, fact.Key)
	}
	return manager.factStore.Delete(ctx, types.KeyExportService(serviceName))
}

// ImportService declares that a consumer service uses an exported service.
func (manager *SharedServiceManager) ImportService(ctx context.Context, consumerService, exportedService string) error {
	allowed, err := manager.IsImportAllowed(ctx, consumerService, exportedService)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("service %q is not allowed to use %q", consumerService, exportedService)
	}

	_, err = manager.factStore.Put(ctx, types.KeyImportService(consumerService, exportedService), []byte(""))
	return err
}

// RemoveImport removes a service's import declaration.
func (manager *SharedServiceManager) RemoveImport(ctx context.Context, consumerService, exportedService string) error {
	return manager.factStore.Delete(ctx, types.KeyImportService(consumerService, exportedService))
}

// IsImportAllowed checks whether a consumer service is allowed to use an
// exported service. This requires the exported service to exist and the
// consumer's tenant to be in the allow list.
func (manager *SharedServiceManager) IsImportAllowed(ctx context.Context, consumerService, exportedService string) (bool, error) {
	exportFact, err := manager.factStore.Get(ctx, types.KeyExportService(exportedService))
	if err != nil {
		return false, fmt.Errorf("service %q is not exported", exportedService)
	}

	exportOwner := string(exportFact.Value)

	consumerTenant, err := manager.registry.ResolveTenantForService(ctx, consumerService)
	if err != nil {
		return false, fmt.Errorf("cannot resolve consumer tenant: %w", err)
	}

	// Same-tenant access to own exports is always allowed.
	if consumerTenant == exportOwner {
		return true, nil
	}

	// Check the allow list.
	_, err = manager.factStore.Get(ctx, types.KeyExportServiceAllowTenant(exportedService, consumerTenant))
	return err == nil, nil
}

// GetExportedService loads an exported service's configuration from the store.
func (manager *SharedServiceManager) GetExportedService(ctx context.Context, serviceName string) (*ExportedService, error) {
	exportFact, err := manager.factStore.Get(ctx, types.KeyExportService(serviceName))
	if err != nil {
		return nil, fmt.Errorf("service %q is not exported", serviceName)
	}

	allowFacts, err := manager.factStore.Scan(ctx, types.ScanExportServiceAllowTenants(serviceName))
	if err != nil {
		return nil, err
	}

	allowedTenants := make([]string, 0, len(allowFacts))
	for _, fact := range allowFacts {
		tenantName := strings.TrimPrefix(fact.Key, types.ScanExportServiceAllowTenants(serviceName))
		allowedTenants = append(allowedTenants, tenantName)
	}

	return &ExportedService{
		ServiceName:    serviceName,
		OwnerTenant:    string(exportFact.Value),
		AllowedTenants: allowedTenants,
	}, nil
}

// ListExportedServices returns all exported services.
func (manager *SharedServiceManager) ListExportedServices(ctx context.Context) ([]ExportedService, error) {
	exportFacts, err := manager.factStore.Scan(ctx, types.PrefixExport)
	if err != nil {
		return nil, err
	}

	// Collect unique exported service names (skip /allow/ sub-keys).
	serviceNames := make(map[string]string)
	for _, fact := range exportFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.PrefixExport)
		if strings.Contains(relativePath, "/allow/") {
			continue
		}
		serviceNames[relativePath] = string(fact.Value)
	}

	exports := make([]ExportedService, 0, len(serviceNames))
	for serviceName := range serviceNames {
		exported, err := manager.GetExportedService(ctx, serviceName)
		if err != nil {
			continue
		}
		exports = append(exports, *exported)
	}
	return exports, nil
}

// ListImportsForService returns all exported services that a consumer imports.
func (manager *SharedServiceManager) ListImportsForService(ctx context.Context, consumerService string) ([]string, error) {
	importFacts, err := manager.factStore.Scan(ctx, types.ScanImportsForService(consumerService))
	if err != nil {
		return nil, err
	}

	imports := make([]string, 0, len(importFacts))
	for _, fact := range importFacts {
		exportedName := strings.TrimPrefix(fact.Key, types.ScanImportsForService(consumerService))
		imports = append(imports, exportedName)
	}
	return imports, nil
}
