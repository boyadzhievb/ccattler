package tenant

import (
	"strings"

	"github.com/boyadzhievb/ccattler/security"
)

// TenantAuditView provides filtered views of the audit log scoped to a
// specific tenant. Each tenant sees only audit entries that affect resources
// they own, while platform admins see all entries.
type TenantAuditView struct {
	auditLog security.AuditLogger
	registry *TenantRegistry
}

// NewTenantAuditView creates an audit view backed by the given audit log.
func NewTenantAuditView(auditLog security.AuditLogger, registry *TenantRegistry) *TenantAuditView {
	return &TenantAuditView{
		auditLog: auditLog,
		registry: registry,
	}
}

// EntriesForTenant returns audit entries visible to the given tenant.
// An entry is visible if:
//   - The target contains the tenant name as a path prefix (e.g. "/ccattler/desired/service/payments/...")
//   - The principal belongs to the tenant (e.g. "user:alice" with tenant attribute)
//   - The target references a service owned by the tenant
func (view *TenantAuditView) EntriesForTenant(tenantName string) []security.AuditEntry {
	allEntries := view.auditLog.Entries()
	filtered := make([]security.AuditEntry, 0)

	for _, entry := range allEntries {
		if view.isVisibleToTenant(entry, tenantName) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// EntriesForPrincipal returns audit entries where the given principal was the actor.
func (view *TenantAuditView) EntriesForPrincipal(principal string) []security.AuditEntry {
	allEntries := view.auditLog.Entries()
	filtered := make([]security.AuditEntry, 0)

	for _, entry := range allEntries {
		if entry.Principal == principal {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// DeniedEntries returns all audit entries with a DENY decision, optionally
// filtered to a specific tenant.
func (view *TenantAuditView) DeniedEntries(tenantName string) []security.AuditEntry {
	var source []security.AuditEntry
	if tenantName != "" {
		source = view.EntriesForTenant(tenantName)
	} else {
		source = view.auditLog.Entries()
	}

	denied := make([]security.AuditEntry, 0)
	for _, entry := range source {
		if entry.Decision == "DENY" {
			denied = append(denied, entry)
		}
	}
	return denied
}

// AllEntries returns all audit entries (platform admin view).
func (view *TenantAuditView) AllEntries() []security.AuditEntry {
	return view.auditLog.Entries()
}

// isVisibleToTenant checks whether an audit entry is relevant to the given tenant.
func (view *TenantAuditView) isVisibleToTenant(entry security.AuditEntry, tenantName string) bool {
	// Target contains tenant name as path segment.
	if strings.Contains(entry.Target, "/"+tenantName+"/") {
		return true
	}

	// Target starts with tenant name (hierarchical service name).
	if strings.HasPrefix(entry.Target, tenantName+"/") {
		return true
	}

	// Target is exactly the tenant name.
	if entry.Target == tenantName {
		return true
	}

	// Principal contains tenant name (e.g. "node:payments-node-1").
	if strings.Contains(entry.Principal, tenantName) {
		return true
	}

	return false
}
