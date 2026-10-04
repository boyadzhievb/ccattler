// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"strings"
	"testing"
)

// TestNftablesRuleSetRendersAllowRule verifies that a compiled allow rule
// produces the correct nft command with source, destination, and port match.
func TestNftablesRuleSetRendersAllowRule(t *testing.T) {
	nftablesRuleSet := NewNftablesRuleSet()
	commands := nftablesRuleSet.RenderRules([]string{"10.0.1.1:10.0.2.1:443:allow"})

	// 2 setup + 1 conntrack + 1 rule = 4 commands
	if len(commands) != 4 {
		t.Fatalf("expected 4 commands, got %d: %v", len(commands), commands)
	}

	ruleCommand := commands[3]
	expectedFragments := []string{
		"ip saddr 10.0.1.1",
		"ip daddr 10.0.2.1",
		"tcp dport 443",
		"accept",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(ruleCommand, fragment) {
			t.Errorf("rule %q missing fragment %q", ruleCommand, fragment)
		}
	}
}

// TestNftablesRuleSetRendersDenyRule verifies that a deny action maps to
// the nftables "drop" verdict.
func TestNftablesRuleSetRendersDenyRule(t *testing.T) {
	nftablesRuleSet := NewNftablesRuleSet()
	commands := nftablesRuleSet.RenderRules([]string{"10.0.1.1:10.0.3.1:5432:deny"})

	ruleCommand := commands[len(commands)-1]
	if !strings.Contains(ruleCommand, "drop") {
		t.Errorf("expected deny to map to drop, got: %s", ruleCommand)
	}
	if strings.Contains(ruleCommand, "accept") {
		t.Errorf("deny rule should not contain accept: %s", ruleCommand)
	}
}

// TestNftablesRuleSetIncludesSetupCommands verifies that the rendered output
// starts with table creation, chain creation, and conntrack rules.
func TestNftablesRuleSetIncludesSetupCommands(t *testing.T) {
	nftablesRuleSet := NewNftablesRuleSet()
	commands := nftablesRuleSet.RenderRules(nil)

	// Even with no rules: 2 setup + 1 conntrack = 3
	if len(commands) != 3 {
		t.Fatalf("expected 3 setup commands, got %d: %v", len(commands), commands)
	}

	if !strings.Contains(commands[0], "add table inet ccattler") {
		t.Errorf("first command should create table: %s", commands[0])
	}
	if !strings.Contains(commands[1], "add chain inet ccattler forward") {
		t.Errorf("second command should create chain: %s", commands[1])
	}
	if !strings.Contains(commands[1], "policy drop") {
		t.Errorf("chain should have default-drop policy: %s", commands[1])
	}
	if !strings.Contains(commands[2], "ct state established,related accept") {
		t.Errorf("third command should be conntrack rule: %s", commands[2])
	}
}

// TestNftablesRuleSetWildcardSourceOmitsSaddr verifies that a "*" source IP
// produces a rule without an ip saddr match clause.
func TestNftablesRuleSetWildcardSourceOmitsSaddr(t *testing.T) {
	nftablesRuleSet := NewNftablesRuleSet()
	commands := nftablesRuleSet.RenderRules([]string{"*:10.0.2.1:443:allow"})

	ruleCommand := commands[len(commands)-1]
	if strings.Contains(ruleCommand, "ip saddr") {
		t.Errorf("wildcard source should omit saddr: %s", ruleCommand)
	}
	if !strings.Contains(ruleCommand, "ip daddr 10.0.2.1") {
		t.Errorf("should still have daddr: %s", ruleCommand)
	}
}

// TestNftablesRuleSetZeroPortOmitsDport verifies that port 0 (any port)
// produces a rule without a tcp dport match clause.
func TestNftablesRuleSetZeroPortOmitsDport(t *testing.T) {
	nftablesRuleSet := NewNftablesRuleSet()
	commands := nftablesRuleSet.RenderRules([]string{"10.0.1.1:10.0.2.1:0:allow"})

	ruleCommand := commands[len(commands)-1]
	if strings.Contains(ruleCommand, "tcp dport") {
		t.Errorf("port 0 should omit dport: %s", ruleCommand)
	}
}
