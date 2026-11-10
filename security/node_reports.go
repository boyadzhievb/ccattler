// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NodesNotReporting returns the sorted nodes (enrolled or registered, not
// disabled) whose observed/node/{id}/{attributeName} is not expectedValue.
// Counting registered nodes too keeps clusters without enrollment from
// passing a cutover gate early. facts must cover the enrollment records and
// observed/node/.
func NodesNotReporting(facts []store.Fact, attributeName string, expectedValue string) []string {
	knownNodes := make(map[string]bool)
	disabledNodes := make(map[string]bool)
	reportingNodes := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, EnrolledNodePrefix) {
		if nodeID := strings.TrimPrefix(fact.Key, EnrolledNodePrefix); nodeID != "" && !strings.Contains(nodeID, "/") {
			knownNodes[nodeID] = true
		}
	}
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
