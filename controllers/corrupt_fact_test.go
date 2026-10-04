// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestCollectIntValuesBySuffixSkipsCorruptFacts verifies that a corrupt
// integer fact is omitted from the result rather than being stored as zero.
func TestCollectIntValuesBySuffixSkipsCorruptFacts(t *testing.T) {
	facts := []store.Fact{
		{Key: types.ScanDesiredServices + "web/instances", Value: []byte("3")},
		{Key: types.ScanDesiredServices + "api/instances", Value: []byte("not-a-number")},
	}

	collectedValues := collectIntValuesBySuffix(facts, types.ScanDesiredServices, "instances")

	if collectedValues["web"] != 3 {
		t.Errorf("web instances = %d, want 3", collectedValues["web"])
	}
	if _, present := collectedValues["api"]; present {
		t.Errorf("api instances should be omitted for a corrupt value, got %d", collectedValues["api"])
	}
}

// TestParseCompiledFirewallRuleRejectsCorruptPort verifies that a rule with a
// non-numeric port is reported as invalid.
func TestParseCompiledFirewallRuleRejectsCorruptPort(t *testing.T) {
	_, _, _, _, parsedOK := parseCompiledFirewallRule("10.0.0.1:10.0.0.2:http:allow")
	if parsedOK {
		t.Error("expected rule with non-numeric port to be rejected")
	}
}

// TestParseIdentityPolicyRuleValueRejectsCorruptPort verifies that a policy
// rule with a non-numeric port returns an error.
func TestParseIdentityPolicyRuleValueRejectsCorruptPort(t *testing.T) {
	_, parseError := parseIdentityPolicyRuleValue("rule-1", "web:api:http:allow")
	if parseError == nil {
		t.Error("expected error for non-numeric port")
	}
}
