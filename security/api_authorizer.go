// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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

// ConditionalPolicy is an ABAC policy that grants a capability only when all
// its conditions evaluate to true against the principal and resource context.
type ConditionalPolicy struct {
	Name       string      // unique policy identifier (e.g. "team-isolation")
	Capability Capability  // capability granted when conditions pass
	Conditions []Condition // all must evaluate true (AND semantics)
}

// APIAuthorizer evaluates capability-based authorization decisions at the API
// layer. Unlike the store-layer RBACAuthorizer (which checks key-prefix
// permissions), this authorizer checks domain-level capabilities at
// hierarchical scopes. It also evaluates ABAC conditional policies when no
// unconditional grant matches.
type APIAuthorizer struct {
	grants   map[string][]CapabilityGrant // principal string → capability grants
	policies []ConditionalPolicy          // ABAC policies with conditions
	mutex    sync.RWMutex
	auditLog AuditLogger // optional audit logger for condition failures
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

// SetAuditLogger attaches an audit logger for recording ABAC condition
// evaluation failures. When set, denied requests that matched a policy's
// capability but failed conditions produce an audit entry.
func (apiAuthorizer *APIAuthorizer) SetAuditLogger(auditLog AuditLogger) {
	apiAuthorizer.mutex.Lock()
	defer apiAuthorizer.mutex.Unlock()
	apiAuthorizer.auditLog = auditLog
}

// ReplaceGrants atomically replaces all grants with the provided map. This
// supports the auth reconciliation controller rebuilding state from store facts
// without leaving a window where grants are partially cleared.
func (apiAuthorizer *APIAuthorizer) ReplaceGrants(newGrants map[string][]CapabilityGrant) {
	apiAuthorizer.mutex.Lock()
	defer apiAuthorizer.mutex.Unlock()
	apiAuthorizer.grants = newGrants
}

// ReplacePolicies atomically replaces all conditional ABAC policies. Called
// by the AuthController after reconciling auth/policy/ facts.
func (apiAuthorizer *APIAuthorizer) ReplacePolicies(newPolicies []ConditionalPolicy) {
	apiAuthorizer.mutex.Lock()
	defer apiAuthorizer.mutex.Unlock()
	apiAuthorizer.policies = newPolicies
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
// An optional ResourceContext enables ABAC condition evaluation against
// resource attributes; pass nil when no resource context is available.
func (apiAuthorizer *APIAuthorizer) AuthorizeAPI(principal Principal, requiredCapability Capability, requiredScope Scope, resourceContext *ResourceContext) error {
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

	conditionPolicyMatched := false
	for _, conditionalPolicy := range apiAuthorizer.policies {
		if conditionalPolicy.Capability != requiredCapability && conditionalPolicy.Capability != CapabilityClusterAdmin {
			continue
		}
		conditionPolicyMatched = true
		if EvaluateConditions(conditionalPolicy.Conditions, principal, resourceContext) {
			return nil
		}
		apiAuthorizer.logConditionFailure(principal, requiredCapability, requiredScope, conditionalPolicy)
	}

	if conditionPolicyMatched {
		return fmt.Errorf("api: principal %q denied capability %q at scope %q (condition_failed)", principalKey, requiredCapability, requiredScope)
	}
	return fmt.Errorf("api: principal %q denied capability %q at scope %q", principalKey, requiredCapability, requiredScope)
}

// logConditionFailure records an audit entry when a policy's capability matched
// but its conditions did not pass.
func (apiAuthorizer *APIAuthorizer) logConditionFailure(principal Principal, capability Capability, scope Scope, conditionalPolicy ConditionalPolicy) {
	if apiAuthorizer.auditLog == nil {
		return
	}
	apiAuthorizer.auditLog.Log(AuditEntry{
		Principal: principal.String(),
		Action:    string(capability),
		Target:    scope.String(),
		Decision:  "deny",
		Policy:    conditionalPolicy.Name + ":condition_failed",
	})
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
