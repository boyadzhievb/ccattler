// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"fmt"
	"strings"
	"sync"
)

// Permission represents an allowed operation on fact store keys.
type Permission string

const (
	// PermissionRead allows reading facts matching the prefix.
	PermissionRead Permission = "read"

	// PermissionWrite allows writing facts matching the prefix.
	PermissionWrite Permission = "write"

	// PermissionDelete allows deleting facts matching the prefix.
	PermissionDelete Permission = "delete"

	// PermissionWatch allows watching facts matching the prefix.
	PermissionWatch Permission = "watch"
)

// Rule defines a single permission grant for a set of keys identified by prefix.
type Rule struct {
	KeyPrefix  string       // fact store key prefix this rule applies to
	Operations []Permission // operations allowed on matching keys
}

// Role is a named collection of rules that can be assigned to principals.
type Role struct {
	Name  string // unique identifier for the role
	Rules []Rule // permission rules in this role
}

// RoleBinding associates a principal (user, node, or controller) with a role.
type RoleBinding struct {
	Principal string // identity of the subject (e.g. "node:node-1", "controller:scheduler")
	RoleName  string // role to bind
}

// RBACAuthorizer evaluates access control decisions based on role bindings
// and fact-prefix permissions.
type RBACAuthorizer struct {
	roles    map[string]*Role
	bindings map[string][]string // principal → list of role names
	mutex    sync.RWMutex
}

// NewRBACAuthorizer creates an empty RBAC authorizer. Add roles and bindings
// before checking authorization.
func NewRBACAuthorizer() *RBACAuthorizer {
	return &RBACAuthorizer{
		roles:    make(map[string]*Role),
		bindings: make(map[string][]string),
	}
}

// AddRole registers a role. If a role with the same name exists, it is replaced.
func (authorizer *RBACAuthorizer) AddRole(role Role) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	authorizer.roles[role.Name] = &role
}

// BindRole assigns a role to a principal.
func (authorizer *RBACAuthorizer) BindRole(binding RoleBinding) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	authorizer.bindings[binding.Principal] = append(authorizer.bindings[binding.Principal], binding.RoleName)
}

// RemoveBinding removes all role bindings for a principal.
func (authorizer *RBACAuthorizer) RemoveBinding(principal string) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	delete(authorizer.bindings, principal)
}

// resolveRoleNames collects all role names bound to a principal. It checks
// exact matches first, then wildcard bindings (principal ending in "*").
// For example, a binding for "user:*" matches any principal starting with "user:".
func (authorizer *RBACAuthorizer) resolveRoleNames(principal string) []string {
	var roleNames []string
	if exactRoles, hasExact := authorizer.bindings[principal]; hasExact {
		roleNames = append(roleNames, exactRoles...)
	}
	for boundPrincipal, boundRoles := range authorizer.bindings {
		if strings.HasSuffix(boundPrincipal, "*") {
			wildcardPrefix := strings.TrimSuffix(boundPrincipal, "*")
			if strings.HasPrefix(principal, wildcardPrefix) {
				roleNames = append(roleNames, boundRoles...)
			}
		}
	}
	return roleNames
}

// Authorize checks whether a principal is allowed to perform the given
// operation on the given key. It returns nil if allowed, or an error describing
// why the request was denied.
func (authorizer *RBACAuthorizer) Authorize(principal string, operation Permission, key string) error {
	authorizer.mutex.RLock()
	defer authorizer.mutex.RUnlock()

	roleNames := authorizer.resolveRoleNames(principal)
	if len(roleNames) == 0 {
		return fmt.Errorf("rbac: principal %q has no role bindings", principal)
	}

	for _, roleName := range roleNames {
		role, roleExists := authorizer.roles[roleName]
		if !roleExists {
			continue
		}
		for _, rule := range role.Rules {
			if !strings.HasPrefix(key, rule.KeyPrefix) {
				continue
			}
			for _, allowedOperation := range rule.Operations {
				if allowedOperation == operation {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("rbac: principal %q denied %s on %q", principal, operation, key)
}

// BuiltinRoles returns the default roles for a CCattler cluster. Each
// controller and node agent gets a role scoped to the prefixes it needs.
// This function exceeds 80 lines because it is a flat slice literal of role
// definitions — each element is an independent data declaration. Extracting
// sub-builders would scatter related role data without improving clarity.
func BuiltinRoles() []Role {
	return []Role{
		{
			Name: "cluster-admin",
			Rules: []Rule{
				{KeyPrefix: "", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete, PermissionWatch}},
			},
		},
		{
			Name: "node-agent",
			Rules: []Rule{
				{KeyPrefix: "observed/instance/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "observed/node/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "observed/volume/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "lease/node/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "effective/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "placement/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "credentials/", Operations: []Permission{PermissionRead}},
				{KeyPrefix: "observed/credential/", Operations: []Permission{PermissionRead}},
				{KeyPrefix: "derived/instance/", Operations: []Permission{PermissionRead}},
				{KeyPrefix: "derived/credential/", Operations: []Permission{PermissionRead}},
			},
		},
		{
			Name: "scheduler",
			Rules: []Rule{
				{KeyPrefix: "placement/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "observed/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "effective/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "controller",
			Rules: []Rule{
				{KeyPrefix: "desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "effective/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "observed/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionWatch}},
				{KeyPrefix: "derived/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete, PermissionWatch}},
				{KeyPrefix: "intent/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionWatch}},
				{KeyPrefix: "placement/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "endpoint/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "network/", Operations: []Permission{PermissionRead, PermissionWrite}},
			},
		},
		{
			Name: "credential-broker",
			Rules: []Rule{
				{KeyPrefix: "desired/cloud_identity/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "desired/service/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "desired/credential_broker/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "observed/instance/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "observed/credential/", Operations: []Permission{PermissionRead}},
				{KeyPrefix: "derived/credential/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "credentials/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
			},
		},
		{
			Name: "cloud-controller",
			Rules: []Rule{
				{KeyPrefix: "desired/cloud/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "desired/service/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "observed/node/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionWatch}},
				{KeyPrefix: "observed/cloud/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "observed/instance/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "endpoint/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "network/node/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "api-reader",
			Rules: []Rule{
				{KeyPrefix: "", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "api-writer",
			Rules: []Rule{
				{KeyPrefix: "desired/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "intent/user/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "observed/metric/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
	}
}
