// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"strings"
	"testing"
)

func TestNftablesBuildVIPRulesetSingleService(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends: []DataPlaneBackend{
				{Address: "192.168.1.10", Port: 8080},
				{Address: "192.168.1.11", Port: 8080},
			},
		},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if !strings.Contains(ruleset, "table ip cca") {
		t.Error("ruleset should contain table declaration")
	}
	if !strings.Contains(ruleset, "chain prerouting") {
		t.Error("ruleset should contain prerouting chain")
	}
	if !strings.Contains(ruleset, "chain output") {
		t.Error("ruleset should contain output chain")
	}
	if !strings.Contains(ruleset, "chain postrouting") {
		t.Error("ruleset should contain postrouting chain")
	}
	if !strings.Contains(ruleset, "ct status dnat masquerade") {
		t.Error("ruleset should contain masquerade rule for DNAT'd packets")
	}
	if !strings.Contains(ruleset, "chain svc_web") {
		t.Error("ruleset should contain per-service chain svc_web")
	}
	if !strings.Contains(ruleset, "numgen inc mod 2") {
		t.Error("ruleset should contain numgen round-robin with mod 2 for 2 backends")
	}
	if !strings.Contains(ruleset, "10.200.0.1 . tcp . 80 : goto svc_web") {
		t.Error("ruleset should contain verdict map entry dispatching VIP to service chain")
	}

	if !desiredServiceChains["svc_web"] {
		t.Error("desiredServiceChains should contain svc_web")
	}
	if !desiredVIPAddresses["10.200.0.1"] {
		t.Error("desiredVIPAddresses should contain 10.200.0.1")
	}
}

func TestNftablesBuildVIPRulesetSingleBackend(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "redis",
			VirtualIP:   "10.200.0.2",
			Port:        6379,
			Backends: []DataPlaneBackend{
				{Address: "192.168.1.20", Port: 6379},
			},
		},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if strings.Contains(ruleset, "numgen") {
		t.Error("single-backend service should use direct dnat, not numgen")
	}
	if !strings.Contains(ruleset, "dnat to 192.168.1.20:6379") {
		t.Error("ruleset should contain direct dnat to the single backend")
	}
}

func TestNftablesBuildVIPRulesetEmptyBackendsSkipped(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "empty",
			VirtualIP:   "10.200.0.3",
			Port:        80,
			Backends:    []DataPlaneBackend{},
		},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if strings.Contains(ruleset, "svc_empty") {
		t.Error("service with no backends should not generate a chain")
	}
	if len(desiredServiceChains) != 0 {
		t.Errorf("desiredServiceChains should be empty, got %d entries", len(desiredServiceChains))
	}
}

func TestNftablesBuildVIPRulesetMultipleServices(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends:    []DataPlaneBackend{{Address: "10.0.0.1", Port: 8080}},
		},
		{
			ServiceName: "api",
			VirtualIP:   "10.200.0.2",
			Port:        443,
			Protocol:    "tcp",
			Backends: []DataPlaneBackend{
				{Address: "10.0.0.2", Port: 9090},
				{Address: "10.0.0.3", Port: 9090},
				{Address: "10.0.0.4", Port: 9090},
			},
		},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if !strings.Contains(ruleset, "chain svc_web") {
		t.Error("should contain svc_web chain")
	}
	if !strings.Contains(ruleset, "chain svc_api") {
		t.Error("should contain svc_api chain")
	}
	if !strings.Contains(ruleset, "numgen inc mod 3") {
		t.Error("api service with 3 backends should have numgen mod 3")
	}
	if len(desiredServiceChains) != 2 {
		t.Errorf("desiredServiceChains should have 2 entries, got %d", len(desiredServiceChains))
	}
}

