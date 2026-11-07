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
// System principals (controllers, scheduler) and node principals (agents,
// server) have unrestricted visibility — they are infrastructure, not tenants.
// User principals with the "platform" group also get full visibility.
func IsPlatformPrincipal(principal security.Principal) bool {
	if principal.Kind == security.PrincipalKindSystem {
		return true
	}
	if principal.Kind == security.PrincipalKindNode {
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

// FilterServiceNamesByTeams returns only the service names that belong to one
// of the given teams, determined by the explicit owner (ownerByService, from
// desired/service/{name}/owner) or else the hierarchical name. No teams
// returns no services (deny by default).
func FilterServiceNamesByTeams(serviceNames []string, teamNames []string, ownerByService map[string]string) []string {
	var visibleServiceNames []string
	for _, serviceName := range serviceNames {
		for _, teamName := range teamNames {
			if isServiceOwnedByTenant(serviceName, ownerByService[serviceName], teamName) {
				visibleServiceNames = append(visibleServiceNames, serviceName)
				break
			}
		}
	}
	return visibleServiceNames
}

// isServiceOwnedByTenant checks if a service belongs to the given tenant: a
// non-empty explicit owner decides, otherwise the hierarchical name does.
func isServiceOwnedByTenant(serviceName string, explicitOwner string, tenantName string) bool {
	if explicitOwner != "" {
		return explicitOwner == tenantName
	}
	return ExtractTenantFromName(serviceName) == tenantName
}

// ScanVisibleToTeams scans the prefix and keeps only the facts visible to one
// of the given teams (IsFactVisibleToTeams). The caller decides the teams from
// the principal's grants; a cluster-scope reader scans the store directly.
func ScanVisibleToTeams(ctx context.Context, factStore store.StateStore, prefix string, teamNames []string) ([]store.Fact, error) {
	facts, err := factStore.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var visibleFacts []store.Fact
	for _, fact := range facts {
		if IsFactVisibleToTeams(fact.Key, teamNames) {
			visibleFacts = append(visibleFacts, fact)
		}
	}
	return visibleFacts, nil
}

// IsFactVisibleToTeams reports whether a fact key is visible to any of the
// given teams. No teams sees nothing.
func IsFactVisibleToTeams(factKey string, teamNames []string) bool {
	for _, teamName := range teamNames {
		if isFactVisibleToTenant(factKey, teamName) {
			return true
		}
	}
	return false
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
