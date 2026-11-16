// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strconv"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// IntentResolverController watches all intent layers (user, autoscaler) and
// scaling policy bounds, then derives the effective instance count for each
// service. The effective count is: max(user_intent, autoscaler_intent) clamped
// to [policy_min, policy_max] and further clamped by any instance quota.
//
// For vertical scaling, it merges user resource declarations with autoscaler
// resource recommendations to produce effective resource values.
//
// This controller sits between intent writers (compiler, autoscaler) and the
// InstanceController, which only reads effective state.
type IntentResolverController struct{}

// NewIntentResolverController returns a new IntentResolverController.
func NewIntentResolverController() *IntentResolverController {
	return &IntentResolverController{}
}

// Name returns "intent-resolver", identifying this controller in logs and
// runner bookkeeping.
func (intentResolverController *IntentResolverController) Name() string { return "intent-resolver" }

// Watch returns the fact prefixes that drive intent resolution: user intents,
// autoscaler intents, and desired service definitions (which carry scale
// policy bounds and the declaration markers). The runner scans them in this
// order, and desired/service/ must come last: apply writes a service's marker
// before its intent and effective count, so every count already seen has its
// marker visible, and a new service is never taken for an orphan (M217).
func (intentResolverController *IntentResolverController) Watch() []string {
	return []string{
		types.ScanIntentUserServices,
		types.ScanIntentAutoscalerServices,
		types.ScanEffectiveServices,
		types.ScanObservedNodes,
		types.ScanDesiredServices,
	}
}

// Reconcile reads all intent layers and scale policy bounds, then computes
// and writes the effective instance count for each service. When a quota
// is configured, effective count is additionally clamped to the quota ceiling.
// For vertical scaling, merges resource intents into effective resource values.
func (intentResolverController *IntentResolverController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	userIntents := extractIntentCounts(facts, types.ScanIntentUserServices)
	autoscalerIntents := extractIntentCounts(facts, types.ScanIntentAutoscalerServices)
	policyBounds := extractPolicyBounds(facts)
	currentEffective := extractEffectiveCounts(facts)
	quotaCeilings := extractQuotaCeilings(facts)
	autoscalerResourceIntents := extractAutoscalerResourceIntents(facts)
	currentEffectiveResources := extractEffectiveResources(facts)
	userResources := extractUserResources(facts)
	placementModes := extractDesiredPlacementModes(facts)
	currentEffectiveModes := extractEffectivePlacementModes(facts)
	aliveNodeCount := countAliveNodesFromFacts(facts)

	var changes []Change

	changes = append(changes, resolvePerNodePlacementChanges(
		placementModes, currentEffective, currentEffectiveModes,
		policyBounds, aliveNodeCount,
	)...)

	declaredServiceKeys := collectDeclaredServiceKeys(facts)
	changes = append(changes, resolveEffectiveInstanceCountChanges(
		keepDeclaredServices(userIntents, declaredServiceKeys), autoscalerIntents, policyBounds, currentEffective, quotaCeilings,
		placementModes,
	)...)
	changes = append(changes, resolveUndeclaredServiceChanges(
		declaredServiceKeys, userIntents, autoscalerIntents, currentEffective,
	)...)

	changes = append(changes, resolveEffectiveResourceChanges(
		autoscalerResourceIntents, currentEffectiveResources, userResources,
	)...)

	return changes, nil
}

// collectDeclaredServiceKeys returns the set of desired/service/ keys, so a
// name is declared when its exact marker key types.KeyDesiredService is in it
// (hierarchical names such as payments/checkout included).
func collectDeclaredServiceKeys(facts []store.Fact) map[string]bool {
	declaredServiceKeys := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		declaredServiceKeys[fact.Key] = true
	}
	return declaredServiceKeys
}

// keepDeclaredServices returns the counts of declared services only (M217):
// an intent for a name without a declaration never becomes instances.
func keepDeclaredServices(countsByService map[string]int, declaredServiceKeys map[string]bool) map[string]int {
	declaredCounts := make(map[string]int, len(countsByService))
	for serviceName, instanceCount := range countsByService {
		if declaredServiceKeys[types.KeyDesiredService(serviceName)] {
			declaredCounts[serviceName] = instanceCount
		}
	}
	return declaredCounts
}

// resolveUndeclaredServiceChanges sets the effective count of every name that
// has an intent or an effective count but no declaration to 0, so the instance
// controller stops its instances (spec service-scaling, design D6). Deleting
// the count instead would hide the name from the instance controller and leave
// the instances running. Names are sorted for a deterministic change order.
func resolveUndeclaredServiceChanges(
	declaredServiceKeys map[string]bool,
	userIntents map[string]int,
	autoscalerIntents map[string]int,
	currentEffective map[string]int,
) []Change {
	undeclaredNames := make(map[string]bool)
	for _, countsByService := range []map[string]int{userIntents, autoscalerIntents, currentEffective} {
		for serviceName := range countsByService {
			if !declaredServiceKeys[types.KeyDesiredService(serviceName)] {
				undeclaredNames[serviceName] = true
			}
		}
	}
	var changes []Change
	for _, serviceName := range sortedMapKeys(undeclaredNames) {
		if existingEffective, exists := currentEffective[serviceName]; exists && existingEffective == 0 {
			continue
		}
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyEffectiveServiceInstances(serviceName),
			Value: []byte("0"),
		})
	}
	return changes
}

