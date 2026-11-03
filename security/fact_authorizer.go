// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"fmt"
	"strings"
)

// CapabilityFactAuthorizer authorizes writing compiled DSL facts with the
// API-layer capability model (Flow 25). Users have no store-layer role
// bindings, so the apply policy gate checks the capability each fact's key
// area requires instead of store RBAC.
type CapabilityFactAuthorizer struct {
	apiAuthorizer *APIAuthorizer // apiAuthorizer holds the users' capability grants
}

// NewCapabilityFactAuthorizer returns a fact authorizer backed by the given
// API authorizer. It implements Authorizer for the policy gate.
func NewCapabilityFactAuthorizer(apiAuthorizer *APIAuthorizer) *CapabilityFactAuthorizer {
	return &CapabilityFactAuthorizer{apiAuthorizer: apiAuthorizer}
}

// Authorize allows a write of factKey when the "kind:name" principal holds
// the capability RequiredCapabilityForFactWrite returns. Only writes are
// gated; any other permission is denied.
func (factAuthorizer *CapabilityFactAuthorizer) Authorize(principal string, permission Permission, factKey string) error {
	if permission != PermissionWrite {
		return fmt.Errorf("fact authorizer: only %s is checked, got %s", PermissionWrite, permission)
	}
	requiredCapability := RequiredCapabilityForFactWrite(factKey)
	return factAuthorizer.apiAuthorizer.AuthorizeAPI(PrincipalFromKindAndName(principal), requiredCapability, ScopeCluster, nil)
}

// clusterAdminFactPrefixes are fact areas only cluster admins may write
// through apply: authorization rules, network policy and cloud identities.
var clusterAdminFactPrefixes = []string{
	"auth/",
	"policy/network/",
	"desired/cloud_identity/",
}

// workloadFactPrefixes are fact areas a workload.create principal may write
// through apply.
var workloadFactPrefixes = []string{
	"desired/",
	"effective/",
	"intent/",
	"derived/service/",
}

// RequiredCapabilityForFactWrite returns the capability needed to write a
// compiled fact. Deny by default: keys outside the known workload areas need
// cluster.admin, and binding secrets or cloud identities to a service needs
// more than workload.create.
func RequiredCapabilityForFactWrite(factKey string) Capability {
	for _, adminPrefix := range clusterAdminFactPrefixes {
		if strings.HasPrefix(factKey, adminPrefix) {
			return CapabilityClusterAdmin
		}
	}
	if strings.HasPrefix(factKey, "desired/service/") {
		switch {
		case strings.Contains(factKey, "/cloud_identity/"):
			return CapabilityClusterAdmin
		case strings.Contains(factKey, "/secret/"), strings.HasSuffix(factKey, "/image_pull_secret"):
			return CapabilitySecretUse
		}
	}
	for _, workloadPrefix := range workloadFactPrefixes {
		if strings.HasPrefix(factKey, workloadPrefix) {
			return CapabilityWorkloadCreate
		}
	}
	return CapabilityClusterAdmin
}
