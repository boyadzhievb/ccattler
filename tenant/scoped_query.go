// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package tenant

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// IsPlatformPrincipal returns true if the principal should see all resources.
// Platform group members and system principals (controllers, scheduler) have
// unrestricted visibility.
func IsPlatformPrincipal(principal security.Principal) bool {
	if principal.Kind == security.PrincipalKindSystem {
		return true
	}
	return principal.HasGroup("platform")
}

// ResolvePrincipalTenant extracts the tenant name for a principal from their
// "team" attribute. Returns empty string if the principal has no team attribute,
// which should be treated as "no tenant visibility" (deny by default).
func ResolvePrincipalTenant(principal security.Principal) string {
	for _, attribute := range principal.Attributes {
		if attribute.Key == "team" {
			return attribute.Value
		}
	}
	return ""
}

// FilterServiceNamesByTenant returns only service names that belong to the
// given tenant, determined by explicit owner fact or hierarchical name.
// An empty tenantName returns no services (deny by default).
func FilterServiceNamesByTenant(ctx context.Context, factStore store.StateStore, serviceNames []string, tenantName string) []string {
	if tenantName == "" {
		return nil
	}

	var visibleServiceNames []string
	for _, serviceName := range serviceNames {
		if isServiceOwnedByTenant(ctx, factStore, serviceName, tenantName) {
			visibleServiceNames = append(visibleServiceNames, serviceName)
		}
	}
	return visibleServiceNames
}

// isServiceOwnedByTenant checks if a service belongs to the given tenant.
// It first checks the explicit owner fact, then falls back to hierarchical
// name derivation.
func isServiceOwnedByTenant(ctx context.Context, factStore store.StateStore, serviceName string, tenantName string) bool {
	ownerFact, err := factStore.Get(ctx, types.KeyDesiredServiceOwner(serviceName))
	if err == nil && len(ownerFact.Value) > 0 {
		return string(ownerFact.Value) == tenantName
	}

	return ExtractTenantFromName(serviceName) == tenantName
}

// ScopedScan wraps a store Scan call and filters results to facts visible
// to the given principal. Platform principals and unauthenticated requests
// (empty principal name) see everything. Tenant principals see only facts
// belonging to their services.
func ScopedScan(ctx context.Context, factStore store.StateStore, prefix string, principal security.Principal) ([]store.Fact, error) {
	facts, err := factStore.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}

	if principal.Name == "" || IsPlatformPrincipal(principal) {
		return facts, nil
	}

	tenantName := ResolvePrincipalTenant(principal)
	if tenantName == "" {
		return nil, nil
	}

	var visibleFacts []store.Fact
	for _, fact := range facts {
		if isFactVisibleToTenant(fact.Key, tenantName) {
			visibleFacts = append(visibleFacts, fact)
		}
	}
	return visibleFacts, nil
}

// isFactVisibleToTenant checks whether a fact key belongs to the given tenant.
// Service-scoped facts (desired/service/{name}/...) are checked by tenant
// ownership via hierarchical name. Other fact prefixes (nodes, cluster-level)
// are visible to all tenants since they represent shared infrastructure.
func isFactVisibleToTenant(factKey string, tenantName string) bool {
	if strings.HasPrefix(factKey, types.PrefixDesired+"/service/") {
		servicePath := strings.TrimPrefix(factKey, types.PrefixDesired+"/service/")
		serviceName := extractServiceNameFromPath(servicePath)
		return ExtractTenantFromName(serviceName) == tenantName
	}

	if strings.HasPrefix(factKey, "observed/instance/") {
		return true
	}

	return true
}

// extractServiceNameFromPath extracts the service name from a fact key path.
// For hierarchical names like "payments/checkout/image", returns "payments/checkout".
// For flat names like "web/image", returns "web".
func extractServiceNameFromPath(servicePath string) string {
	pathSegments := strings.Split(servicePath, "/")
	knownServiceFields := map[string]bool{
		"image": true, "instances": true, "port": true, "owner": true,
		"cpu": true, "memory": true, "health": true, "config": true,
		"secret": true, "scale": true, "placement": true, "update": true,
		"init": true, "startup": true, "liveness": true, "readiness": true,
		"cloud_identity": true, "external_port": true, "volume": true,
	}

	for segmentIndex := len(pathSegments) - 1; segmentIndex >= 1; segmentIndex-- {
		if knownServiceFields[pathSegments[segmentIndex]] {
			return strings.Join(pathSegments[:segmentIndex], "/")
		}
	}

	if len(pathSegments) >= 2 {
		return pathSegments[0] + "/" + pathSegments[1]
	}
	if len(pathSegments) >= 1 {
		return pathSegments[0]
	}
	return servicePath
}
