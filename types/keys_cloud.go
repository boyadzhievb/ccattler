// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// Cloud identity keys
// Pattern: desired/cloud_identity/{name}/{field}
// Pattern: desired/service/{name}/cloud_identity/{identityName}/{field}
// Pattern: desired/credential_broker/{field}
// Pattern: observed/credential/{instanceID}/{identityName}/{field}

const (
	// ScanDesiredCloudIdentities scans all desired cloud identity definitions.
	ScanDesiredCloudIdentities = PrefixDesired + "/cloud_identity/"

	// ScanDesiredCredentialBroker scans the credential broker configuration.
	ScanDesiredCredentialBroker = PrefixDesired + "/credential_broker/"

	// ScanObservedCredentials scans all observed credential state.
	ScanObservedCredentials = PrefixObserved + "/credential/"
)

// KeyDesiredCloudIdentity returns the root marker for a cloud identity.
// Path: desired/cloud_identity/{name}
func KeyDesiredCloudIdentity(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityProvider returns the cloud provider for an identity.
// Path: desired/cloud_identity/{name}/provider
func KeyDesiredCloudIdentityProvider(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/provider", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityRole returns the IAM role ARN (AWS).
// Path: desired/cloud_identity/{name}/role
func KeyDesiredCloudIdentityRole(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/role", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityServiceAccount returns the GCP service account email.
// Path: desired/cloud_identity/{name}/service_account
func KeyDesiredCloudIdentityServiceAccount(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/service_account", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityPool returns the GCP workload identity pool.
// Path: desired/cloud_identity/{name}/pool
func KeyDesiredCloudIdentityPool(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/pool", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityClientID returns the Azure AD client ID.
// Path: desired/cloud_identity/{name}/client_id
func KeyDesiredCloudIdentityClientID(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/client_id", PrefixDesired, identityName)
}

// KeyDesiredCloudIdentityTenantID returns the Azure AD tenant ID.
// Path: desired/cloud_identity/{name}/tenant_id
func KeyDesiredCloudIdentityTenantID(identityName string) string {
	return fmt.Sprintf("%s/cloud_identity/%s/tenant_id", PrefixDesired, identityName)
}

// KeyDesiredServiceCloudIdentity returns the root marker for a service's cloud
// identity binding.
// Path: desired/service/{serviceName}/cloud_identity/{identityName}
func KeyDesiredServiceCloudIdentity(serviceName string, identityName string) string {
	return fmt.Sprintf("%s/service/%s/cloud_identity/%s", PrefixDesired, serviceName, identityName)
}

// KeyDesiredServiceCloudIdentityMountPath returns the mount path for a service's
// cloud identity credential files.
// Path: desired/service/{serviceName}/cloud_identity/{identityName}/mount_path
func KeyDesiredServiceCloudIdentityMountPath(serviceName string, identityName string) string {
	return fmt.Sprintf("%s/service/%s/cloud_identity/%s/mount_path", PrefixDesired, serviceName, identityName)
}

// KeyDesiredServiceCloudIdentityDeliverMode returns how credentials are delivered
// ("credentials" or "token").
// Path: desired/service/{serviceName}/cloud_identity/{identityName}/deliver_mode
func KeyDesiredServiceCloudIdentityDeliverMode(serviceName string, identityName string) string {
	return fmt.Sprintf("%s/service/%s/cloud_identity/%s/deliver_mode", PrefixDesired, serviceName, identityName)
}

// ScanDesiredServiceCloudIdentities returns the scan prefix for all cloud
// identity bindings of a service.
// Path: desired/service/{serviceName}/cloud_identity/
func ScanDesiredServiceCloudIdentities(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/cloud_identity/", PrefixDesired, serviceName)
}

// KeyDesiredCredentialBrokerOIDCIssuer returns the OIDC issuer URL.
// Path: desired/credential_broker/oidc_issuer
func KeyDesiredCredentialBrokerOIDCIssuer() string {
	return PrefixDesired + "/credential_broker/oidc_issuer"
}

// KeyDesiredCredentialBrokerCredentialTTL returns the credential lifetime.
// Path: desired/credential_broker/credential_ttl
func KeyDesiredCredentialBrokerCredentialTTL() string {
	return PrefixDesired + "/credential_broker/credential_ttl"
}

// KeyDesiredCredentialBrokerRefreshBefore returns the refresh-before window.
// Path: desired/credential_broker/refresh_before
func KeyDesiredCredentialBrokerRefreshBefore() string {
	return PrefixDesired + "/credential_broker/refresh_before"
}

// KeyDerivedCredentialState returns the broker-derived credential state for an
// instance's cloud identity binding.
// Path: derived/credential/{instanceID}/{identityName}/state
func KeyDerivedCredentialState(instanceID string, identityName string) string {
	return fmt.Sprintf("%s/credential/%s/%s/state", PrefixDerived, instanceID, identityName)
}

// KeyDerivedCredentialExpiresAt returns the broker-derived expiry timestamp for an
// instance's cloud credential.
// Path: derived/credential/{instanceID}/{identityName}/expires_at
func KeyDerivedCredentialExpiresAt(instanceID string, identityName string) string {
	return fmt.Sprintf("%s/credential/%s/%s/expires_at", PrefixDerived, instanceID, identityName)
}

// KeyDerivedCredentialIssuedAt returns the broker-derived issuance timestamp for an
// instance's cloud credential.
// Path: derived/credential/{instanceID}/{identityName}/issued_at
func KeyDerivedCredentialIssuedAt(instanceID string, identityName string) string {
	return fmt.Sprintf("%s/credential/%s/%s/issued_at", PrefixDerived, instanceID, identityName)
}

// KeyDerivedCredentialError returns the broker-derived last error for an instance's
// cloud credential.
// Path: derived/credential/{instanceID}/{identityName}/error
func KeyDerivedCredentialError(instanceID string, identityName string) string {
	return fmt.Sprintf("%s/credential/%s/%s/error", PrefixDerived, instanceID, identityName)
}

// Cloud controller manager keys
// Pattern: desired/cloud/{field}
// Pattern: desired/service/{name}/expose/{port}/external
// Pattern: observed/cloud/instance/{providerInstanceID}/{field}
// Pattern: observed/cloud/loadbalancer/{serviceName}/{field}
// Pattern: observed/cloud/route/{cidr}
// Pattern: observed/node/{nodeID}/provider_instance_id

const (
	// ScanDesiredCloud scans all desired cloud configuration facts.
	ScanDesiredCloud = PrefixDesired + "/cloud/"

	// ScanObservedCloudInstances scans all observed cloud instance facts.
	ScanObservedCloudInstances = PrefixObserved + "/cloud/instance/"

	// ScanObservedCloudLoadBalancers scans all observed cloud load balancer facts.
	ScanObservedCloudLoadBalancers = PrefixObserved + "/cloud/loadbalancer/"

	// ScanObservedCloudRoutes scans all observed cloud route facts.
	ScanObservedCloudRoutes = PrefixObserved + "/cloud/route/"
)

// KeyDesiredCloudProvider returns the store path for the cloud provider name.
// Path: desired/cloud/provider
func KeyDesiredCloudProvider() string {
	return PrefixDesired + "/cloud/provider"
}

// KeyDesiredCloudRegion returns the store path for the cloud region.
// Path: desired/cloud/region
func KeyDesiredCloudRegion() string {
	return PrefixDesired + "/cloud/region"
}

// KeyDesiredCloudInstanceType returns the store path for the default instance
// type to provision.
// Path: desired/cloud/instance_type
func KeyDesiredCloudInstanceType() string {
	return PrefixDesired + "/cloud/instance_type"
}

// KeyDesiredCloudCredentials returns the store path for the cloud credentials
// reference (a cloud_identity name).
// Path: desired/cloud/credentials
func KeyDesiredCloudCredentials() string {
	return PrefixDesired + "/cloud/credentials"
}

// KeyDesiredCloudProjectID returns the store path for the cloud project ID
// (GCP-specific, also used by Azure as subscription_id).
// Path: desired/cloud/project_id
func KeyDesiredCloudProjectID() string {
	return PrefixDesired + "/cloud/project_id"
}

// KeyDesiredCloudResourceGroup returns the store path for the Azure resource group.
// Path: desired/cloud/resource_group
func KeyDesiredCloudResourceGroup() string {
	return PrefixDesired + "/cloud/resource_group"
}

// KeyDesiredCloudVPCNetwork returns the store path for the VPC/VNet network name.
// Path: desired/cloud/vpc_network
func KeyDesiredCloudVPCNetwork() string {
	return PrefixDesired + "/cloud/vpc_network"
}

// KeyDesiredCloudRouteTable returns the store path for the VPC route table ID.
// Path: desired/cloud/route_table
func KeyDesiredCloudRouteTable() string {
	return PrefixDesired + "/cloud/route_table"
}

// KeyDesiredServiceExposeExternal returns the store path marking a service port
// as externally exposed via a cloud load balancer. The value is the protocol
// ("tcp" or "http").
// Path: desired/service/{name}/expose/{port}/external
func KeyDesiredServiceExposeExternal(serviceName string, port int) string {
	return fmt.Sprintf("%s/service/%s/expose/%d/external", PrefixDesired, serviceName, port)
}

// ScanDesiredServiceExposeExternal returns the scan prefix for all externally
// exposed ports of a service.
func ScanDesiredServiceExposeExternal(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/expose/", PrefixDesired, serviceName)
}

// KeyObservedCloudInstanceState returns the store path for a cloud instance's
// lifecycle state as reported by the cloud provider.
// Path: observed/cloud/instance/{providerInstanceID}/state
func KeyObservedCloudInstanceState(providerInstanceID string) string {
	return fmt.Sprintf("%s/cloud/instance/%s/state", PrefixObserved, providerInstanceID)
}

// KeyObservedCloudInstanceNodeID returns the store path mapping a cloud instance
// to its CCattler node ID.
// Path: observed/cloud/instance/{providerInstanceID}/node_id
func KeyObservedCloudInstanceNodeID(providerInstanceID string) string {
	return fmt.Sprintf("%s/cloud/instance/%s/node_id", PrefixObserved, providerInstanceID)
}

// KeyObservedCloudLoadBalancerAddress returns the store path for a cloud load
// balancer's external address.
// Path: observed/cloud/loadbalancer/{serviceName}/address
func KeyObservedCloudLoadBalancerAddress(serviceName string) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/address", PrefixObserved, serviceName)
}

// KeyObservedCloudLoadBalancerState returns the store path for a cloud load
// balancer's operational state.
// Path: observed/cloud/loadbalancer/{serviceName}/state
func KeyObservedCloudLoadBalancerState(serviceName string) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/state", PrefixObserved, serviceName)
}

// KeyObservedCloudRoute returns the store path for an observed cloud VPC route.
// The value is the target node ID.
// Path: observed/cloud/route/{destinationCIDR}
func KeyObservedCloudRoute(destinationCIDR string) string {
	return fmt.Sprintf("%s/cloud/route/%s", PrefixObserved, destinationCIDR)
}

// KeyDerivedCloudLBPendingOperation returns the store path for a cloud load
// balancer's pending operation (ensure or delete). Written by the LB
// controller during Reconcile; executed post-commit by the executor.
// Path: derived/cloud/loadbalancer/{serviceName}/pending_operation
func KeyDerivedCloudLBPendingOperation(serviceName string) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/pending_operation", PrefixDerived, serviceName)
}

// KeyDerivedCloudLBPendingOperationForPort returns the per-port store path
// for a cloud load balancer pending operation. Multi-port services emit one
// pending operation per external port.
// Path: derived/cloud/loadbalancer/{serviceName}/pending_operation/{port}
func KeyDerivedCloudLBPendingOperationForPort(serviceName string, port int) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/pending_operation/%d", PrefixDerived, serviceName, port)
}

// KeyDerivedCloudLBAppliedHash returns the store path for the legacy
// per-service applied hash. Retained for backward compatibility during
// rolling upgrades — new code uses KeyDerivedCloudLBAppliedHashForPort.
// Path: derived/cloud/loadbalancer/{serviceName}/applied_hash
func KeyDerivedCloudLBAppliedHash(serviceName string) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/applied_hash", PrefixDerived, serviceName)
}

// KeyDerivedCloudLBAppliedHashForPort returns the per-port store path for
// the hash of the last successfully applied cloud load balancer config.
// Multi-port services store one hash per port to avoid overwrite races.
// Path: derived/cloud/loadbalancer/{serviceName}/applied_hash/{port}
func KeyDerivedCloudLBAppliedHashForPort(serviceName string, port int) string {
	return fmt.Sprintf("%s/cloud/loadbalancer/%s/applied_hash/%d", PrefixDerived, serviceName, port)
}

// KeyDerivedCloudRoutePendingOperation returns the store path for a cloud
// route's pending operation (ensure or delete). Written by the route
// controller during Reconcile; executed post-commit by the executor.
// Path: derived/cloud/route/{cidr}/pending_operation
func KeyDerivedCloudRoutePendingOperation(cidr string) string {
	return fmt.Sprintf("%s/cloud/route/%s/pending_operation", PrefixDerived, cidr)
}
