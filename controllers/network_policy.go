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

// NetworkPolicyController watches identity-based network policy rules and
// endpoint facts, resolves service identities to IP addresses, and writes
// compiled per-node IP-based firewall rules under derived/network/rule/{nodeID}/{index}.
// When instances move or endpoints change, the controller recompiles all rules
// so each node's firewall state converges to the desired policy.
type NetworkPolicyController struct{}

// NewNetworkPolicyController creates a NetworkPolicyController with no
// configuration — all state is derived from watched facts.
func NewNetworkPolicyController() *NetworkPolicyController {
	return &NetworkPolicyController{}
}

// Name returns "network-policy", identifying this controller in logs and
// the runner's bookkeeping.
func (networkPolicyController *NetworkPolicyController) Name() string {
	return "network-policy"
}

// Watch returns the fact prefixes the network policy controller monitors:
// identity-based policy rules, service endpoints (for IP resolution),
// scheduler placements (for instance-to-node mapping), and existing
// compiled rules (for diffing).
func (networkPolicyController *NetworkPolicyController) Watch() []string {
	return []string{
		types.ScanNetworkPolicies,
		types.ScanEndpoints,
		types.ScanPlacements,
		types.ScanDerivedNetworkRules,
	}
}

// identityPolicyRule is a parsed network policy rule with service identity
// names (e.g. "frontend/web") rather than IP addresses.
type identityPolicyRule struct {
	name          string // unique rule name from the fact key
	sourceService string // source service identity
	targetService string // target service identity
	port          int    // target port (0 means any port)
	action        string // "allow" or "deny"
}

// serviceEndpointEntry maps a running instance to its IP address within a
// service's endpoint set.
type serviceEndpointEntry struct {
	instanceID string // instance identifier
	ipAddress  string // instance's network IP
}

// targetNodeEntry associates an IP address with the node it lives on,
// used when compiling per-node firewall rules.
type targetNodeEntry struct {
	ipAddress string // instance IP
	nodeID    string // node hosting this instance
}

// Reconcile reads identity-based policy rules, resolves service names to
// instance IPs via endpoint facts, maps instances to nodes via placements,
// compiles IP-based firewall rules per node, and emits Put/Delete changes
// to converge derived/network/rule/ facts to the desired state.
func (networkPolicyController *NetworkPolicyController) Reconcile(
	_ context.Context, facts []store.Fact,
) ([]Change, error) {
	policyRules := parseIdentityPolicyRulesFromFacts(facts)
	serviceEndpoints := buildServiceEndpointMapFromFacts(facts)
	instanceNodeMap := buildInstanceNodeMapFromPlacements(facts)

	desiredRulesPerNode := compileIdentityRulesToIPRulesPerNode(
		policyRules, serviceEndpoints, instanceNodeMap)
	existingRulesPerNode := collectExistingDerivedNetworkRules(facts)

	return diffDerivedNetworkRules(desiredRulesPerNode, existingRulesPerNode), nil
}

// parseIdentityPolicyRulesFromFacts extracts identity-based network policy
// rules from policy/network/ facts. Each fact value is encoded as
// "source:target:port:action".
func parseIdentityPolicyRulesFromFacts(facts []store.Fact) []identityPolicyRule {
	var policyRules []identityPolicyRule
	for _, fact := range store.FactsWithPrefix(facts, types.ScanNetworkPolicies) {
		ruleName := strings.TrimPrefix(fact.Key, types.ScanNetworkPolicies)
		parsedRule, parseErr := parseIdentityPolicyRuleValue(ruleName, string(fact.Value))
		if parseErr != nil {
			continue
		}
		policyRules = append(policyRules, parsedRule)
	}
	return policyRules
}

// parseIdentityPolicyRuleValue deserializes a colon-delimited policy rule
// value into an identityPolicyRule struct.
func parseIdentityPolicyRuleValue(ruleName, ruleValue string) (identityPolicyRule, error) {
	parts := strings.SplitN(ruleValue, ":", 4)
	if len(parts) != 4 {
		return identityPolicyRule{}, fmt.Errorf("invalid rule format: %s", ruleValue)
	}
	port, parseError := strconv.Atoi(parts[2])
	if parseError != nil {
		return identityPolicyRule{}, fmt.Errorf("invalid port %q in rule %s: %w", parts[2], ruleName, parseError)
	}
	return identityPolicyRule{
		name:          ruleName,
		sourceService: parts[0],
		targetService: parts[1],
		port:          port,
		action:        parts[3],
	}, nil
}

