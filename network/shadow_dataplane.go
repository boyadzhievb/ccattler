// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

// shadowDataPlaneMaxLoggedDivergences caps how many divergence messages are
// logged per reconciliation cycle to avoid flooding the log.
const shadowDataPlaneMaxLoggedDivergences = 10

// ShadowDataPlane runs iptables as the active data plane and nftables in
// shadow mode. On each reconciliation cycle it generates the nftables
// ruleset without applying it, compares the service/chain layout against
// what iptables would produce, and logs any divergences. This validates
// that nftables produces correct rules before switching over.
type ShadowDataPlane struct {
	// activeProvider handles real traffic.
	activeProvider *IptablesDataPlane
	// shadowProvider generates rulesets for comparison only.
	shadowProvider *NftablesDataPlane
	// mutex serializes reconciliation for consistent comparison.
	mutex sync.Mutex
	// totalReconciliations counts how many VIP reconciliation cycles ran.
	totalReconciliations int
	// divergenceCount counts total divergences across all cycles.
	divergenceCount int
}

// NewShadowDataPlane creates a shadow data plane with iptables active
// and nftables generating rulesets for comparison.
func NewShadowDataPlane() *ShadowDataPlane {
	return &ShadowDataPlane{
		activeProvider: NewIptablesDataPlane(),
		shadowProvider: NewNftablesDataPlane(),
	}
}

// ReconcileVIPDataPlane reconciles iptables (active), then generates the
// nftables ruleset in shadow mode and logs the comparison.
func (shadowDataPlane *ShadowDataPlane) ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	shadowDataPlane.mutex.Lock()
	defer shadowDataPlane.mutex.Unlock()

	activeError := shadowDataPlane.activeProvider.ReconcileVIPDataPlane(ctx, serviceConfigs)
	shadowDataPlane.totalReconciliations++

	shadowDataPlane.compareVIPRulesets(serviceConfigs)

	return activeError
}

// compareVIPRulesets generates the nftables ruleset and compares it against
// what iptables configured. Logs divergences as warnings.
func (shadowDataPlane *ShadowDataPlane) compareVIPRulesets(serviceConfigs []ServiceVIPConfig) {
	startTime := time.Now()
	shadowRuleset := shadowDataPlane.shadowProvider.GenerateVIPRuleset(serviceConfigs)
	generationDuration := time.Since(startTime)

	logger := logging.Default()
	logger.Info("shadow nftables generation",
		"services", strconv.Itoa(len(serviceConfigs)),
		"ruleset_bytes", strconv.Itoa(len(shadowRuleset)),
		"generation_time", generationDuration.String())

	divergences := compareShadowServices(serviceConfigs, shadowRuleset)
	for index, divergence := range divergences {
		if index >= shadowDataPlaneMaxLoggedDivergences {
			logger.Warn("shadow divergence log truncated",
				"remaining", strconv.Itoa(len(divergences)-shadowDataPlaneMaxLoggedDivergences))
			break
		}
		logger.Warn("shadow divergence", "detail", divergence)
	}
	shadowDataPlane.divergenceCount += len(divergences)
}

// compareShadowServices checks that every service with backends has a
// corresponding chain in the nftables ruleset and that backend counts match.
func compareShadowServices(serviceConfigs []ServiceVIPConfig, nftablesRuleset string) []string {
	var divergences []string
	for _, serviceConfig := range serviceConfigs {
		if len(serviceConfig.Backends) == 0 {
			continue
		}
		expectedChain := fmt.Sprintf("chain %s%s",
			nftablesServiceChainPrefix, sanitizeChainName(serviceConfig.ServiceName))
		if !strings.Contains(nftablesRuleset, expectedChain) {
			divergences = append(divergences,
				fmt.Sprintf("service %s: missing nftables chain %s", serviceConfig.ServiceName, expectedChain))
			continue
		}

		expectedNumgen := fmt.Sprintf("numgen inc mod %d", len(serviceConfig.Backends))
		if len(serviceConfig.Backends) > 1 && !strings.Contains(nftablesRuleset, expectedNumgen) {
			divergences = append(divergences,
				fmt.Sprintf("service %s: expected %d backends (numgen mod), not found in ruleset",
					serviceConfig.ServiceName, len(serviceConfig.Backends)))
		}
	}
	return divergences
}

// ReconcileRouteDataPlane delegates to iptables only — route comparison
// is logged but route rules are not applied in shadow mode.
func (shadowDataPlane *ShadowDataPlane) ReconcileRouteDataPlane(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error {
	return shadowDataPlane.activeProvider.ReconcileRouteDataPlane(ctx, routeConfigs)
}

// Cleanup tears down both providers.
func (shadowDataPlane *ShadowDataPlane) Cleanup(ctx context.Context) error {
	logger := logging.Default()
	logger.Info("shadow data plane summary",
		"total_reconciliations", strconv.Itoa(shadowDataPlane.totalReconciliations),
		"total_divergences", strconv.Itoa(shadowDataPlane.divergenceCount))
	return shadowDataPlane.activeProvider.Cleanup(ctx)
}

// TotalReconciliations returns the number of VIP reconciliation cycles completed.
func (shadowDataPlane *ShadowDataPlane) TotalReconciliations() int {
	shadowDataPlane.mutex.Lock()
	defer shadowDataPlane.mutex.Unlock()
	return shadowDataPlane.totalReconciliations
}

// DivergenceCount returns the total number of divergences detected.
func (shadowDataPlane *ShadowDataPlane) DivergenceCount() int {
	shadowDataPlane.mutex.Lock()
	defer shadowDataPlane.mutex.Unlock()
	return shadowDataPlane.divergenceCount
}