// resolveEffectiveInstanceCountChanges computes the effective instance count
// for each service by combining user intent, autoscaler intent, policy bounds,
// and quota ceilings. Returns changes only for services whose effective count
// differs from the current effective count.
func resolveEffectiveInstanceCountChanges(
	userIntents map[string]int,
	autoscalerIntents map[string]int,
	policyBounds map[string]scalePolicyBounds,
	currentEffective map[string]int,
	quotaCeilings map[string]int,
	placementModes map[string]string,
) []Change {
	var changes []Change

	for serviceName, userCount := range userIntents {
		if placementModes[serviceName] == "per-node" {
			continue
		}
		effectiveCount := userCount

		if autoscalerCount, hasAutoscaler := autoscalerIntents[serviceName]; hasAutoscaler {
			if autoscalerCount > effectiveCount {
				effectiveCount = autoscalerCount
			}
		}

		if bounds, hasBounds := policyBounds[serviceName]; hasBounds {
			if effectiveCount < bounds.min {
				effectiveCount = bounds.min
			}
			if effectiveCount > bounds.max {
				effectiveCount = bounds.max
			}
		}

		if quota, hasQuota := quotaCeilings[serviceName]; hasQuota && effectiveCount > quota {
			effectiveCount = quota
		}

		if existingEffective, exists := currentEffective[serviceName]; exists && existingEffective == effectiveCount {
			continue
		}

		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyEffectiveServiceInstances(serviceName),
			Value: []byte(strconv.Itoa(effectiveCount)),
		})
	}

	return changes
}

// resolveEffectiveResourceChanges computes effective CPU and memory values for
// each service by merging autoscaler resource recommendations with user-declared
// resources. Autoscaler recommendations take precedence when present. Returns
// changes only when the effective value differs from the current value.
func resolveEffectiveResourceChanges(
	autoscalerResourceIntents map[string]autoscalerResourceIntent,
	currentEffectiveResources map[string]string,
	userResources map[string]string,
) []Change {
	var changes []Change

	for serviceName, autoscalerResources := range autoscalerResourceIntents {
		if autoscalerResources.cpu != "" {
			effectiveCPU := autoscalerResources.cpu
			currentEffCPU := currentEffectiveResources[serviceName+"/cpu"]
			if effectiveCPU != currentEffCPU {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyEffectiveServiceResourcesCPU(serviceName),
					Value: []byte(effectiveCPU),
				})
			}
		} else if userCPU := userResources[serviceName+"/cpu"]; userCPU != "" {
			currentEffCPU := currentEffectiveResources[serviceName+"/cpu"]
			if userCPU != currentEffCPU {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyEffectiveServiceResourcesCPU(serviceName),
					Value: []byte(userCPU),
				})
			}
		}

		if autoscalerResources.memory != "" {
			effectiveMemory := autoscalerResources.memory
			currentEffMemory := currentEffectiveResources[serviceName+"/memory"]
			if effectiveMemory != currentEffMemory {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyEffectiveServiceResourcesMemory(serviceName),
					Value: []byte(effectiveMemory),
				})
			}
		} else if userMemory := userResources[serviceName+"/memory"]; userMemory != "" {
			currentEffMemory := currentEffectiveResources[serviceName+"/memory"]
			if userMemory != currentEffMemory {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyEffectiveServiceResourcesMemory(serviceName),
					Value: []byte(userMemory),
				})
			}
		}
	}

	return changes
}

// extractIntentCounts scans facts under the given intent prefix and returns
// a map of service name to desired instance count for that intent layer.
func extractIntentCounts(facts []store.Fact, prefix string) map[string]int {
	return collectIntValuesBySuffix(facts, prefix, "instances")
}

// scalePolicyBounds holds the min/max instance counts from a service's
// horizontal scale policy.
type scalePolicyBounds struct {
	min int
	max int
}

// extractPolicyBounds scans desired service facts for horizontal scale
// policy min/max values.
func extractPolicyBounds(facts []store.Fact) map[string]scalePolicyBounds {
	bounds := make(map[string]scalePolicyBounds)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}

		current := bounds[serviceName]
		switch suffix {
		case "scale/horizontal/min":
			parsedValue, parseError := strconv.Atoi(string(fact.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt scale horizontal/min", "service", serviceName, "value", string(fact.Value))
				continue
			}
			current.min = parsedValue
			bounds[serviceName] = current
		case "scale/horizontal/max":
			parsedValue, parseError := strconv.Atoi(string(fact.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt scale horizontal/max", "service", serviceName, "value", string(fact.Value))
				continue
			}
			current.max = parsedValue
			bounds[serviceName] = current
		}
	}
	return bounds
}

