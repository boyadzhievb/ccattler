package security

import (
	"fmt"
	"strings"
	"sync"
)

// Attribute represents a key-value pair attached to a principal or resource.
type Attribute struct {
	Key   string // attribute name (e.g. "team", "environment", "tier")
	Value string // attribute value (e.g. "platform", "production", "critical")
}

// ABACPolicy is a single attribute-based access control policy rule.
type ABACPolicy struct {
	Name                 string       // unique policy name
	RequiredAttributes   []Attribute  // all must match the principal's attributes
	TargetKeyPrefix      string       // fact store key prefix this policy applies to
	AllowedOperations    []Permission // operations allowed when the policy matches
}

// ABACAuthorizer evaluates attribute-based access control policies. It works
// alongside RBACAuthorizer — RBAC checks role-based permissions while ABAC
// adds attribute-based constraints (team isolation, production gates).
type ABACAuthorizer struct {
	policies             map[string]*ABACPolicy
	principalAttributes  map[string][]Attribute // principal → list of attributes
	mutex                sync.RWMutex
}

// NewABACAuthorizer creates an empty attribute-based authorizer.
func NewABACAuthorizer() *ABACAuthorizer {
	return &ABACAuthorizer{
		policies:            make(map[string]*ABACPolicy),
		principalAttributes: make(map[string][]Attribute),
	}
}

// AddPolicy registers an ABAC policy.
func (authorizer *ABACAuthorizer) AddPolicy(policy ABACPolicy) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	authorizer.policies[policy.Name] = &policy
}

// RemovePolicy removes a policy by name.
func (authorizer *ABACAuthorizer) RemovePolicy(policyName string) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	delete(authorizer.policies, policyName)
}

// SetPrincipalAttributes assigns attributes to a principal, replacing any
// existing attributes.
func (authorizer *ABACAuthorizer) SetPrincipalAttributes(principal string, attributes []Attribute) {
	authorizer.mutex.Lock()
	defer authorizer.mutex.Unlock()
	authorizer.principalAttributes[principal] = attributes
}

// GetPrincipalAttributes returns the attributes for a principal.
func (authorizer *ABACAuthorizer) GetPrincipalAttributes(principal string) []Attribute {
	authorizer.mutex.RLock()
	defer authorizer.mutex.RUnlock()
	attrs := authorizer.principalAttributes[principal]
	result := make([]Attribute, len(attrs))
	copy(result, attrs)
	return result
}

// Authorize checks whether a principal with its attributes is allowed to
// perform the given operation on the given key. It returns nil if any matching
// policy allows the operation, or an error if no policy matches.
func (authorizer *ABACAuthorizer) Authorize(principal string, operation Permission, key string) error {
	authorizer.mutex.RLock()
	defer authorizer.mutex.RUnlock()

	principalAttrs := authorizer.principalAttributes[principal]

	for _, policy := range authorizer.policies {
		if !strings.HasPrefix(key, policy.TargetKeyPrefix) {
			continue
		}

		if !attributesMatch(principalAttrs, policy.RequiredAttributes) {
			continue
		}

		for _, allowedOperation := range policy.AllowedOperations {
			if allowedOperation == operation {
				return nil
			}
		}
	}

	return fmt.Errorf("abac: principal %q denied %s on %q (no matching policy)", principal, operation, key)
}

// attributesMatch returns true if the principal's attributes satisfy all
// required attributes. Each required attribute must be present with the
// exact value.
func attributesMatch(principalAttributes, requiredAttributes []Attribute) bool {
	for _, required := range requiredAttributes {
		found := false
		for _, actual := range principalAttributes {
			if actual.Key == required.Key && actual.Value == required.Value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// CombinedAuthorizer chains RBAC and ABAC authorization. Access is granted if
// EITHER the RBAC authorizer OR the ABAC authorizer allows the operation.
type CombinedAuthorizer struct {
	rbacAuthorizer *RBACAuthorizer
	abacAuthorizer *ABACAuthorizer
}

// NewCombinedAuthorizer creates an authorizer that checks both RBAC and ABAC.
func NewCombinedAuthorizer(rbacAuthorizer *RBACAuthorizer, abacAuthorizer *ABACAuthorizer) *CombinedAuthorizer {
	return &CombinedAuthorizer{
		rbacAuthorizer: rbacAuthorizer,
		abacAuthorizer: abacAuthorizer,
	}
}

// Authorize checks RBAC first, then ABAC. Access is granted if either allows.
func (combinedAuthorizer *CombinedAuthorizer) Authorize(principal string, operation Permission, key string) error {
	rbacErr := combinedAuthorizer.rbacAuthorizer.Authorize(principal, operation, key)
	if rbacErr == nil {
		return nil
	}

	abacErr := combinedAuthorizer.abacAuthorizer.Authorize(principal, operation, key)
	if abacErr == nil {
		return nil
	}

	return fmt.Errorf("authorization denied: rbac: %v; abac: %v", rbacErr, abacErr)
}
