// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"fmt"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/types"
)

// collectIgnoredMaxExtraWarnings returns one warning per compiled stateful or
// per-node service that sets a non-zero update { max_extra }: those services
// never surge, so the value is ignored (M241 D14). Services in name order.
func collectIgnoredMaxExtraWarnings(facts []lang.Fact) []string {
	compiledValues := make(map[string]string, len(facts))
	for _, compiledFact := range facts {
		compiledValues[compiledFact.Key] = compiledFact.Value
	}
	var warnings []string
	for _, serviceCount := range lang.ServiceInstanceCountsFromFacts(facts) {
		serviceName := serviceCount.ServiceName
		maxExtraText, hasMaxExtra := compiledValues[types.KeyDesiredServiceUpdateMaxExtra(serviceName)]
		workloadKind := types.WorkloadKindOf(compiledValues[types.KeyDesiredServiceStateful(serviceName)] == "true",
			types.PlacementMode(compiledValues[types.KeyDesiredServicePlacementMode(serviceName)]))
		if !hasMaxExtra || maxExtraText == "0" || workloadKind == types.WorkloadStateless {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("service %s: update { max_extra %s } is ignored: %s services never surge, they replace one instance at a time",
			serviceName, maxExtraText, workloadKind))
	}
	return warnings
}
