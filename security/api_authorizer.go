package security

import (
	"fmt"
	"sync"
)

// CapabilityGrant binds a capability to a scope for a specific principal or
// role. For example, granting "workload.read" at "team/payments" scope.
type CapabilityGrant struct {
	Capability Capability // the operation allowed
	Scope      Scope      // the scope at which the capability is granted
}

// APIAuthorizer evaluates capability-based authorization decisions at the API
// layer. Unlike the store-layer RBACAuthorizer (which checks key-prefix
// permissions), this authorizer checks domain-level capabilities at
// hierarchical scopes.
type APIAuthorizer struct {
	grants map[string][]CapabilityGrant // principal string → capability grants
	mutex  sync.RWMutex
}

// NewAPIAuthorizer creates an empty API authorizer. Add grants before checking
// authorization.
func NewAPIAuthorizer() *APIAuthorizer {
	return &APIAuthorizer{
		grants: make(map[string][]CapabilityGrant),
	}
}

// Grant adds a capability grant for a principal. The principal is the canonical
// "kind:name" string (e.g. "user:alice", "node:node-1").
func (apiAuthorizer *APIAuthorizer) Grant(principal string, capability Capability, scope Scope) {
	apiAuthorizer.mutex.Lock()
	defer apiAuthorizer.mutex.Unlock()
	apiAuthorizer.grants[principal] = append(apiAuthorizer.grants[principal], CapabilityGrant{
		Capability: capability,
		Scope:      scope,
	})
}

// ReplaceGrants atomically replaces all grants with the provided map. This
// supports the auth reconciliation controller rebuilding state from store facts
// without leaving a window where grants are partially cleared.
func (apiAuthorizer *APIAuthorizer) ReplaceGrants(newGrants map[string][]CapabilityGrant) {
	apiAuthorizer.mutex.Lock()
	defer apiAuthorizer.mutex.Unlock()
	apiAuthorizer.grants = newGrants
}

// GrantRole maps a builtin role name to capability grants for a principal.
// This connects the RBAC role model to the capability model.
func (apiAuthorizer *APIAuthorizer) GrantRole(principal string, roleName string) {
	for _, grant := range builtinRoleCapabilities(roleName) {
		apiAuthorizer.Grant(principal, grant.Capability, grant.Scope)
	}
}

// AuthorizeAPI checks whether the principal has the required capability at the
// given scope. Returns nil if allowed, or an error describing the denial.
// The cluster.admin capability implicitly grants all other capabilities.
func (apiAuthorizer *APIAuthorizer) AuthorizeAPI(principal Principal, requiredCapability Capability, requiredScope Scope) error {
	if principal.Name == "" {
		return fmt.Errorf("api: denied — incomplete principal identity (kind=%q, name empty)", principal.Kind)
	}

	apiAuthorizer.mutex.RLock()
	defer apiAuthorizer.mutex.RUnlock()

	principalKey := principal.String()
	allGrants := apiAuthorizer.resolveGrants(principalKey)

	for _, grant := range allGrants {
		if grant.Capability == CapabilityClusterAdmin && grant.Scope.Contains(requiredScope) {
			return nil
		}
		if grant.Capability == requiredCapability && grant.Scope.Contains(requiredScope) {
			return nil
		}
	}

	return fmt.Errorf("api: principal %q denied capability %q at scope %q", principalKey, requiredCapability, requiredScope)
}

// resolveGrants collects all grants for a principal, including wildcard
// bindings (e.g. "user:*" matching "user:alice"). Caller must hold the read lock.
func (apiAuthorizer *APIAuthorizer) resolveGrants(principal string) []CapabilityGrant {
	var allGrants []CapabilityGrant
	if directGrants, hasGrants := apiAuthorizer.grants[principal]; hasGrants {
		allGrants = append(allGrants, directGrants...)
	}
	for boundPrincipal, boundGrants := range apiAuthorizer.grants {
		if len(boundPrincipal) > 0 && boundPrincipal[len(boundPrincipal)-1] == '*' {
			wildcardPrefix := boundPrincipal[:len(boundPrincipal)-1]
			if len(principal) >= len(wildcardPrefix) && principal[:len(wildcardPrefix)] == wildcardPrefix {
				allGrants = append(allGrants, boundGrants...)
			}
		}
	}
	return allGrants
}

// builtinRoleCapabilities maps builtin role names to their capability grants.
// This bridges the store-layer role model with the API-layer capability model.
func builtinRoleCapabilities(roleName string) []CapabilityGrant {
	switch roleName {
	case "cluster-admin":
		return []CapabilityGrant{
			{Capability: CapabilityClusterAdmin, Scope: ScopeCluster},
		}
	case "api-reader":
		return []CapabilityGrant{
			{Capability: CapabilityWorkloadRead, Scope: ScopeCluster},
			{Capability: CapabilityNodeRead, Scope: ScopeCluster},
			{Capability: CapabilityPlacementRead, Scope: ScopeCluster},
			{Capability: CapabilityScalingRead, Scope: ScopeCluster},
			{Capability: CapabilitySecretMetadataRead, Scope: ScopeCluster},
		}
	case "api-writer":
		return []CapabilityGrant{
			{Capability: CapabilityWorkloadRead, Scope: ScopeCluster},
			{Capability: CapabilityWorkloadCreate, Scope: ScopeCluster},
			{Capability: CapabilityWorkloadUpdate, Scope: ScopeCluster},
			{Capability: CapabilityScalingRead, Scope: ScopeCluster},
			{Capability: CapabilityScalingWrite, Scope: ScopeCluster},
			{Capability: CapabilityNodeRead, Scope: ScopeCluster},
			{Capability: CapabilityPlacementRead, Scope: ScopeCluster},
			{Capability: CapabilitySecretMetadataRead, Scope: ScopeCluster},
		}
	case "node-agent":
		return []CapabilityGrant{
			{Capability: CapabilityWorkloadRead, Scope: ScopeCluster},
			{Capability: CapabilityNodeRead, Scope: ScopeCluster},
			{Capability: CapabilityPlacementRead, Scope: ScopeCluster},
		}
	default:
		return nil
	}
}
