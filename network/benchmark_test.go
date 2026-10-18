// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"strings"
	"testing"
)

func TestGenerateSyntheticServiceConfigsCount(t *testing.T) {
	serviceConfigs := GenerateSyntheticServiceConfigs(100, 3)
	if len(serviceConfigs) != 100 {
		t.Errorf("expected 100 service configs, got %d", len(serviceConfigs))
	}
	for _, serviceConfig := range serviceConfigs {
		if len(serviceConfig.Backends) != 3 {
			t.Errorf("expected 3 backends for %s, got %d",
				serviceConfig.ServiceName, len(serviceConfig.Backends))
		}
		if serviceConfig.Protocol != "tcp" {
			t.Errorf("expected tcp protocol for %s, got %s",
				serviceConfig.ServiceName, serviceConfig.Protocol)
		}
	}
}

func TestGenerateSyntheticServiceConfigsUniqueVIPs(t *testing.T) {
	serviceConfigs := GenerateSyntheticServiceConfigs(500, 2)
	seenVIPs := make(map[string]bool)
	for _, serviceConfig := range serviceConfigs {
		if seenVIPs[serviceConfig.VirtualIP] {
			t.Errorf("duplicate VIP: %s", serviceConfig.VirtualIP)
		}
		seenVIPs[serviceConfig.VirtualIP] = true
	}
}

func TestCountIptablesVIPCommandsScaling(t *testing.T) {
	tenServices := GenerateSyntheticServiceConfigs(10, 3)
	hundredServices := GenerateSyntheticServiceConfigs(100, 3)

	tenCount := CountIptablesVIPCommands(tenServices)
	hundredCount := CountIptablesVIPCommands(hundredServices)

	// 10 services × (3+3) + 4 global = 64
	expectedTen := 10*(3+3) + 4
	if tenCount != expectedTen {
		t.Errorf("10 services: expected %d commands, got %d", expectedTen, tenCount)
	}

	// 100 services × (3+3) + 4 global = 604
	expectedHundred := 100*(3+3) + 4
	if hundredCount != expectedHundred {
		t.Errorf("100 services: expected %d commands, got %d", expectedHundred, hundredCount)
	}

	// Must scale linearly
	if hundredCount <= tenCount {
		t.Error("iptables command count must grow with service count")
	}
}

func TestGenerateVIPRulesetProducesValidRuleset(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := GenerateSyntheticServiceConfigs(10, 3)

	ruleset := nftablesDataPlane.GenerateVIPRuleset(serviceConfigs)

	if !strings.Contains(ruleset, "table ip cca") {
		t.Error("ruleset should contain table declaration")
	}
	if !strings.Contains(ruleset, "map vip_dispatch") {
		t.Error("ruleset should contain verdict map")
	}
	if !strings.Contains(ruleset, "chain svc_svc-0000") {
		t.Error("ruleset should contain per-service chains")
	}
	if !strings.Contains(ruleset, "numgen inc mod 3") {
		t.Error("ruleset should contain round-robin numgen for 3 backends")
	}
}

func TestGenerateVIPRulesetScaling(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()

	smallRuleset := nftablesDataPlane.GenerateVIPRuleset(
		GenerateSyntheticServiceConfigs(10, 3))
	largeRuleset := nftablesDataPlane.GenerateVIPRuleset(
		GenerateSyntheticServiceConfigs(100, 3))

	if len(largeRuleset) <= len(smallRuleset) {
		t.Error("larger service count should produce larger ruleset")
	}

	smallChainCount := strings.Count(smallRuleset, "chain svc_")
	largeChainCount := strings.Count(largeRuleset, "chain svc_")
	if smallChainCount != 10 {
		t.Errorf("expected 10 service chains, got %d", smallChainCount)
	}
	if largeChainCount != 100 {
		t.Errorf("expected 100 service chains, got %d", largeChainCount)
	}
}

func TestCountIptablesVIPCommandsEmptyBackends(t *testing.T) {
	serviceConfigs := []ServiceVIPConfig{
		{ServiceName: "empty", VirtualIP: "10.200.0.1", Port: 80},
	}
	commandCount := CountIptablesVIPCommands(serviceConfigs)
	// Only global setup commands when no backends
	if commandCount != 4 {
		t.Errorf("expected 4 global commands for empty service, got %d", commandCount)
	}
}
