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
// policy bounds).
func (intentResolverController *IntentResolverController) Watch() []string {
	return []string{
		types.ScanIntentUserServices,
		types.ScanIntentAutoscalerServices,
		types.ScanDesiredServices,
		types.ScanEffectiveServices,
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

	var changes []Change

	changes = append(changes, resolveEffectiveInstanceCountChanges(
		userIntents, autoscalerIntents, policyBounds, currentEffective, quotaCeilings,
	)...)

	changes = append(changes, resolveEffectiveResourceChanges(
		autoscalerResourceIntents, currentEffectiveResources, userResources,
	)...)

	return changes, nil
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
) []Change {
	var changes []Change

	for serviceName, userCount := range userIntents {
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
