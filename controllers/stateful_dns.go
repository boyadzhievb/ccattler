// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// statefulDNSSuffix is the DNS suffix used for per-instance stable DNS names
// in stateful services (e.g. "postgres-0.ccattler.local").
const statefulDNSSuffix = ".ccattler.local"

// StatefulDNSController creates per-instance DNS entries for stateful services.
// Each running stateful instance with an IP address gets a stable DNS name of
// the form "{service}-{ordinal}.ccattler.local" that resolves to its IP. This
// enables clients to address specific ordinals by name.
type StatefulDNSController struct{}

// NewStatefulDNSController returns a ready-to-use StatefulDNSController.
func NewStatefulDNSController() *StatefulDNSController {
	return &StatefulDNSController{}
}

// Name returns "stateful-dns", identifying this controller in logs and runner
// bookkeeping.
func (statefulDNSController *StatefulDNSController) Name() string { return "stateful-dns" }

// Watch returns the fact prefixes the stateful DNS controller monitors:
// effective services (for stateful flag), observed instances (for ordinal, IP,
// and state), and existing per-instance DNS entries (for staleness detection).
func (statefulDNSController *StatefulDNSController) Watch() []string {
	return []string{
		types.ScanEffectiveServices,
		types.ScanObservedInstances,
		types.ScanNetworkDNSInstances,
	}
}

// Reconcile examines stateful services and their running instances, then emits
// changes to create missing per-instance DNS entries and delete stale ones.
func (statefulDNSController *StatefulDNSController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	_, statefulServices := parseEffectiveServiceFacts(facts)
	instanceFields := parseInstanceFieldsFromFacts(facts)
	existingInstanceDNS := parseExistingInstanceDNSEntries(facts)

	desiredInstanceDNS := buildDesiredStatefulDNSEntries(statefulServices, instanceFields)

	return computeStatefulDNSDiffChanges(desiredInstanceDNS, existingInstanceDNS), nil
}

// parseExistingInstanceDNSEntries scans per-instance DNS facts and returns a
// map from instance ID to the existing DNS value (IP address).
func parseExistingInstanceDNSEntries(facts []store.Fact) map[string]string {
	existingDNS := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanNetworkDNSInstances) {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanNetworkDNSInstances)
		existingDNS[instanceID] = string(fact.Value)
	}
	return existingDNS
}

// buildDesiredStatefulDNSEntries determines which per-instance DNS entries
// should exist. For each stateful service, running instances with an IP and an
// ordinal get a DNS entry mapping "{service}-{ordinal}.ccattler.local" to their
// IP. The map key is the instance ID; the value is the DNS record content
// ("{dnsName}={ipAddress}").
func buildDesiredStatefulDNSEntries(
	statefulServices map[string]bool,
	instanceFields map[string]map[string]string,
) map[string]string {
	desiredDNS := make(map[string]string)

	sortedInstanceIDs := make([]string, 0, len(instanceFields))
	for instanceID := range instanceFields {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		serviceName := fields["service"]
		if !statefulServices[serviceName] {
			continue
		}
		if effectiveInstanceState(fields) != types.InstanceRunning {
			continue
		}
		instanceIP := fields["ip"]
		if instanceIP == "" {
			continue
		}
		ordinalStr := fields["ordinal"]
		if ordinalStr == "" {
			continue
		}
		ordinal, parseErr := strconv.Atoi(ordinalStr)
		if parseErr != nil {
			continue
		}
		dnsName := fmt.Sprintf("%s-%d%s", serviceName, ordinal, statefulDNSSuffix)
		desiredDNS[instanceID] = dnsName + "=" + instanceIP
	}
	return desiredDNS
}

// computeStatefulDNSDiffChanges compares desired per-instance DNS entries
// against existing ones and returns changes to create missing entries and
// delete stale ones.
func computeStatefulDNSDiffChanges(desiredDNS map[string]string, existingDNS map[string]string) []Change {
	var changes []Change

	sortedDesiredKeys := make([]string, 0, len(desiredDNS))
	for instanceID := range desiredDNS {
		sortedDesiredKeys = append(sortedDesiredKeys, instanceID)
	}
	sort.Strings(sortedDesiredKeys)

	for _, instanceID := range sortedDesiredKeys {
		desiredValue := desiredDNS[instanceID]
		if existingDNS[instanceID] != desiredValue {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyNetworkDNSInstance(instanceID),
				Value: []byte(desiredValue),
			})
		}
	}

	sortedExistingKeys := make([]string, 0, len(existingDNS))
	for instanceID := range existingDNS {
		sortedExistingKeys = append(sortedExistingKeys, instanceID)
	}
	sort.Strings(sortedExistingKeys)

	for _, instanceID := range sortedExistingKeys {
		if _, stillDesired := desiredDNS[instanceID]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyNetworkDNSInstance(instanceID),
			})
		}
	}

	return changes
}