func TestNftablesBuildVIPRulesetDeterministicOrder(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{ServiceName: "zeta", VirtualIP: "10.200.0.3", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.3", Port: 80}}},
		{ServiceName: "alpha", VirtualIP: "10.200.0.1", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.1", Port: 80}}},
		{ServiceName: "mid", VirtualIP: "10.200.0.2", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.2", Port: 80}}},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	alphaIndex := strings.Index(ruleset, "chain svc_alpha")
	midIndex := strings.Index(ruleset, "chain svc_mid")
	zetaIndex := strings.Index(ruleset, "chain svc_zeta")

	if alphaIndex > midIndex || midIndex > zetaIndex {
		t.Error("service chains should be ordered alphabetically by service name")
	}
}

func TestNftablesBuildVIPRulesetUDPProtocol(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "dns",
			VirtualIP:   "10.200.0.10",
			Port:        53,
			Protocol:    "udp",
			Backends: []DataPlaneBackend{
				{Address: "10.0.0.1", Port: 53},
			},
		},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if !strings.Contains(ruleset, "10.200.0.10 . udp . 53 : goto svc_dns") {
		t.Error("UDP service should have udp in the verdict map entry")
	}
}

func TestNftablesBuildRouteRulesetSourceIPSticky(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	routeConfigs := []RouteDataPlaneConfig{
		{
			RouteName:    "web-public",
			ServiceName:  "web",
			VirtualIP:    "10.200.0.1",
			Port:         80,
			StickyMethod: "source-ip",
			Backends: []DataPlaneBackend{
				{Address: "10.0.0.1", Port: 8080},
				{Address: "10.0.0.2", Port: 8080},
			},
		},
	}

	desiredRouteChains := make(map[string]bool)
	ruleset := nftablesDataPlane.buildRouteRuleset(routeConfigs, desiredRouteChains)

	if !strings.Contains(ruleset, "chain rt_web-public") {
		t.Error("should contain route chain")
	}
	if !strings.Contains(ruleset, "jhash ip saddr mod 2") {
		t.Error("source-ip sticky should use jhash on source address")
	}
	if !desiredRouteChains["rt_web-public"] {
		t.Error("desiredRouteChains should contain rt_web-public")
	}
}

func TestNftablesBuildRouteRulesetEmptyReturnsEmpty(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	desiredRouteChains := make(map[string]bool)
	ruleset := nftablesDataPlane.buildRouteRuleset(nil, desiredRouteChains)

	if ruleset != "" {
		t.Errorf("empty route config should produce empty ruleset, got %q", ruleset)
	}
}

func TestNftablesBuildVIPRulesetAtomicFlush(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.1", Port: 80}}},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if !strings.HasPrefix(ruleset, "table ip cca {}\ndelete table ip cca\n") {
		t.Error("ruleset should begin with create+delete for safe table replacement")
	}
}

func TestNftablesNewProviderInitializesCleanState(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()

	if nftablesDataPlane.initialized {
		t.Error("new provider should not be initialized")
	}
	if len(nftablesDataPlane.activeServiceChains) != 0 {
		t.Error("new provider should have no active service chains")
	}
	if len(nftablesDataPlane.activeRouteChains) != 0 {
		t.Error("new provider should have no active route chains")
	}
	if len(nftablesDataPlane.activeVIPAddresses) != 0 {
		t.Error("new provider should have no active VIP addresses")
	}
}

func TestNftablesBuildVIPRulesetVerdictMapHooksPresent(t *testing.T) {
	nftablesDataPlane := NewNftablesDataPlane()
	serviceConfigs := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.1", Port: 80}}},
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if !strings.Contains(ruleset, "hook prerouting priority -100") {
		t.Error("prerouting chain should hook at dstnat priority (-100)")
	}
	if !strings.Contains(ruleset, "hook output priority -100") {
		t.Error("output chain should hook at dstnat priority (-100) for host-originated traffic")
	}
	if !strings.Contains(ruleset, "hook postrouting priority 100") {
		t.Error("postrouting chain should hook at srcnat priority (100)")
	}
}

func TestNftablesEffectiveProtocolDefault(t *testing.T) {
	serviceConfig := ServiceVIPConfig{ServiceName: "test", Protocol: ""}
	if serviceConfig.EffectiveProtocol() != "tcp" {
		t.Errorf("empty protocol should default to tcp, got %q", serviceConfig.EffectiveProtocol())
	}
}

func TestNftablesEffectiveProtocolExplicit(t *testing.T) {
	serviceConfig := ServiceVIPConfig{ServiceName: "test", Protocol: "udp"}
	if serviceConfig.EffectiveProtocol() != "udp" {
		t.Errorf("explicit udp should remain udp, got %q", serviceConfig.EffectiveProtocol())
	}
}
