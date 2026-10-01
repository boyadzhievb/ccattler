package controllers

import (
	"context"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// AuthController watches auth/ prefix facts (roles, grants, groups) and
// rebuilds the APIAuthorizer's in-memory capability grants. Role and grant
// changes applied via DSL take effect on the next reconciliation cycle
// without requiring a server restart.
type AuthController struct {
	apiAuthorizer *security.APIAuthorizer // target authorizer to rebuild on each cycle
}

// NewAuthController creates an auth controller that will rebuild the given
// APIAuthorizer whenever auth/ facts change.
func NewAuthController(apiAuthorizer *security.APIAuthorizer) *AuthController {
	return &AuthController{apiAuthorizer: apiAuthorizer}
}

// Name returns "auth", identifying this controller in logs and runner bookkeeping.
func (authController *AuthController) Name() string { return "auth" }

// Watch returns the fact prefixes the auth controller monitors: roles,
// grants, groups, and policies under the auth/ prefix.
func (authController *AuthController) Watch() []string {
	return []string{
		types.ScanAuthRoles,
		types.ScanAuthGrants,
		types.ScanAuthGroups,
		types.ScanAuthPolicies,
	}
}

// Reconcile reads all auth/ facts, derives role→capability mappings and
// grant→principal bindings, resolves group memberships, parses conditional
// ABAC policies, and atomically replaces the APIAuthorizer's grant table
// and policy set. Returns no store changes — the side effect is the
// rebuilt in-memory authorizer state.
func (authController *AuthController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	roleCapabilities := parseRoleCapabilitiesFromFacts(facts)
	roleScopes := parseRoleScopesFromFacts(facts)
	groupMembers := parseGroupMembersFromFacts(facts)
	principalRoles := parsePrincipalRolesFromFacts(facts, groupMembers)

	newGrants := buildCapabilityGrants(principalRoles, roleCapabilities, roleScopes)
	authController.apiAuthorizer.ReplaceGrants(newGrants)

	conditionalPolicies := parseConditionalPoliciesFromFacts(facts)
	authController.apiAuthorizer.ReplacePolicies(conditionalPolicies)

	return nil, nil
}

// parseRoleCapabilitiesFromFacts extracts role→capabilities from auth/role/{name}/capability/{cap} facts.
func parseRoleCapabilitiesFromFacts(facts []store.Fact) map[string][]security.Capability {
	roleCapabilities := make(map[string][]security.Capability)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanAuthRoles) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthRoles)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) == 3 && pathParts[1] == "capability" {
			roleName := pathParts[0]
			capabilityName := pathParts[2]
			roleCapabilities[roleName] = append(roleCapabilities[roleName], security.Capability(capabilityName))
		}
	}
	return roleCapabilities
}

// parseRoleScopesFromFacts extracts role→scopes from auth/role/{name}/scope/{path} facts.
func parseRoleScopesFromFacts(facts []store.Fact) map[string][]security.Scope {
	roleScopes := make(map[string][]security.Scope)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanAuthRoles) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthRoles)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) >= 3 && pathParts[1] == "scope" {
			roleName := pathParts[0]
			scopePath := pathParts[2]
			roleScopes[roleName] = append(roleScopes[roleName], security.Scope(scopePath))
		}
	}
	return roleScopes
}

// parseGroupMembersFromFacts extracts group→members from auth/group/{name}/member/{id} facts.
func parseGroupMembersFromFacts(facts []store.Fact) map[string][]string {
	groupMembers := make(map[string][]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanAuthGroups) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthGroups)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) == 3 && pathParts[1] == "member" {
			groupName := pathParts[0]
			memberName := pathParts[2]
			groupMembers[groupName] = append(groupMembers[groupName], memberName)
		}
	}
	return groupMembers
}

// parsePrincipalRolesFromFacts extracts principal→roles from auth/grant/{kind}/{name}/role/{role} facts.
// Group grants are expanded: a grant to "group/developers" resolves to each
// group member as "user:{member}".
func parsePrincipalRolesFromFacts(facts []store.Fact, groupMembers map[string][]string) map[string][]string {
	principalRoles := make(map[string][]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanAuthGrants) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthGrants)
		pathParts := strings.SplitN(relativePath, "/", 4)
		if len(pathParts) == 4 && pathParts[2] == "role" {
			principalKind := pathParts[0]
			principalName := pathParts[1]
			roleName := pathParts[3]

			if principalKind == "group" {
				members := groupMembers[principalName]
				for _, memberName := range members {
					principalKey := "user:" + memberName
					principalRoles[principalKey] = append(principalRoles[principalKey], roleName)
				}
			} else {
				principalKey := principalKind + ":" + principalName
				principalRoles[principalKey] = append(principalRoles[principalKey], roleName)
			}
		}
	}
	return principalRoles
}

