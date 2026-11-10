// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NodesNotReporting returns the sorted registered, non-disabled nodes (an
// agent wrote observed/node/{id}/state) whose observed/node/{id}/{attributeName}
// is not expectedValue. Enrolled nodes that never ran an agent, such as an
// agentless control plane (enrolled at server start), have nothing to report
// and do not count. facts must cover observed/node/.
func NodesNotReporting(facts []store.Fact, attributeName string, expectedValue string) []string {
	knownNodes := make(map[string]bool)
	disabledNodes := make(map[string]bool)
	reportingNodes := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		nodeID, attributePath, hasAttribute := strings.Cut(strings.TrimPrefix(fact.Key, types.ScanObservedNodes), "/")
		switch {
		case !hasAttribute:
		case attributePath == "state":
			knownNodes[nodeID] = true
			disabledNodes[nodeID] = types.NodeState(fact.Value) == types.NodeDisabled
		case attributePath == attributeName:
			reportingNodes[nodeID] = string(fact.Value) == expectedValue
		}
	}
	pendingNodes := make([]string, 0)
	for nodeID := range knownNodes {
		if !disabledNodes[nodeID] && !reportingNodes[nodeID] {
			pendingNodes = append(pendingNodes, nodeID)
		}
	}
	sort.Strings(pendingNodes)
	return pendingNodes
}