// extractEffectiveCounts scans effective service facts and returns the
// current effective instance count for each service.
func extractEffectiveCounts(facts []store.Fact) map[string]int {
	return collectIntValuesBySuffix(facts, types.ScanEffectiveServices, "instances")
}

// extractQuotaCeilings scans desired service facts for instance quota ceilings.
func extractQuotaCeilings(facts []store.Fact) map[string]int {
	return collectIntValuesBySuffix(facts, types.ScanDesiredServices, "quota/instances")
}

// autoscalerResourceIntent holds the autoscaler's recommended CPU and memory.
type autoscalerResourceIntent struct {
	cpu    string
	memory string
}

// extractAutoscalerResourceIntents scans autoscaler intent facts for resource recommendations.
func extractAutoscalerResourceIntents(facts []store.Fact) map[string]autoscalerResourceIntent {
	intents := make(map[string]autoscalerResourceIntent)
	prefix := types.ScanIntentAutoscalerServices
	for _, fact := range store.FactsWithPrefix(facts, prefix) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, prefix)
		if !hasSuffix {
			continue
		}
		intent := intents[serviceName]
		switch suffix {
		case "resources/cpu":
			intent.cpu = string(fact.Value)
		case "resources/memory":
			intent.memory = string(fact.Value)
		}
		intents[serviceName] = intent
	}
	return intents
}

// extractEffectiveResources scans effective service facts for current resource values.
func extractEffectiveResources(facts []store.Fact) map[string]string {
	resources := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEffectiveServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanEffectiveServices)
		if !hasSuffix {
			continue
		}
		switch suffix {
		case "resources/cpu":
			resources[serviceName+"/cpu"] = string(fact.Value)
		case "resources/memory":
			resources[serviceName+"/memory"] = string(fact.Value)
		}
	}
	return resources
}

// extractDesiredPlacementModes scans desired service facts for placement mode
// declarations (e.g. "per-node").
func extractDesiredPlacementModes(facts []store.Fact) map[string]string {
	modes := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix || suffix != "placement/mode" {
			continue
		}
		modes[serviceName] = string(fact.Value)
	}
	return modes
}

// extractEffectivePlacementModes scans effective service facts for the current
// placement mode.
func extractEffectivePlacementModes(facts []store.Fact) map[string]string {
	modes := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEffectiveServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanEffectiveServices)
		if !hasSuffix || suffix != "placement_mode" {
			continue
		}
		modes[serviceName] = string(fact.Value)
	}
	return modes
}

// countAliveNodesFromFacts counts the number of nodes with state "alive" from
// observed node facts.
func countAliveNodesFromFacts(facts []store.Fact) int {
	aliveCount := 0
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		_, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanObservedNodes)
		if hasSuffix && suffix == "state" && string(fact.Value) == string(types.NodeAlive) {
			aliveCount++
		}
	}
	return aliveCount
}

// resolvePerNodePlacementChanges computes effective instance counts for
// per-node services. The effective count equals the number of alive nodes,
// clamped to policy_max if set. Also propagates the placement mode to
// effective state.
func resolvePerNodePlacementChanges(
	placementModes map[string]string,
	currentEffective map[string]int,
	currentEffectiveModes map[string]string,
	policyBounds map[string]scalePolicyBounds,
	aliveNodeCount int,
) []Change {
	var changes []Change

	for serviceName, mode := range placementModes {
		if mode != "per-node" {
			continue
		}

		effectiveCount := aliveNodeCount
		if bounds, hasBounds := policyBounds[serviceName]; hasBounds && bounds.max > 0 && effectiveCount > bounds.max {
			effectiveCount = bounds.max
		}

		if existingCount, exists := currentEffective[serviceName]; !exists || existingCount != effectiveCount {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyEffectiveServiceInstances(serviceName),
				Value: []byte(strconv.Itoa(effectiveCount)),
			})
		}

		if currentEffectiveModes[serviceName] != "per-node" {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyEffectiveServicePlacementMode(serviceName),
				Value: []byte("per-node"),
			})
		}
	}

	return changes
}

// extractUserResources scans desired service facts for user-declared resources.
func extractUserResources(facts []store.Fact) map[string]string {
	resources := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}
		switch suffix {
		case "resources/cpu":
			resources[serviceName+"/cpu"] = string(fact.Value)
		case "resources/memory":
			resources[serviceName+"/memory"] = string(fact.Value)
		}
	}
	return resources
}