// buildServiceEndpointMapFromFacts scans endpoint facts and builds a map
// from service name to a deduplicated list of (instanceID, ipAddress) entries.
// Endpoint keys have the form: endpoint/service/{service}/{instanceID}/{port}.
func buildServiceEndpointMapFromFacts(facts []store.Fact) map[string][]serviceEndpointEntry {
	serviceEndpoints := make(map[string][]serviceEndpointEntry)
	seenInstances := make(map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		serviceName, instanceID, ipAddress := parseEndpointFact(fact)
		if serviceName == "" || instanceID == "" {
			continue
		}
		dedupKey := serviceName + ":" + instanceID
		if seenInstances[dedupKey] {
			continue
		}
		seenInstances[dedupKey] = true
		serviceEndpoints[serviceName] = append(serviceEndpoints[serviceName],
			serviceEndpointEntry{instanceID: instanceID, ipAddress: ipAddress})
	}
	return serviceEndpoints
}

// parseEndpointFact extracts the service name, instance ID, and IP address
// from a single endpoint fact. Returns empty strings if the fact is malformed.
func parseEndpointFact(fact store.Fact) (string, string, string) {
	relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
	pathParts := strings.SplitN(relativePath, "/", 3)
	if len(pathParts) < 2 {
		return "", "", ""
	}
	serviceName := pathParts[0]
	instanceID := pathParts[1]
	ipAddress := extractIPFromEndpointValue(string(fact.Value))
	return serviceName, instanceID, ipAddress
}

// extractIPFromEndpointValue splits an "IP:port" endpoint value and returns
// just the IP portion.
func extractIPFromEndpointValue(endpointValue string) string {
	colonIndex := strings.LastIndex(endpointValue, ":")
	if colonIndex > 0 {
		return endpointValue[:colonIndex]
	}
	return endpointValue
}

// buildInstanceNodeMapFromPlacements builds a map from instanceID to nodeID
// using placement/instance/ facts written by the scheduler.
func buildInstanceNodeMapFromPlacements(facts []store.Fact) map[string]string {
	instanceNodeMap := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		instanceNodeMap[instanceID] = string(fact.Value)
	}
	return instanceNodeMap
}

// compileIdentityRulesToIPRulesPerNode resolves each identity-based policy
// rule into concrete IP-based rules and groups them by the target node.
// A rule is placed on the node hosting the target instance (destination-side
// enforcement). Each compiled rule value is "sourceIP:targetIP:port:action".
func compileIdentityRulesToIPRulesPerNode(
	policyRules []identityPolicyRule,
	serviceEndpoints map[string][]serviceEndpointEntry,
	instanceNodeMap map[string]string,
) map[string][]string {
	rulesPerNode := make(map[string][]string)

	for _, rule := range policyRules {
		sourceIPs := resolveServiceToIPAddresses(rule.sourceService, serviceEndpoints)
		targetEntries := resolveServiceToTargetNodeEntries(
			rule.targetService, serviceEndpoints, instanceNodeMap)
		appendCompiledRulesForPolicyRule(rulesPerNode, sourceIPs, targetEntries, rule)
	}

	sortCompiledRulesPerNode(rulesPerNode)
	return rulesPerNode
}

// resolveServiceToIPAddresses returns the IP addresses of all instances
// belonging to a service. A wildcard "*" service returns ["*"].
func resolveServiceToIPAddresses(
	serviceName string,
	serviceEndpoints map[string][]serviceEndpointEntry,
) []string {
	if serviceName == "*" {
		return []string{"*"}
	}
	entries := serviceEndpoints[serviceName]
	ipAddresses := make([]string, 0, len(entries))
	for _, entry := range entries {
		ipAddresses = append(ipAddresses, entry.ipAddress)
	}
	return ipAddresses
}

// resolveServiceToTargetNodeEntries resolves a target service name to a list
// of (IP, nodeID) pairs by joining endpoints with placement facts.
func resolveServiceToTargetNodeEntries(
	serviceName string,
	serviceEndpoints map[string][]serviceEndpointEntry,
	instanceNodeMap map[string]string,
) []targetNodeEntry {
	var entries []targetNodeEntry
	for _, endpoint := range serviceEndpoints[serviceName] {
		nodeID, hasPlacement := instanceNodeMap[endpoint.instanceID]
		if !hasPlacement {
			continue
		}
		entries = append(entries, targetNodeEntry{
			ipAddress: endpoint.ipAddress,
			nodeID:    nodeID,
		})
	}
	return entries
}

