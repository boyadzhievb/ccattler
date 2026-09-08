package controllers

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// timestampedRecommendation records a scaling recommendation with its timestamp
// for stabilization window evaluation.
type timestampedRecommendation struct {
	count     int
	timestamp time.Time
}

// AutoscaleController watches observed metrics and scaling policies, computes
// per-metric instance recommendations, and writes the autoscaler intent layer.
// It never manipulates instances directly — it writes a recommended instance
// count to the autoscaler intent prefix, and the IntentResolverController
// derives the effective count.
//
// Supports stabilization windows to prevent oscillation, event-driven scaling
// for queue-depth metrics, scheduled scaling for time-based minimums, and
// vertical autoscaling for resource recommendations.
type AutoscaleController struct {
	recommendationHistory      map[string][]timestampedRecommendation
	recommendationHistoryMutex sync.Mutex
	timeNow                    func() time.Time
}

// NewAutoscaleController returns a new AutoscaleController.
func NewAutoscaleController() *AutoscaleController {
	return &AutoscaleController{
		recommendationHistory: make(map[string][]timestampedRecommendation),
		timeNow:               time.Now,
	}
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
//  3. For each event source, computes: ceil(currentValue / targetPerInstance)
//  4. Applies scheduled scaling minimum if currently within the time window
//  5. Takes the maximum recommendation across all sources
//  6. Clamps to [min, max] from the policy
//  7. Applies stabilization windows to prevent oscillation
//  8. Writes the result to the autoscaler intent layer
//
// For vertical scaling, it recommends resource adjustments within configured bounds.
func (autoscaleController *AutoscaleController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	scalePolicies := extractScalePolicies(facts)
	observedMetrics := extractObservedMetrics(facts)
	activeInstanceCounts := extractActiveInstanceCounts(facts)
	eventTargets := extractEventTargets(facts)
	scheduleRules := extractScheduleRules(facts)
	stabilizationWindows := extractStabilizationWindows(facts)
	verticalPolicies := extractVerticalPolicies(facts)
	currentCPU := extractCurrentResources(facts, "cpu")
	currentMemory := extractCurrentResources(facts, "memory")

	currentTime := autoscaleController.timeNow()
	var changes []Change

	for serviceName, policy := range scalePolicies {
		if len(policy.targets) == 0 && len(eventTargets[serviceName]) == 0 && scheduleRules[serviceName] == nil {
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

		for source, targetPerInstance := range eventTargets[serviceName] {
			eventMetricKey := serviceName + "/event." + source
			currentEventValue, hasEvent := observedMetrics[eventMetricKey]
			if !hasEvent {
				continue
			}
			eventRecommendation := int(math.Ceil(float64(currentEventValue) / float64(targetPerInstance)))
			if eventRecommendation > recommendation {
				recommendation = eventRecommendation
			}
		}

		if schedule := scheduleRules[serviceName]; schedule != nil {
			if isWithinScheduleWindow(currentTime, schedule) && schedule.minimum > recommendation {
				recommendation = schedule.minimum
			}
		}

		if recommendation < policy.min {
			recommendation = policy.min
		}
		if recommendation > policy.max {
			recommendation = policy.max
		}

		recommendation = autoscaleController.applyStabilizationWindow(
			serviceName, recommendation, activeInstanceCounts[serviceName],
			stabilizationWindows[serviceName], currentTime,
		)

		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyIntentAutoscalerServiceInstances(serviceName),
			Value: []byte(strconv.Itoa(recommendation)),
		})
	}

	for serviceName, verticalPolicy := range verticalPolicies {
		currentServiceCPU := currentCPU[serviceName]
		currentServiceMemory := currentMemory[serviceName]
		cpuMetric, hasCPU := observedMetrics[serviceName+"/cpu"]
		memoryMetric, hasMemory := observedMetrics[serviceName+"/memory"]

		if hasCPU && currentServiceCPU > 0 && verticalPolicy.cpuMax > 0 {
			recommendedCPU := int(math.Ceil(float64(currentServiceCPU) * float64(cpuMetric) / 70.0))
			if recommendedCPU < verticalPolicy.cpuMin {
				recommendedCPU = verticalPolicy.cpuMin
			}
			if recommendedCPU > verticalPolicy.cpuMax {
				recommendedCPU = verticalPolicy.cpuMax
			}
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyIntentAutoscalerServiceResourcesCPU(serviceName),
				Value: []byte(strconv.Itoa(recommendedCPU)),
			})
		}

		if hasMemory && currentServiceMemory > 0 && verticalPolicy.memoryMax > 0 {
			recommendedMemory := int(math.Ceil(float64(currentServiceMemory) * float64(memoryMetric) / 70.0))
			if recommendedMemory < verticalPolicy.memoryMin {
				recommendedMemory = verticalPolicy.memoryMin
			}
			if recommendedMemory > verticalPolicy.memoryMax {
				recommendedMemory = verticalPolicy.memoryMax
			}
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyIntentAutoscalerServiceResourcesMemory(serviceName),
				Value: []byte(strconv.Itoa(recommendedMemory)),
			})
		}
	}

	return changes, nil
}

