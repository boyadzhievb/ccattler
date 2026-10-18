// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import "fmt"

// GenerateVIPRuleset builds a complete nftables ruleset string from the
// given service configurations without applying it. Used for benchmarking
// ruleset generation performance at different service counts.
func (nftablesDataPlane *NftablesDataPlane) GenerateVIPRuleset(serviceConfigs []ServiceVIPConfig) string {
	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)
	return nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)
}

// CountIptablesVIPCommands returns the number of sequential iptables command
// invocations needed to reconcile the given service configurations.
// Per service: 1 create-chain + 1 flush + N DNAT rules + 1 jump = N+3.
// Plus 4 global setup commands (create main chain, PREROUTING jump, OUTPUT
// jump, masquerade). Each command is a separate exec — this count directly
// represents the minimum wall-clock cost.
func CountIptablesVIPCommands(serviceConfigs []ServiceVIPConfig) int {
	// Global: create CCA_SERVICES (1) + PREROUTING jump (1) + OUTPUT jump (1) + masquerade (1)
	commandCount := 4
	for _, serviceConfig := range serviceConfigs {
		if len(serviceConfig.Backends) == 0 {
			continue
		}
		commandCount += len(serviceConfig.Backends) + 3
	}
	return commandCount
}

// GenerateSyntheticServiceConfigs creates a slice of ServiceVIPConfig
// entries with deterministic addresses for benchmarking. Each service
// gets the specified number of backends.
func GenerateSyntheticServiceConfigs(serviceCount int, backendsPerService int) []ServiceVIPConfig {
	serviceConfigs := make([]ServiceVIPConfig, serviceCount)
	for serviceIndex := range serviceConfigs {
		backends := make([]DataPlaneBackend, backendsPerService)
		for backendIndex := range backends {
			backends[backendIndex] = DataPlaneBackend{
				Address: fmt.Sprintf("10.%d.%d.%d",
					(serviceIndex/250)+1, (serviceIndex%250)+1, backendIndex+1),
				Port: 8080,
			}
		}
		vipOctet3 := (serviceIndex / 254) + 1
		vipOctet4 := (serviceIndex % 254) + 1
		serviceConfigs[serviceIndex] = ServiceVIPConfig{
			ServiceName: fmt.Sprintf("svc-%04d", serviceIndex),
			VirtualIP:   fmt.Sprintf("10.200.%d.%d", vipOctet3, vipOctet4),
			Port:        80,
			Protocol:    "tcp",
			Backends:    backends,
		}
	}
	return serviceConfigs
}
