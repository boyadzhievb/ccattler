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
	roles        map[string]*Role
	bindings     map[string][]string // principal → list of role names
	mutex        sync.RWMutex
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

// Authorize checks whether a principal is allowed to perform the given
// operation on the given key. It returns nil if allowed, or an error describing
// why the request was denied.
func (authorizer *RBACAuthorizer) Authorize(principal string, operation Permission, key string) error {
	authorizer.mutex.RLock()
	defer authorizer.mutex.RUnlock()

	roleNames, hasPrincipal := authorizer.bindings[principal]
	if !hasPrincipal {
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
func BuiltinRoles() []Role {
	return []Role{
		{
			Name: "cluster-admin",
			Rules: []Rule{
				{KeyPrefix: "/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete, PermissionWatch}},
			},
		},
		{
			Name: "node-agent",
			Rules: []Rule{
				{KeyPrefix: "/ccattler/observed/instance/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/observed/node/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/observed/volume/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/lease/node/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/effective/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/placement/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "scheduler",
			Rules: []Rule{
				{KeyPrefix: "/ccattler/placement/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "/ccattler/observed/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/effective/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "controller",
			Rules: []Rule{
				{KeyPrefix: "/ccattler/desired/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/effective/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/observed/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionWatch}},
				{KeyPrefix: "/ccattler/intent/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionWatch}},
				{KeyPrefix: "/ccattler/placement/", Operations: []Permission{PermissionRead, PermissionWatch}},
				{KeyPrefix: "/ccattler/endpoint/", Operations: []Permission{PermissionRead, PermissionWrite, PermissionDelete}},
				{KeyPrefix: "/ccattler/network/", Operations: []Permission{PermissionRead, PermissionWrite}},
			},
		},
		{
			Name: "api-reader",
			Rules: []Rule{
				{KeyPrefix: "/ccattler/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
		{
			Name: "api-writer",
			Rules: []Rule{
				{KeyPrefix: "/ccattler/desired/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/intent/user/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/observed/metric/", Operations: []Permission{PermissionRead, PermissionWrite}},
				{KeyPrefix: "/ccattler/", Operations: []Permission{PermissionRead, PermissionWatch}},
			},
		},
	}
}