// applyStabilizationWindow applies asymmetric stabilization to prevent scaling
// oscillation. Scale-up must be consistent for the up-window; scale-down must
// be consistent for the down-window.
func (autoscaleController *AutoscaleController) applyStabilizationWindow(
	serviceName string,
	recommendation int,
	currentCount int,
	window *stabilizationConfig,
	currentTime time.Time,
) int {
	if window == nil {
		return recommendation
	}

	autoscaleController.recommendationHistoryMutex.Lock()
	defer autoscaleController.recommendationHistoryMutex.Unlock()

	autoscaleController.recommendationHistory[serviceName] = append(
		autoscaleController.recommendationHistory[serviceName],
		timestampedRecommendation{count: recommendation, timestamp: currentTime},
	)

	cutoff := currentTime.Add(-maxDuration(window.upSeconds, window.downSeconds))
	history := autoscaleController.recommendationHistory[serviceName]
	pruneStart := 0
	for pruneStart < len(history) && history[pruneStart].timestamp.Before(cutoff) {
		pruneStart++
	}
	autoscaleController.recommendationHistory[serviceName] = history[pruneStart:]
	history = autoscaleController.recommendationHistory[serviceName]

	if currentCount == 0 {
		return recommendation
	}

	if recommendation > currentCount && window.upSeconds > 0 {
		windowStart := currentTime.Add(-time.Duration(window.upSeconds) * time.Second)
		for _, entry := range history {
			if entry.timestamp.Before(windowStart) {
				continue
			}
			if entry.count <= currentCount {
				return currentCount
			}
		}
	}

	if recommendation < currentCount && window.downSeconds > 0 {
		windowStart := currentTime.Add(-time.Duration(window.downSeconds) * time.Second)
		for _, entry := range history {
			if entry.timestamp.Before(windowStart) {
				continue
			}
			if entry.count >= currentCount {
				return currentCount
			}
		}
	}

	return recommendation
}

// maxDuration returns the larger of two integer durations.
func maxDuration(durationA, durationB int) time.Duration {
	if durationA > durationB {
		return time.Duration(durationA) * time.Second
	}
	return time.Duration(durationB) * time.Second
}

// stabilizationConfig holds the parsed stabilization window durations in seconds.
type stabilizationConfig struct {
	upSeconds   int
	downSeconds int
}

// extractStabilizationWindows parses stabilization window facts per service.
func extractStabilizationWindows(facts []store.Fact) map[string]*stabilizationConfig {
	windows := make(map[string]*stabilizationConfig)
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

		switch suffix {
		case "scale/horizontal/stabilization/up":
			if windows[serviceName] == nil {
				windows[serviceName] = &stabilizationConfig{}
			}
			windows[serviceName].upSeconds = parseDurationSeconds(string(fact.Value))
		case "scale/horizontal/stabilization/down":
			if windows[serviceName] == nil {
				windows[serviceName] = &stabilizationConfig{}
			}
			windows[serviceName].downSeconds = parseDurationSeconds(string(fact.Value))
		}
	}
	return windows
}

