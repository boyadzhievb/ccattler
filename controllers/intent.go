package controllers

import (
	"context"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// IntentResolverController watches all intent layers (user, autoscaler) and
// scaling policy bounds, then derives the effective instance count for each
// service. The effective count is: max(user_intent, autoscaler_intent) clamped
// to [policy_min, policy_max] when a scale policy exists.
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
// and writes the effective instance count for each service.
func (intentResolverController *IntentResolverController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	userIntents := extractIntentCounts(facts, types.ScanIntentUserServices)
	autoscalerIntents := extractIntentCounts(facts, types.ScanIntentAutoscalerServices)
	policyBounds := extractPolicyBounds(facts)
	currentEffective := extractEffectiveCounts(facts)

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

		if existingEffective, exists := currentEffective[serviceName]; exists && existingEffective == effectiveCount {
			continue
		}

		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyEffectiveServiceInstances(serviceName),
			Value: []byte(strconv.Itoa(effectiveCount)),
		})
	}

	return changes, nil
}

// extractIntentCounts scans facts under the given intent prefix and returns
// a map of service name to desired instance count for that intent layer.
func extractIntentCounts(facts []store.Fact, prefix string) map[string]int {
	counts := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, prefix)
		// relativePath = "{service}/instances"
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "instances" {
			parsedCount, _ := strconv.Atoi(string(fact.Value))
			counts[parts[0]] = parsedCount
		}
	}
	return counts
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
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		suffix := parts[1]

		current := bounds[serviceName]
		switch suffix {
		case "scale/horizontal/min":
			current.min, _ = strconv.Atoi(string(fact.Value))
			bounds[serviceName] = current
		case "scale/horizontal/max":
			current.max, _ = strconv.Atoi(string(fact.Value))
			bounds[serviceName] = current
		}
	}
	return bounds
}

// extractEffectiveCounts scans effective service facts and returns the
// current effective instance count for each service.
func extractEffectiveCounts(facts []store.Fact) map[string]int {
	counts := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanEffectiveServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEffectiveServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "instances" {
			parsedCount, _ := strconv.Atoi(string(fact.Value))
			counts[parts[0]] = parsedCount
		}
	}
	return counts
}
