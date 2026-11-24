// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyEndpoint returns the store path for a service instance's network endpoint
// on a specific port. The value stored at this key is the IP:port pair.
// Path: endpoint/service/{serviceName}/{instanceID}/{port}
func KeyEndpoint(serviceName, instanceID string, port int) string {
	return fmt.Sprintf("%s/service/%s/%s/%d", PrefixEndpoint, serviceName, instanceID, port)
}

// KeyNetworkNodeSubnet returns the store path for a node's assigned subnet CIDR.
// Path: network/node/{nodeID}/subnet
func KeyNetworkNodeSubnet(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/subnet", PrefixNetwork, nodeID)
}

// KeyNetworkAllocation returns the store path for an instance's allocated IP address.
// Path: network/allocation/{instanceID}
func KeyNetworkAllocation(instanceID string) string {
	return fmt.Sprintf("%s/allocation/%s", PrefixNetwork, instanceID)
}

// KeyNetworkVIPService returns the store path for a service's virtual IP address.
// Path: network/vip/service/{serviceName}
func KeyNetworkVIPService(serviceName string) string {
	return fmt.Sprintf("%s/vip/service/%s", PrefixNetwork, serviceName)
}

// KeyNetworkVIPServicePort returns the store path for a service VIP's port number.
// Path: network/vip/service/{serviceName}/port
func KeyNetworkVIPServicePort(serviceName string) string {
	return fmt.Sprintf("%s/vip/service/%s/port", PrefixNetwork, serviceName)
}

// KeyNetworkDNS returns the store path for a service's DNS name-to-VIP mapping.
// Path: network/dns/{serviceName}
func KeyNetworkDNS(serviceName string) string {
	return fmt.Sprintf("%s/dns/%s", PrefixNetwork, serviceName)
}

// KeyNetworkDNSInstance returns the store path for a per-instance DNS name
// used by stateful services. The name resolves to the instance's IP address
// (e.g. "postgres-0.cca.local" → "10.0.1.4").
// Path: network/dns/instance/{instanceID}
func KeyNetworkDNSInstance(instanceID string) string {
	return fmt.Sprintf("%s/dns/instance/%s", PrefixNetwork, instanceID)
}

// ScanNetworkDNSInstances scans all per-instance DNS entries for stateful services.
const ScanNetworkDNSInstances = PrefixNetwork + "/dns/instance/"

// KeyNetworkDNSHostname returns the store path for a container hostname DNS
// entry. Maps the unique container hostname (e.g. "worker-a1b2c3d4e5") to its
// overlay IP so that other containers can resolve it via CCattler DNS.
// Path: network/dns/hostname/{hostname}
func KeyNetworkDNSHostname(hostname string) string {
	return fmt.Sprintf("%s/dns/hostname/%s", PrefixNetwork, hostname)
}

// ScanNetworkDNSHostnames scans all per-instance hostname DNS entries.
const ScanNetworkDNSHostnames = PrefixNetwork + "/dns/hostname/"

// ScanNetworkPolicies scans all network policy rules under the policy/network/ prefix.
const ScanNetworkPolicies = "policy/network/"

// KeyNetworkPolicyRule returns the store path for a named network policy rule.
// The value is encoded as "source:target:port:action".
// Path: policy/network/{ruleName}
func KeyNetworkPolicyRule(ruleName string) string {
	return ScanNetworkPolicies + ruleName
}

// ScanDerivedNetworkRules scans compiled IP-based firewall rules written by
// the NetworkPolicyController.
const ScanDerivedNetworkRules = "derived/network/rule/"

// KeyDerivedNetworkRule returns the store path for a compiled per-node firewall rule.
// Path: derived/network/rule/{nodeID}/{ruleIndex}
func KeyDerivedNetworkRule(nodeID string, ruleIndex int) string {
	return fmt.Sprintf("%s%s/%d", ScanDerivedNetworkRules, nodeID, ruleIndex)
}
