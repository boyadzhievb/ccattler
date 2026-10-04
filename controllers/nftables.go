// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// defaultNftablesTableName is the nftables table used for CCattler firewall rules.
	defaultNftablesTableName = "ccattler"
	// defaultNftablesChainName is the chain within the CCattler table for forwarded traffic.
	defaultNftablesChainName = "forward"
)

// NftablesRuleSet renders compiled IP-based firewall rules into nftables
// commands suitable for applying to a node's packet filter. Each compiled
// rule (stored as "sourceIP:targetIP:port:action") becomes an nft rule
// with source/destination address matches and a port match.
type NftablesRuleSet struct {
	tableName string // nftables table name (default "ccattler")
	chainName string // nftables chain name (default "forward")
}

// NewNftablesRuleSet creates an NftablesRuleSet with the default table
// and chain names ("ccattler" / "forward").
func NewNftablesRuleSet() *NftablesRuleSet {
	return &NftablesRuleSet{
		tableName: defaultNftablesTableName,
		chainName: defaultNftablesChainName,
	}
}

// RenderRules converts a slice of compiled firewall rule values into a
// sequence of nft commands. The output begins with table/chain setup and
// a conntrack rule for established connections, followed by one rule per
// compiled entry. The chain's default policy is drop (deny-by-default).
func (nftablesRuleSet *NftablesRuleSet) RenderRules(compiledRules []string) []string {
	commands := nftablesRuleSet.renderTableAndChainSetup()
	commands = append(commands, nftablesRuleSet.renderConntrackRule())
	for _, compiledRule := range compiledRules {
		ruleCommand := nftablesRuleSet.renderSingleRule(compiledRule)
		if ruleCommand != "" {
			commands = append(commands, ruleCommand)
		}
	}
	return commands
}

// renderTableAndChainSetup emits the nft commands that create the inet
// table and a forward chain with a default-drop policy.
func (nftablesRuleSet *NftablesRuleSet) renderTableAndChainSetup() []string {
	return []string{
		fmt.Sprintf("add table inet %s", nftablesRuleSet.tableName),
		fmt.Sprintf("add chain inet %s %s { type filter hook forward priority 0 ; policy drop ; }",
			nftablesRuleSet.tableName, nftablesRuleSet.chainName),
	}
}

// renderConntrackRule emits an nft rule that accepts packets belonging to
// established or related connections, so only new flows need explicit allow.
func (nftablesRuleSet *NftablesRuleSet) renderConntrackRule() string {
	return fmt.Sprintf("add rule inet %s %s ct state established,related accept",
		nftablesRuleSet.tableName, nftablesRuleSet.chainName)
}

// renderSingleRule converts one compiled "sourceIP:targetIP:port:action"
// value into an nft rule string. Returns an empty string for malformed input.
func (nftablesRuleSet *NftablesRuleSet) renderSingleRule(compiledRule string) string {
	sourceIP, targetIP, port, action, parseOK := parseCompiledFirewallRule(compiledRule)
	if !parseOK {
		return ""
	}

	nftAction := mapPolicyActionToNftVerdict(action)
	rulePrefix := fmt.Sprintf("add rule inet %s %s",
		nftablesRuleSet.tableName, nftablesRuleSet.chainName)

	matchClauses := buildNftMatchClauses(sourceIP, targetIP, port)
	if matchClauses == "" {
		return fmt.Sprintf("%s %s", rulePrefix, nftAction)
	}
	return fmt.Sprintf("%s %s %s", rulePrefix, matchClauses, nftAction)
}

// parseCompiledFirewallRule splits a compiled "sourceIP:targetIP:port:action"
// value into its four components.
func parseCompiledFirewallRule(compiledRule string) (string, string, int, string, bool) {
	parts := strings.SplitN(compiledRule, ":", 4)
	if len(parts) != 4 {
		return "", "", 0, "", false
	}
	port, parseError := strconv.Atoi(parts[2])
	if parseError != nil {
		return "", "", 0, "", false
	}
	return parts[0], parts[1], port, parts[3], true
}

// mapPolicyActionToNftVerdict translates an "allow"/"deny" action to the
// corresponding nftables verdict ("accept" or "drop").
func mapPolicyActionToNftVerdict(action string) string {
	if action == "allow" {
		return "accept"
	}
	return "drop"
}

// buildNftMatchClauses assembles the nft match expressions for source IP,
// destination IP, and destination port. Wildcard "*" values and port 0
// are omitted.
func buildNftMatchClauses(sourceIP, targetIP string, port int) string {
	var clauses []string
	if sourceIP != "*" && sourceIP != "" {
		clauses = append(clauses, fmt.Sprintf("ip saddr %s", sourceIP))
	}
	if targetIP != "*" && targetIP != "" {
		clauses = append(clauses, fmt.Sprintf("ip daddr %s", targetIP))
	}
	if port > 0 {
		clauses = append(clauses, fmt.Sprintf("tcp dport %d", port))
	}
	return strings.Join(clauses, " ")
}