// parseDurationSeconds converts a duration string like "60s" or "300s" to seconds.
func parseDurationSeconds(durationString string) int {
	durationString = strings.TrimSpace(durationString)
	if strings.HasSuffix(durationString, "s") {
		seconds, _ := strconv.Atoi(strings.TrimSuffix(durationString, "s"))
		return seconds
	}
	if strings.HasSuffix(durationString, "m") {
		minutes, _ := strconv.Atoi(strings.TrimSuffix(durationString, "m"))
		return minutes * 60
	}
	seconds, _ := strconv.Atoi(durationString)
	return seconds
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

		if strings.HasPrefix(scaleField, "stabilization/") || strings.HasPrefix(scaleField, "event/") || strings.HasPrefix(scaleField, "schedule/") {
			continue
		}

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

// extractEventTargets parses event-driven scaling targets per service.
func extractEventTargets(facts []store.Fact) map[string]map[string]int {
	targets := make(map[string]map[string]int)
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

		if !strings.HasPrefix(suffix, "scale/horizontal/event/") {
			continue
		}
		source := strings.TrimPrefix(suffix, "scale/horizontal/event/")
		targetValue, _ := strconv.Atoi(string(fact.Value))
		if targets[serviceName] == nil {
			targets[serviceName] = make(map[string]int)
		}
		targets[serviceName][source] = targetValue
	}
	return targets
}

// extractedScheduleRule holds a parsed schedule rule.
type extractedScheduleRule struct {
	days    string
	start   string
	end     string
	minimum int
}

// extractScheduleRules parses scheduled scaling rules per service.
func extractScheduleRules(facts []store.Fact) map[string]*extractedScheduleRule {
	rules := make(map[string]*extractedScheduleRule)
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

		if !strings.HasPrefix(suffix, "scale/horizontal/schedule/") {
			continue
		}
		field := strings.TrimPrefix(suffix, "scale/horizontal/schedule/")

		if rules[serviceName] == nil {
			rules[serviceName] = &extractedScheduleRule{}
		}
		switch field {
		case "days":
			rules[serviceName].days = string(fact.Value)
		case "start":
			rules[serviceName].start = string(fact.Value)
		case "end":
			rules[serviceName].end = string(fact.Value)
		case "minimum":
			rules[serviceName].minimum, _ = strconv.Atoi(string(fact.Value))
		}
	}
	return rules
}

// isWithinScheduleWindow checks if the current time falls within the schedule.
func isWithinScheduleWindow(currentTime time.Time, schedule *extractedScheduleRule) bool {
	weekday := currentTime.Weekday()

	switch schedule.days {
	case "weekdays":
		if weekday == time.Saturday || weekday == time.Sunday {
			return false
		}
	case "weekends":
		if weekday != time.Saturday && weekday != time.Sunday {
			return false
		}
	case "everyday":
		// Always active
	default:
		return false
	}

	currentHHMM := currentTime.Format("15:04")
	if schedule.start != "" && currentHHMM < schedule.start {
		return false
	}
	if schedule.end != "" && currentHHMM >= schedule.end {
		return false
	}
	return true
}

// extractedVerticalPolicy holds parsed vertical autoscaling bounds.
type extractedVerticalPolicy struct {
	cpuMin    int
	cpuMax    int
	memoryMin int
	memoryMax int
}

// extractVerticalPolicies parses vertical autoscaling policies per service.
func extractVerticalPolicies(facts []store.Fact) map[string]*extractedVerticalPolicy {
	policies := make(map[string]*extractedVerticalPolicy)
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

		if !strings.HasPrefix(suffix, "scale/vertical/") {
			continue
		}
		field := strings.TrimPrefix(suffix, "scale/vertical/")

		if policies[serviceName] == nil {
			policies[serviceName] = &extractedVerticalPolicy{}
		}
		parsedValue, _ := strconv.Atoi(string(fact.Value))
		switch field {
		case "cpu/min":
			policies[serviceName].cpuMin = parsedValue
		case "cpu/max":
			policies[serviceName].cpuMax = parsedValue
		case "memory/min":
			policies[serviceName].memoryMin = parsedValue
		case "memory/max":
			policies[serviceName].memoryMax = parsedValue
		}
	}
	return policies
}

// extractCurrentResources extracts current resource values per service.
func extractCurrentResources(facts []store.Fact, resourceType string) map[string]int {
	resources := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == "resources/"+resourceType {
			parsedValue, _ := strconv.Atoi(string(fact.Value))
			resources[parts[0]] = parsedValue
		}
	}
	return resources
}

// extractObservedMetrics scans observed metric facts and returns a map of
// "service/metric" -> current integer value.
func extractObservedMetrics(facts []store.Fact) map[string]int {
	metrics := make(map[string]int)

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedMetrics) {
			continue
		}
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