// buildCapabilityGrants produces the APIAuthorizer grant map from principal→roles,
// role→capabilities, and role→scopes. When a role has no explicit scopes, the
// cluster scope is used (granting the capability cluster-wide).
func buildCapabilityGrants(
	principalRoles map[string][]string,
	roleCapabilities map[string][]security.Capability,
	roleScopes map[string][]security.Scope,
) map[string][]security.CapabilityGrant {
	newGrants := make(map[string][]security.CapabilityGrant)

	sortedPrincipals := make([]string, 0, len(principalRoles))
	for principalKey := range principalRoles {
		sortedPrincipals = append(sortedPrincipals, principalKey)
	}
	sort.Strings(sortedPrincipals)

	for _, principalKey := range sortedPrincipals {
		roleNames := principalRoles[principalKey]
		for _, roleName := range roleNames {
			capabilities := roleCapabilities[roleName]
			scopes := roleScopes[roleName]
			if len(scopes) == 0 {
				scopes = []security.Scope{security.ScopeCluster}
			}
			for _, capability := range capabilities {
				for _, scope := range scopes {
					newGrants[principalKey] = append(newGrants[principalKey], security.CapabilityGrant{
						Capability: capability,
						Scope:      scope,
					})
				}
			}
		}
	}
	return newGrants
}

// parseConditionalPoliciesFromFacts extracts ABAC policies from auth/policy/
// facts. Each policy has a capability and zero or more conditions. The key
// layout is:
//
//	auth/policy/{name}/capability → "service.update"
//	auth/policy/{name}/condition/{index}/field → "subject.team"
//	auth/policy/{name}/condition/{index}/operator → "=="
//	auth/policy/{name}/condition/{index}/value → "resource.team"
func parseConditionalPoliciesFromFacts(facts []store.Fact) []security.ConditionalPolicy {
	policyCapabilities := make(map[string]string)
	conditionFields := make(map[string]map[int]string)
	conditionOperators := make(map[string]map[int]string)
	conditionValues := make(map[string]map[int]string)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanAuthPolicies) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthPolicies)
		pathParts := strings.SplitN(relativePath, "/", 4)

		if len(pathParts) < 2 {
			continue
		}

		policyName := pathParts[0]
		subKey := pathParts[1]

		if subKey == "capability" && len(pathParts) == 2 {
			policyCapabilities[policyName] = string(fact.Value)
			continue
		}

		if subKey == "condition" && len(pathParts) == 4 {
			conditionIndexStr := pathParts[2]
			conditionField := pathParts[3]
			conditionIndex := parseConditionIndex(conditionIndexStr)
			if conditionIndex < 0 {
				continue
			}

			switch conditionField {
			case "field":
				if conditionFields[policyName] == nil {
					conditionFields[policyName] = make(map[int]string)
				}
				conditionFields[policyName][conditionIndex] = string(fact.Value)
			case "operator":
				if conditionOperators[policyName] == nil {
					conditionOperators[policyName] = make(map[int]string)
				}
				conditionOperators[policyName][conditionIndex] = string(fact.Value)
			case "value":
				if conditionValues[policyName] == nil {
					conditionValues[policyName] = make(map[int]string)
				}
				conditionValues[policyName][conditionIndex] = string(fact.Value)
			}
		}
	}

	sortedPolicyNames := make([]string, 0, len(policyCapabilities))
	for policyName := range policyCapabilities {
		sortedPolicyNames = append(sortedPolicyNames, policyName)
	}
	sort.Strings(sortedPolicyNames)

	var conditionalPolicies []security.ConditionalPolicy
	for _, policyName := range sortedPolicyNames {
		capabilityName := policyCapabilities[policyName]
		policy := security.ConditionalPolicy{
			Name:       policyName,
			Capability: security.Capability(capabilityName),
		}

		fieldMap := conditionFields[policyName]
		operatorMap := conditionOperators[policyName]
		valueMap := conditionValues[policyName]

		maxIndex := -1
		for conditionIndex := range fieldMap {
			if conditionIndex > maxIndex {
				maxIndex = conditionIndex
			}
		}

		for conditionIndex := 0; conditionIndex <= maxIndex; conditionIndex++ {
			fieldValue, hasField := fieldMap[conditionIndex]
			operatorValue, hasOperator := operatorMap[conditionIndex]
			valueStr, hasValue := valueMap[conditionIndex]
			if !hasField || !hasOperator || !hasValue {
				continue
			}
			policy.Conditions = append(policy.Conditions, security.Condition{
				Field:    fieldValue,
				Operator: security.ConditionOperator(operatorValue),
				Value:    valueStr,
			})
		}

		conditionalPolicies = append(conditionalPolicies, policy)
	}

	return conditionalPolicies
}

// parseConditionIndex parses a string condition index into an integer.
// Returns -1 if the string is not a valid non-negative integer.
func parseConditionIndex(indexString string) int {
	conditionIndex := 0
	for _, digitChar := range indexString {
		if digitChar < '0' || digitChar > '9' {
			return -1
		}
		conditionIndex = conditionIndex*10 + int(digitChar-'0')
	}
	if len(indexString) == 0 {
		return -1
	}
	return conditionIndex
}
