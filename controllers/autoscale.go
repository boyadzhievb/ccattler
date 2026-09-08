package controllers

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// AutoscaleController watches observed metrics and scaling policies, computes
// per-metric instance recommendations, and writes the autoscaler intent layer.
// It never manipulates instances directly — it writes a recommended instance
// count to the autoscaler intent prefix, and the IntentResolverController
// derives the effective count.
type AutoscaleController struct{}

// NewAutoscaleController returns a new AutoscaleController.
func NewAutoscaleController() *AutoscaleController {
	return &AutoscaleController{}
}

// Name returns "autoscale", identifying this controller in logs and runner bookkeeping.
func (autoscaleController *AutoscaleController) Name() string { return "autoscale" }

// Watch returns the fact prefixes that drive autoscaling: desired service
// definitions (which carry scale policies), observed metrics (current load),
// and observed instances (current count for ratio computation).
func (autoscaleController *AutoscaleController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedMetrics,
		types.ScanObservedInstances,
	}
}

// Reconcile evaluates each service's scaling policy against observed metrics
// and emits changes to write the autoscaler's recommended instance count.
//
// For each service with a horizontal scale policy, it:
//  1. Counts currently active (non-stopped) instances
//  2. For each target metric, computes: ceil(activeCount * currentValue / targetValue)
//  3. Takes the maximum recommendation across all metrics
//  4. Clamps to [min, max] from the policy
//  5. Writes the result to the autoscaler intent layer
func (autoscaleController *AutoscaleController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	scalePolicies := extractScalePolicies(facts)
	observedMetrics := extractObservedMetrics(facts)
	activeInstanceCounts := extractActiveInstanceCounts(facts)

	var changes []Change
	for serviceName, policy := range scalePolicies {
		if len(policy.targets) == 0 {
			continue
		}

		currentInstanceCount := activeInstanceCounts[serviceName]
		if currentInstanceCount == 0 {
			currentInstanceCount = 1
		}

		recommendation := policy.min
		for metricName, targetValue := range policy.targets {
			currentMetricValue, hasMetric := observedMetrics[serviceName+"/"+metricName]
			if !hasMetric {
				continue
			}
			metricRecommendation := int(math.Ceil(
				float64(currentInstanceCount) * float64(currentMetricValue) / float64(targetValue),
			))
			if metricRecommendation > recommendation {
				recommendation = metricRecommendation
			}
		}

		if recommendation < policy.min {
			recommendation = policy.min
		}
		if recommendation > policy.max {
			recommendation = policy.max
		}

		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyIntentAutoscalerServiceInstances(serviceName),
			Value: []byte(strconv.Itoa(recommendation)),
		})
	}

	return changes, nil
}

// extractedScalePolicy holds the parsed fields of a horizontal scale policy
// extracted from flat facts during reconciliation.
type extractedScalePolicy struct {
	min     int
	max     int
	targets map[string]int
}

// extractScalePolicies scans desired service facts for horizontal scale
// policy definitions and returns a map of service name to parsed policy.
func extractScalePolicies(facts []store.Fact) map[string]*extractedScalePolicy {
	policies := make(map[string]*extractedScalePolicy)

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)

		// Match: {service}/scale/horizontal/min
		// Match: {service}/scale/horizontal/max
		// Match: {service}/scale/horizontal/target/{metric}
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		suffix := parts[1]

		if !strings.HasPrefix(suffix, "scale/horizontal/") {
			continue
		}
		scaleField := strings.TrimPrefix(suffix, "scale/horizontal/")

		policy, exists := policies[serviceName]
		if !exists {
			policy = &extractedScalePolicy{targets: make(map[string]int)}
			policies[serviceName] = policy
		}

		switch {
		case scaleField == "min":
			policy.min, _ = strconv.Atoi(string(fact.Value))
		case scaleField == "max":
			policy.max, _ = strconv.Atoi(string(fact.Value))
		case strings.HasPrefix(scaleField, "target/"):
			metricName := strings.TrimPrefix(scaleField, "target/")
			targetValue, _ := strconv.Atoi(string(fact.Value))
			policy.targets[metricName] = targetValue
		}
	}

	return policies
}

// extractObservedMetrics scans observed metric facts and returns a map of
// "service/metric" -> current integer value.
func extractObservedMetrics(facts []store.Fact) map[string]int {
	metrics := make(map[string]int)

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedMetrics) {
			continue
		}
		// Key format: /ccattler/observed/metric/service/{service}/{metric}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedMetrics)
		if !strings.HasPrefix(relativePath, "service/") {
			continue
		}
		serviceAndMetric := strings.TrimPrefix(relativePath, "service/")
		metricValue, _ := strconv.Atoi(string(fact.Value))
		metrics[serviceAndMetric] = metricValue
	}

	return metrics
}

// extractActiveInstanceCounts scans observed instance facts and returns a map
// of service name to the number of non-stopped instances.
func extractActiveInstanceCounts(facts []store.Fact) map[string]int {
	serviceByID := make(map[string]string)
	stateByID := make(map[string]types.InstanceState)

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		switch parts[1] {
		case "service":
			serviceByID[instanceID] = string(fact.Value)
		case "state":
			stateByID[instanceID] = types.InstanceState(fact.Value)
		}
	}

	counts := make(map[string]int)
	for instanceID, serviceName := range serviceByID {
		if stateByID[instanceID] != types.InstanceStopped {
			counts[serviceName]++
		}
	}
	return counts
}
