// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

const (
	// ScanAuthRoles scans role definitions under auth/role/.
	ScanAuthRoles = PrefixAuth + "/role/"

	// ScanAuthGrants scans grant bindings under auth/grant/.
	ScanAuthGrants = PrefixAuth + "/grant/"

	// ScanAuthGroups scans group definitions under auth/group/.
	ScanAuthGroups = PrefixAuth + "/group/"

	// ScanAuthPolicies scans ABAC policy definitions under auth/policy/.
	ScanAuthPolicies = PrefixAuth + "/policy/"

	// ScanAuthUsers scans user account definitions under auth/user/.
	ScanAuthUsers = PrefixAuth + "/user/"
)

// KeyAuthRoleCapability returns the store path for a capability granted by a role.
// Path: auth/role/{roleName}/capability/{capabilityName}
func KeyAuthRoleCapability(roleName string, capabilityName string) string {
	return fmt.Sprintf("%s/role/%s/capability/%s", PrefixAuth, roleName, capabilityName)
}

// KeyAuthRoleScope returns the store path for a scope restriction on a role.
// Path: auth/role/{roleName}/scope/{scopePath}
func KeyAuthRoleScope(roleName string, scopePath string) string {
	return fmt.Sprintf("%s/role/%s/scope/%s", PrefixAuth, roleName, scopePath)
}

// KeyAuthGrant returns the store path for a role grant to a principal.
// Path: auth/grant/{principalKind}/{principalName}/role/{roleName}
func KeyAuthGrant(principalKind string, principalName string, roleName string) string {
	return fmt.Sprintf("%s/grant/%s/%s/role/%s", PrefixAuth, principalKind, principalName, roleName)
}

// ScanAuthGrantsForPrincipal returns the scan prefix covering every role grant
// held by one principal. Path: auth/grant/{principalKind}/{principalName}/
func ScanAuthGrantsForPrincipal(principalKind string, principalName string) string {
	return fmt.Sprintf("%s%s/%s/", ScanAuthGrants, principalKind, principalName)
}

// KeyAuthGroupMember returns the store path for a member in a group.
// Path: auth/group/{groupName}/member/{memberName}
func KeyAuthGroupMember(groupName string, memberName string) string {
	return fmt.Sprintf("%s/group/%s/member/%s", PrefixAuth, groupName, memberName)
}

// KeyAuthPolicyCapability returns the store path for a policy's granted capability.
// Path: auth/policy/{policyName}/capability
func KeyAuthPolicyCapability(policyName string) string {
	return fmt.Sprintf("%s/policy/%s/capability", PrefixAuth, policyName)
}

// KeyAuthPolicyConditionField returns the store path for a condition's field (left-hand side).
// Path: auth/policy/{policyName}/condition/{index}/field
func KeyAuthPolicyConditionField(policyName string, conditionIndex int) string {
	return fmt.Sprintf("%s/policy/%s/condition/%d/field", PrefixAuth, policyName, conditionIndex)
}

// KeyAuthPolicyConditionOperator returns the store path for a condition's operator.
// Path: auth/policy/{policyName}/condition/{index}/operator
func KeyAuthPolicyConditionOperator(policyName string, conditionIndex int) string {
	return fmt.Sprintf("%s/policy/%s/condition/%d/operator", PrefixAuth, policyName, conditionIndex)
}

// KeyAuthPolicyConditionValue returns the store path for a condition's value (right-hand side).
// Path: auth/policy/{policyName}/condition/{index}/value
func KeyAuthPolicyConditionValue(policyName string, conditionIndex int) string {
	return fmt.Sprintf("%s/policy/%s/condition/%d/value", PrefixAuth, policyName, conditionIndex)
}

// KeyAuthUser returns the store path for a user account definition.
// The value is JSON with team, groups, and created timestamp.
// Path: auth/user/{name}
func KeyAuthUser(userName string) string {
	return fmt.Sprintf("%s/user/%s", PrefixAuth, userName)
}
