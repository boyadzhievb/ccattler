// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// containerHostnameIDLength matches runtime.containerHostnameIDLength — the
// number of instance ID characters appended to form a unique container hostname.
const containerHostnameIDLength = 10

// HostnameDNSController creates per-instance hostname DNS entries for all
// running containers. Each running instance with an IP gets a DNS entry
// mapping its container hostname ("{service}-{idPrefix}.cca.local") to its
// overlay IP. This enables cross-container hostname resolution — a container
// can resolve any other container's hostname via CCattler DNS, matching
// Docker's automatic /etc/hosts + DNS behavior.
type HostnameDNSController struct{}

// NewHostnameDNSController returns a ready-to-use HostnameDNSController.
func NewHostnameDNSController() *HostnameDNSController {
	return &HostnameDNSController{}
}

// Name returns "hostname-dns", identifying this controller in logs.
func (hostnameDNSController *HostnameDNSController) Name() string { return "hostname-dns" }

// Watch returns the fact prefixes the hostname DNS controller monitors:
// observed instances (for state, service, IP) and existing hostname DNS
// entries (for staleness detection).
func (hostnameDNSController *HostnameDNSController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
		types.ScanNetworkDNSHostnames,
	}
}

// Reconcile examines all running instances and emits changes to create missing
// hostname DNS entries and delete stale ones.
func (hostnameDNSController *HostnameDNSController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	existingHostnameDNS := parseExistingHostnameDNSEntries(facts)
	desiredHostnameDNS := buildDesiredHostnameDNSEntries(instanceFields)
	return computeHostnameDNSDiffChanges(desiredHostnameDNS, existingHostnameDNS), nil
}

// buildContainerHostname replicates runtime.buildUniqueContainerHostname to
// compute the same hostname the containerd/nerdctl runtime assigns to the
// container's kernel hostname.
func buildContainerHostname(serviceName string, instanceID string) string {
	idSuffix := instanceID
	if len(idSuffix) > containerHostnameIDLength {
		idSuffix = idSuffix[:containerHostnameIDLength]
	}
	return serviceName + "-" + idSuffix
}

// parseExistingHostnameDNSEntries scans hostname DNS facts and returns a map
// from hostname to IP.
func parseExistingHostnameDNSEntries(facts []store.Fact) map[string]string {
	existingDNS := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanNetworkDNSHostnames) {
		hostname := strings.TrimPrefix(fact.Key, types.ScanNetworkDNSHostnames)
		existingDNS[hostname] = string(fact.Value)
	}
	return existingDNS
}

// buildDesiredHostnameDNSEntries determines which hostname DNS entries should
// exist. Every running instance with a service name and IP gets a hostname
// entry: "{service}-{idPrefix}" → IP.
func buildDesiredHostnameDNSEntries(instanceFields map[string]map[string]string) map[string]string {
	desiredDNS := make(map[string]string)

	sortedInstanceIDs := make([]string, 0, len(instanceFields))
	for instanceID := range instanceFields {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}
		if effectiveInstanceState(fields) != types.InstanceRunning {
			continue
		}
		instanceIP := fields["ip"]
		if instanceIP == "" {
			continue
		}
		containerHostname := buildContainerHostname(serviceName, instanceID)
		desiredDNS[containerHostname] = instanceIP
	}
	return desiredDNS
}

// computeHostnameDNSDiffChanges compares desired hostname DNS entries against
// existing ones and returns changes to create missing entries and delete stale
// ones.
func computeHostnameDNSDiffChanges(desiredDNS map[string]string, existingDNS map[string]string) []Change {
	var changes []Change

	sortedDesiredHostnames := make([]string, 0, len(desiredDNS))
	for hostname := range desiredDNS {
		sortedDesiredHostnames = append(sortedDesiredHostnames, hostname)
	}
	sort.Strings(sortedDesiredHostnames)

	for _, hostname := range sortedDesiredHostnames {
		desiredIP := desiredDNS[hostname]
		if existingDNS[hostname] != desiredIP {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyNetworkDNSHostname(hostname),
				Value: []byte(desiredIP),
			})
		}
	}

	sortedExistingHostnames := make([]string, 0, len(existingDNS))
	for hostname := range existingDNS {
		sortedExistingHostnames = append(sortedExistingHostnames, hostname)
	}
	sort.Strings(sortedExistingHostnames)

	for _, hostname := range sortedExistingHostnames {
		if _, stillDesired := desiredDNS[hostname]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyNetworkDNSHostname(hostname),
			})
		}
	}

	return changes
}