// appendCompiledRulesForPolicyRule generates IP-based rules for a single
// identity policy rule and appends them to the per-node map.
func appendCompiledRulesForPolicyRule(
	rulesPerNode map[string][]string,
	sourceIPs []string,
	targetEntries []targetNodeEntry,
	rule identityPolicyRule,
) {
	for _, target := range targetEntries {
		for _, sourceIP := range sourceIPs {
			compiledValue := formatCompiledFirewallRule(
				sourceIP, target.ipAddress, rule.port, rule.action)
			rulesPerNode[target.nodeID] = append(
				rulesPerNode[target.nodeID], compiledValue)
		}
	}
}

// formatCompiledFirewallRule produces the colon-delimited value stored in
// derived/network/rule/ facts: "sourceIP:targetIP:port:action".
func formatCompiledFirewallRule(sourceIP, targetIP string, port int, action string) string {
	return fmt.Sprintf("%s:%s:%d:%s", sourceIP, targetIP, port, action)
}

// sortCompiledRulesPerNode deterministically sorts the compiled rules for
// each node so that diff output is stable across reconciliations.
func sortCompiledRulesPerNode(rulesPerNode map[string][]string) {
	for nodeID := range rulesPerNode {
		sort.Strings(rulesPerNode[nodeID])
	}
}

// collectExistingDerivedNetworkRules reads the current derived/network/rule/
// facts and returns a map from nodeID to {ruleIndex → serialized value}.
func collectExistingDerivedNetworkRules(facts []store.Fact) map[string]map[int]string {
	existingRules := make(map[string]map[int]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedNetworkRules) {
		nodeID, ruleIndex, parseOK := parseDerivedNetworkRuleKey(fact.Key)
		if !parseOK {
			continue
		}
		if existingRules[nodeID] == nil {
			existingRules[nodeID] = make(map[int]string)
		}
		existingRules[nodeID][ruleIndex] = string(fact.Value)
	}
	return existingRules
}

// parseDerivedNetworkRuleKey extracts the nodeID and rule index from a
// derived/network/rule/{nodeID}/{index} key.
func parseDerivedNetworkRuleKey(factKey string) (string, int, bool) {
	nodeID, indexString, hasSuffix := splitFactKeyIntoEntityAndSuffix(factKey, types.ScanDerivedNetworkRules)
	if !hasSuffix {
		return "", 0, false
	}
	ruleIndex, parseErr := strconv.Atoi(indexString)
	if parseErr != nil {
		return "", 0, false
	}
	return nodeID, ruleIndex, true
}

// diffDerivedNetworkRules compares the desired compiled rules per node with
// the existing derived facts and returns the minimal set of Put and Delete
// changes needed to converge.
func diffDerivedNetworkRules(
	desiredRulesPerNode map[string][]string,
	existingRulesPerNode map[string]map[int]string,
) []Change {
	var changes []Change
	changes = append(changes, diffDesiredNodeRules(desiredRulesPerNode, existingRulesPerNode)...)
	changes = append(changes, removeStaleNodeRules(desiredRulesPerNode, existingRulesPerNode)...)
	return changes
}

// diffDesiredNodeRules emits Put changes for new or modified rules and Delete
// changes for excess indices on nodes that should have rules.
func diffDesiredNodeRules(
	desiredRulesPerNode map[string][]string,
	existingRulesPerNode map[string]map[int]string,
) []Change {
	var changes []Change
	for nodeID, desiredRules := range desiredRulesPerNode {
		existingForNode := existingRulesPerNode[nodeID]
		for ruleIndex, ruleValue := range desiredRules {
			if existingForNode != nil && existingForNode[ruleIndex] == ruleValue {
				continue
			}
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedNetworkRule(nodeID, ruleIndex),
				Value: []byte(ruleValue),
			})
		}
		changes = append(changes,
			removeExcessRulesForNode(nodeID, len(desiredRules), existingForNode)...)
	}
	return changes
}

// removeExcessRulesForNode deletes derived rule indices that exceed the
// desired count for a single node.
func removeExcessRulesForNode(
	nodeID string, desiredCount int, existingForNode map[int]string,
) []Change {
	var changes []Change
	for ruleIndex := range existingForNode {
		if ruleIndex >= desiredCount {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyDerivedNetworkRule(nodeID, ruleIndex),
			})
		}
	}
	return changes
}

// removeStaleNodeRules deletes all derived rules for nodes that are no
// longer present in the desired set.
func removeStaleNodeRules(
	desiredRulesPerNode map[string][]string,
	existingRulesPerNode map[string]map[int]string,
) []Change {
	var changes []Change
	for nodeID, existingForNode := range existingRulesPerNode {
		if _, hasDesired := desiredRulesPerNode[nodeID]; hasDesired {
			continue
		}
		for ruleIndex := range existingForNode {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyDerivedNetworkRule(nodeID, ruleIndex),
			})
		}
	}
	return changes
}
