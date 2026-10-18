// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"testing"
)

func TestCompareShadowServicesNoBackends(t *testing.T) {
	serviceConfigs := []ServiceVIPConfig{
		{ServiceName: "empty", VirtualIP: "10.200.0.1", Port: 80},
	}
	divergences := compareShadowServices(serviceConfigs, "")
	if len(divergences) != 0 {
		t.Errorf("expected no divergences for empty backends, got %d", len(divergences))
	}
}

func TestCompareShadowServicesMissingChain(t *testing.T) {
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends:    []DataPlaneBackend{{Address: "10.0.0.1", Port: 8080}},
		},
	}
	divergences := compareShadowServices(serviceConfigs, "table ip cca {}")
	if len(divergences) != 1 {
		t.Fatalf("expected 1 divergence for missing chain, got %d", len(divergences))
	}
	if divergences[0] == "" {
		t.Error("divergence message should not be empty")
	}
}

func TestCompareShadowServicesMatchingRuleset(t *testing.T) {
	serviceConfigs := GenerateSyntheticServiceConfigs(5, 3)
	nftablesDataPlane := NewNftablesDataPlane()
	ruleset := nftablesDataPlane.GenerateVIPRuleset(serviceConfigs)

	divergences := compareShadowServices(serviceConfigs, ruleset)
	if len(divergences) != 0 {
		t.Errorf("expected no divergences for matching ruleset, got %d: %v", len(divergences), divergences)
	}
}

func TestCompareShadowServicesSingleBackendNoDivergence(t *testing.T) {
	serviceConfigs := []ServiceVIPConfig{
		{
			ServiceName: "single",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Protocol:    "tcp",
			Backends:    []DataPlaneBackend{{Address: "10.0.0.1", Port: 8080}},
		},
	}
	nftablesDataPlane := NewNftablesDataPlane()
	ruleset := nftablesDataPlane.GenerateVIPRuleset(serviceConfigs)

	divergences := compareShadowServices(serviceConfigs, ruleset)
	if len(divergences) != 0 {
		t.Errorf("expected no divergences for single-backend service, got %d", len(divergences))
	}
}

func TestCompareShadowServicesBackendCountMismatch(t *testing.T) {
	twoBackendConfigs := GenerateSyntheticServiceConfigs(1, 2)
	threeBackendConfigs := GenerateSyntheticServiceConfigs(1, 3)

	nftablesDataPlane := NewNftablesDataPlane()
	rulesetForTwo := nftablesDataPlane.GenerateVIPRuleset(twoBackendConfigs)

	divergences := compareShadowServices(threeBackendConfigs, rulesetForTwo)
	if len(divergences) != 1 {
		t.Errorf("expected 1 divergence for backend count mismatch, got %d: %v",
			len(divergences), divergences)
	}
}
