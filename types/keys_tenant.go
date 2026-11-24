// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// Tenant key prefixes.
const (
	// PrefixDesiredTenant holds desired tenant definitions and quotas.
	PrefixDesiredTenant = PrefixDesired + "/tenant/"

	// PrefixObservedTenant holds observed tenant usage metrics.
	PrefixObservedTenant = PrefixObserved + "/tenant/"

	// ScanDesiredTenants scans all desired tenant definitions.
	ScanDesiredTenants = PrefixDesiredTenant
)

// KeyDesiredTenant returns the store path for a tenant's root marker key.
// Path: desired/tenant/{name}
func KeyDesiredTenant(tenantName string) string {
	return fmt.Sprintf("%s%s", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaCPU returns the store path for a tenant's CPU quota in millicores.
// Path: desired/tenant/{name}/quota/cpu
func KeyDesiredTenantQuotaCPU(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/cpu", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaMemory returns the store path for a tenant's memory quota in bytes.
// Path: desired/tenant/{name}/quota/memory
func KeyDesiredTenantQuotaMemory(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/memory", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaInstances returns the store path for a tenant's maximum instance count.
// Path: desired/tenant/{name}/quota/instances
func KeyDesiredTenantQuotaInstances(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/instances", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaVolumes returns the store path for a tenant's maximum volume count.
// Path: desired/tenant/{name}/quota/volumes
func KeyDesiredTenantQuotaVolumes(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/volumes", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaStorage returns the store path for a tenant's total storage quota.
// Path: desired/tenant/{name}/quota/storage
func KeyDesiredTenantQuotaStorage(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/storage", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantWeight returns the store path for a tenant's scheduling weight.
// Path: desired/tenant/{name}/weight
func KeyDesiredTenantWeight(tenantName string) string {
	return fmt.Sprintf("%s%s/weight", PrefixDesiredTenant, tenantName)
}

// KeyObservedTenantUsageCPU returns the store path for a tenant's current CPU usage.
// Path: observed/tenant/{name}/usage/cpu
func KeyObservedTenantUsageCPU(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/cpu", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageMemory returns the store path for a tenant's current memory usage.
// Path: observed/tenant/{name}/usage/memory
func KeyObservedTenantUsageMemory(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/memory", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageInstances returns the store path for a tenant's current instance count.
// Path: observed/tenant/{name}/usage/instances
func KeyObservedTenantUsageInstances(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/instances", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageVolumes returns the store path for a tenant's current volume count.
// Path: observed/tenant/{name}/usage/volumes
func KeyObservedTenantUsageVolumes(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/volumes", PrefixObservedTenant, tenantName)
}

// KeyDesiredServiceOwner returns the store path for a service's owning tenant.
// Path: desired/service/{name}/owner
func KeyDesiredServiceOwner(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/owner", PrefixDesired, serviceName)
}

// Shared service export/import keys.
const (
	// PrefixExport holds shared service export declarations.
	PrefixExport = "export/"

	// PrefixImport holds service import ("uses") declarations.
	PrefixImport = "import/"
)

// KeyExportService returns the store path for a shared service export marker.
// Path: export/{serviceName}
func KeyExportService(serviceName string) string {
	return fmt.Sprintf("%s%s", PrefixExport, serviceName)
}

// KeyExportServiceAllowTenant returns the store path for a tenant allowed to
// consume an exported service.
// Path: export/{serviceName}/allow/{tenantName}
func KeyExportServiceAllowTenant(serviceName, tenantName string) string {
	return fmt.Sprintf("%s%s/allow/%s", PrefixExport, serviceName, tenantName)
}

// ScanExportServiceAllowTenants returns the scan prefix for all tenants
// allowed to consume an exported service.
func ScanExportServiceAllowTenants(serviceName string) string {
	return fmt.Sprintf("%s%s/allow/", PrefixExport, serviceName)
}

// KeyImportService returns the store path for a service's import declaration.
// Path: import/{consumerService}/uses/{exportedService}
func KeyImportService(consumerService, exportedService string) string {
	return fmt.Sprintf("%s%s/uses/%s", PrefixImport, consumerService, exportedService)
}

// ScanImportsForService returns the scan prefix for all imports declared by a service.
func ScanImportsForService(consumerService string) string {
	return fmt.Sprintf("%s%s/uses/", PrefixImport, consumerService)
}

// Tenant lifecycle keys.

// KeyDesiredTenantState returns the store path for a tenant's lifecycle state
// (active, deleting).
// Path: desired/tenant/{name}/state
func KeyDesiredTenantState(tenantName string) string {
	return fmt.Sprintf("%s%s/state", PrefixDesiredTenant, tenantName)
}
