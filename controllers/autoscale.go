// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// verticalMetricWindowDuration is the sliding window over which P95 usage
	// is computed for vertical autoscaling recommendations.
	verticalMetricWindowDuration = 5 * time.Minute

	// verticalScaleUpStabilizationDuration is the stabilization window for
	// vertical scale-up — recommendations must be consistently higher for
	// this duration before the change is applied.
	verticalScaleUpStabilizationDuration = 60 * time.Second

	// verticalScaleDownStabilizationDuration is the stabilization window for
	// vertical scale-down — recommendations must be consistently lower for
	// this duration before the change is applied.
	verticalScaleDownStabilizationDuration = 5 * time.Minute

	// verticalTargetUtilizationPercent is the target utilization percentage
	// for vertical scaling. The recommendation is: P95_usage / target * current.
	verticalTargetUtilizationPercent = 70
)

// timestampedRecommendation records a scaling recommendation with its timestamp
// for stabilization window evaluation.
type timestampedRecommendation struct {
	count     int
	timestamp time.Time
}

// timestampedMetricSample records a metric observation with its timestamp
// for P95 sliding-window computation in vertical autoscaling.
type timestampedMetricSample struct {
	value     int       // observed metric value (e.g. CPU usage percentage)
	timestamp time.Time // when this sample was observed
}

// AutoscaleController watches observed metrics and scaling policies, computes
// per-metric instance recommendations, and writes the autoscaler intent layer.
// It never manipulates instances directly — it writes a recommended instance
// count to the autoscaler intent prefix, and the IntentResolverController
// derives the effective count.
//
// Supports stabilization windows to prevent oscillation, event-driven scaling
// for queue-depth metrics, scheduled scaling for time-based minimums, and
// vertical autoscaling with P95 sliding-window recommendations.
type AutoscaleController struct {
	// recommendationHistory tracks horizontal scaling recommendations per service.
	recommendationHistory      map[string][]timestampedRecommendation
	recommendationHistoryMutex sync.Mutex
	// verticalMetricHistory tracks metric samples per "service/metric" key for P95.
	verticalMetricHistory map[string][]timestampedMetricSample
	// verticalRecommendationHistory tracks vertical resource recommendations per
	// "service/resource" key for stabilization window evaluation.
	verticalRecommendationHistory map[string][]timestampedRecommendation
	timeNow                       func() time.Time
}

// NewAutoscaleController returns a new AutoscaleController.
func NewAutoscaleController() *AutoscaleController {
	return &AutoscaleController{
		recommendationHistory:         make(map[string][]timestampedRecommendation),
		verticalMetricHistory:         make(map[string][]timestampedMetricSample),
		verticalRecommendationHistory: make(map[string][]timestampedRecommendation),
		timeNow:                       time.Now,
	}
}

// SetTimeNow overrides the clock function used for stabilization window
// evaluation. Intended for deterministic testing with simulated time.
func (autoscaleController *AutoscaleController) SetTimeNow(timeFn func() time.Time) {
	autoscaleController.timeNow = timeFn
}

// Name returns "autoscale", identifying this controller in logs and runner bookkeeping.
func (autoscaleController *AutoscaleController) Name() string { return "autoscale" }

// Watch returns the fact prefixes that drive autoscaling: desired service
// definitions (which carry scale policies), observed metrics (current load),
// observed instances (current count for ratio computation), and effective
// services (the current request the vertical autoscaler resizes, M243 D6).
func (autoscaleController *AutoscaleController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedMetrics,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanIntentAutoscalerServices,
		types.ScanDerivedServices,
		types.ScanEffectiveServices,
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
	verticalBounds := extractVerticalBoundsByResource(facts)
	currentCPU, currentMemory := extractCurrentResources(facts)
	activationStates := extractActivationStates(facts)

	currentTime := autoscaleController.timeNow()

	horizontalChanges := autoscaleController.buildHorizontalScalingChanges(
		scalePolicies, observedMetrics, activeInstanceCounts,
		eventTargets, scheduleRules, stabilizationWindows,
		activationStates, currentTime,
	)
	verticalChanges := autoscaleController.buildVerticalScalingChanges(
		verticalBounds, currentCPU, currentMemory, observedMetrics,
		extractAutoscalerResourceIntents(facts), currentTime,
	)

	return append(horizontalChanges, verticalChanges...), nil
}

// buildHorizontalScalingChanges iterates over all services with horizontal scale
// policies and computes the recommended instance count for each. It evaluates
// metric targets, event-driven targets, and schedule-based minimums, clamps the
// result to the policy bounds, applies stabilization windows, and returns the
// changes that write the recommendation to the autoscaler intent layer.
func (autoscaleController *AutoscaleController) buildHorizontalScalingChanges(
	scalePolicies map[string]*extractedScalePolicy,
	observedMetrics map[string]int,
	activeInstanceCounts map[string]int,
	eventTargets map[string]map[string]int,
	scheduleRules map[string]*extractedScheduleRule,
	stabilizationWindows map[string]*stabilizationConfig,
	activationStates map[string]string,
	currentTime time.Time,
) []Change {
	var changes []Change

	for serviceName, policy := range scalePolicies {
		hasWarmZero := policy.min == 0
		isActivating := activationStates[serviceName] == "activating"
		if len(policy.targets) == 0 && len(eventTargets[serviceName]) == 0 && scheduleRules[serviceName] == nil && !isActivating && !hasWarmZero {
			continue
		}

		currentInstanceCount := activeInstanceCounts[serviceName]
		if currentInstanceCount == 0 {
			currentInstanceCount = 1
		}

		recommendation := computeRawHorizontalRecommendation(
			serviceName, policy, currentInstanceCount,
			observedMetrics, eventTargets[serviceName], scheduleRules[serviceName],
			currentTime,
		)

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

		if isActivating && recommendation < 1 {
			recommendation = 1
		}

		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyIntentAutoscalerServiceInstances(serviceName),
			Value: []byte(strconv.Itoa(recommendation)),
		})
	}

	return changes
}

// computeRawHorizontalRecommendation calculates the unclamped instance count
// recommendation for a single service by evaluating all metric targets,
// event-driven targets, and the schedule-based minimum, then returning the
// maximum recommendation across all sources.
func computeRawHorizontalRecommendation(
	serviceName string,
	policy *extractedScalePolicy,
	currentInstanceCount int,
	observedMetrics map[string]int,
	serviceEventTargets map[string]int,
	schedule *extractedScheduleRule,
	currentTime time.Time,
) int {
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

	for source, targetPerInstance := range serviceEventTargets {
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

	if schedule != nil {
		if isWithinScheduleWindow(currentTime, schedule) && schedule.minimum > recommendation {
			recommendation = schedule.minimum
		}
	}

	return recommendation
}

// verticalResourceKinds are the resources the vertical autoscaler sizes, in order.
var verticalResourceKinds = []string{"cpu", "memory"}

// verticalResourceDimension is one resource (cpu or memory) of one service the
// vertical autoscaler sizes, with its bounds, current value and unit.
type verticalResourceDimension struct {
	resourceType   string                 // resourceType is "cpu" or "memory": the metric name and history key
	intentKey      string                 // intentKey is intent/autoscaler/service/{svc}/resources/{type}
	currentValue   int                    // currentValue is the current request (millicores or bytes), 0 when unknown
	bounds         verticalResourceBounds // bounds are the parsed scale/vertical/{type} bounds
	formatQuantity func(int64) string     // formatQuantity writes the unit form (FormatCPUMillicores, FormatMemoryMiB)
}

// buildVerticalScalingChanges recommends, per declared vertical resource
// (sorted "{svc}/{kind}" keys), a CPU or memory level in unit form, using P95
// of the metric window and asymmetric stabilization; then deletes the
// recommendations of resources that no longer declare vertical scaling (B3).
func (autoscaleController *AutoscaleController) buildVerticalScalingChanges(
	boundsByResource map[string]verticalResourceBounds,
	currentCPU map[string]int,
	currentMemory map[string]int,
	observedMetrics map[string]int,
	autoscalerResourceIntents map[string]autoscalerResourceIntent,
	currentTime time.Time,
) []Change {
	autoscaleController.recommendationHistoryMutex.Lock()
	defer autoscaleController.recommendationHistoryMutex.Unlock()

	autoscaleController.recordVerticalMetricSamples(observedMetrics, currentTime)

	var changes []Change
	for _, resourceKey := range sortedMapKeys(boundsByResource) {
		lastSlash := strings.LastIndex(resourceKey, "/")
		serviceName, resourceKind := resourceKey[:lastSlash], resourceKey[lastSlash+1:]
		dimension := verticalResourceDimension{resourceType: resourceKind, bounds: boundsByResource[resourceKey],
			intentKey: types.KeyIntentAutoscalerServiceResourcesCPU(serviceName), currentValue: currentCPU[serviceName],
			formatQuantity: types.FormatCPUMillicores}
		if resourceKind == "memory" {
			dimension.intentKey, dimension.currentValue = types.KeyIntentAutoscalerServiceResourcesMemory(serviceName), currentMemory[serviceName]
			dimension.formatQuantity = types.FormatMemoryMiB
		}
		if dimensionChange := autoscaleController.computeVerticalResourceChange(serviceName, dimension, currentTime); dimensionChange != nil {
			changes = append(changes, *dimensionChange)
		}
	}
	return append(changes, buildRemovedVerticalPolicyDeletions(boundsByResource, autoscalerResourceIntents)...)
}

// buildRemovedVerticalPolicyDeletions deletes, per resource, the
// recommendation of a service that no longer has a scale/vertical/{kind}/
// fact, so the intent resolver drops that effective value and the desired
// request applies (D7, B3). Removing only cpu {} keeps the memory one.
func buildRemovedVerticalPolicyDeletions(
	boundsByResource map[string]verticalResourceBounds,
	autoscalerResourceIntents map[string]autoscalerResourceIntent,
) []Change {
	var deletions []Change
	for _, serviceName := range sortedMapKeys(autoscalerResourceIntents) {
		storedIntents := autoscalerResourceIntents[serviceName]
		for _, resourceKind := range verticalResourceKinds {
			if _, declared := boundsByResource[serviceName+"/"+resourceKind]; declared {
				continue
			}
			if resourceKind == "cpu" && storedIntents.cpu != "" {
				deletions = append(deletions, DeleteChange(types.KeyIntentAutoscalerServiceResourcesCPU(serviceName)))
			}
			if resourceKind == "memory" && storedIntents.memory != "" {
				deletions = append(deletions, DeleteChange(types.KeyIntentAutoscalerServiceResourcesMemory(serviceName)))
			}
		}
	}
	return deletions
}

// recordVerticalMetricSamples appends current metric observations to the
// sliding window history and prunes samples older than the window duration.
func (autoscaleController *AutoscaleController) recordVerticalMetricSamples(
	observedMetrics map[string]int,
	currentTime time.Time,
) {
	cutoff := currentTime.Add(-verticalMetricWindowDuration)
	for metricKey, metricValue := range observedMetrics {
		autoscaleController.verticalMetricHistory[metricKey] = append(
			autoscaleController.verticalMetricHistory[metricKey],
			timestampedMetricSample{value: metricValue, timestamp: currentTime},
		)
		autoscaleController.verticalMetricHistory[metricKey] = pruneMetricSamples(
			autoscaleController.verticalMetricHistory[metricKey], cutoff,
		)
	}
}

// computeVerticalResourceChange computes a stabilized vertical scaling
// recommendation for a single resource dimension (cpu or memory), written in
// unit form ("750m", "768Mi"). Returns nil if no change is needed or if
// insufficient data is available.
func (autoscaleController *AutoscaleController) computeVerticalResourceChange(
	serviceName string,
	dimension verticalResourceDimension,
	currentTime time.Time,
) *Change {
	currentResourceValue := dimension.currentValue
	if currentResourceValue <= 0 || dimension.bounds.unreadable || dimension.bounds.maximum <= 0 {
		return nil // unknown size, an unreadable bound (S4) or no max: no recommendation
	}

	metricKey := serviceName + "/" + dimension.resourceType
	p95Usage := computeP95FromSamples(autoscaleController.verticalMetricHistory[metricKey])
	if p95Usage <= 0 {
		return nil
	}

	rawRecommendation := int(math.Ceil(
		float64(currentResourceValue) * float64(p95Usage) / float64(verticalTargetUtilizationPercent),
	))
	rawRecommendation = clampToRange(rawRecommendation, int(dimension.bounds.minimum), int(dimension.bounds.maximum))

	stabilizedRecommendation := autoscaleController.applyVerticalStabilization(
		metricKey, rawRecommendation, currentResourceValue, currentTime,
	)

	if stabilizedRecommendation == currentResourceValue {
		return nil
	}

	return &Change{
		Type:  store.OpPut,
		Key:   dimension.intentKey,
		Value: []byte(dimension.formatQuantity(int64(stabilizedRecommendation))),
	}
}

// applyVerticalStabilization applies asymmetric stabilization to a vertical
// scaling recommendation. Scale-up must be sustained for 60s; scale-down must
// be sustained for 5m. Returns the stabilized value (may equal currentValue
// if the window hasn't been consistently above/below).
func (autoscaleController *AutoscaleController) applyVerticalStabilization(
	resourceKey string,
	recommendation int,
	currentValue int,
	currentTime time.Time,
) int {
	autoscaleController.verticalRecommendationHistory[resourceKey] = append(
		autoscaleController.verticalRecommendationHistory[resourceKey],
		timestampedRecommendation{count: recommendation, timestamp: currentTime},
	)

	longerWindow := verticalScaleDownStabilizationDuration
	cutoff := currentTime.Add(-longerWindow)
	autoscaleController.verticalRecommendationHistory[resourceKey] = pruneRecommendations(
		autoscaleController.verticalRecommendationHistory[resourceKey], cutoff,
	)

	history := autoscaleController.verticalRecommendationHistory[resourceKey]
	if currentValue == 0 {
		return recommendation
	}

	if recommendation > currentValue {
		windowStart := currentTime.Add(-verticalScaleUpStabilizationDuration)
		for _, entry := range history {
			if entry.timestamp.Before(windowStart) {
				continue
			}
			if entry.count <= currentValue {
				return currentValue
			}
		}
	}

	if recommendation < currentValue {
		windowStart := currentTime.Add(-verticalScaleDownStabilizationDuration)
		for _, entry := range history {
			if entry.timestamp.Before(windowStart) {
				continue
			}
			if entry.count >= currentValue {
				return currentValue
			}
		}
	}

	return recommendation
}

// computeP95FromSamples returns the 95th percentile value from a slice of
// metric samples. Returns 0 if the slice is empty.
func computeP95FromSamples(samples []timestampedMetricSample) int {
	if len(samples) == 0 {
		return 0
	}
	values := make([]int, len(samples))
	for index, sample := range samples {
		values[index] = sample.value
	}
	sort.Ints(values)
	p95Index := int(math.Ceil(float64(len(values))*0.95)) - 1
	if p95Index < 0 {
		p95Index = 0
	}
	if p95Index >= len(values) {
		p95Index = len(values) - 1
	}
	return values[p95Index]
}

// pruneMetricSamples removes samples older than the cutoff time.
func pruneMetricSamples(samples []timestampedMetricSample, cutoff time.Time) []timestampedMetricSample {
	pruneStart := 0
	for pruneStart < len(samples) && samples[pruneStart].timestamp.Before(cutoff) {
		pruneStart++
	}
	return samples[pruneStart:]
}

// pruneRecommendations removes recommendations older than the cutoff time.
func pruneRecommendations(history []timestampedRecommendation, cutoff time.Time) []timestampedRecommendation {
	pruneStart := 0
	for pruneStart < len(history) && history[pruneStart].timestamp.Before(cutoff) {
		pruneStart++
	}
	return history[pruneStart:]
}

// clampToRange constrains a value to lie within [minimum, maximum].
func clampToRange(value int, minimum int, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
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
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}

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
// parseDurationSeconds delegates to types.ParseDurationSeconds for backward
// compatibility within the controllers package.
func parseDurationSeconds(durationString string) int {
	return types.ParseDurationSeconds(durationString)
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

	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}

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
			parsedMin, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt scale min fact", "service", serviceName, "value", string(fact.Value))
			}
			policy.min = parsedMin
		case scaleField == "max":
			parsedMax, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt scale max fact", "service", serviceName, "value", string(fact.Value))
			}
			policy.max = parsedMax
		case strings.HasPrefix(scaleField, "target/"):
			metricName := strings.TrimPrefix(scaleField, "target/")
			targetValue, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt scale target fact", "service", serviceName, "metric", metricName, "value", string(fact.Value))
			}
			policy.targets[metricName] = targetValue
		}
	}

	return policies
}

// extractEventTargets parses event-driven scaling targets per service.
func extractEventTargets(facts []store.Fact) map[string]map[string]int {
	targets := make(map[string]map[string]int)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}

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

// normalizeHHMM ensures a time string is in HH:MM format with zero-padded hour,
// so lexicographic comparison against time.Format("15:04") is correct.
func normalizeHHMM(raw string) string {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return raw
	}
	hour, _ := strconv.Atoi(parts[0])
	return fmt.Sprintf("%02d:%s", hour, parts[1])
}

// extractScheduleRules parses scheduled scaling rules per service.
func extractScheduleRules(facts []store.Fact) map[string]*extractedScheduleRule {
	rules := make(map[string]*extractedScheduleRule)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDesiredServices)
		if !hasSuffix {
			continue
		}

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
			rules[serviceName].start = normalizeHHMM(string(fact.Value))
		case "end":
			rules[serviceName].end = normalizeHHMM(string(fact.Value))
		case "minimum":
			parsedMinimum, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt schedule minimum fact", "service", serviceName, "value", string(fact.Value))
			}
			rules[serviceName].minimum = parsedMinimum
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

// verticalResourceBounds holds one resource's (cpu or memory) vertical
// autoscaling bounds of one service, in millicores or bytes.
type verticalResourceBounds struct {
	minimum    int64 // minimum is the lower bound, 0 when unset
	maximum    int64 // maximum is the upper bound, 0 when unset (then no recommendation)
	unreadable bool  // unreadable is set when a bound cannot be parsed: no recommendation at all (S4)
}

// clamp limits a quantity to the readable bounds; with an unreadable bound
// the quantity is returned unchanged (used by the startup migration, D9).
func (bounds verticalResourceBounds) clamp(quantity int64) int64 {
	if bounds.unreadable {
		return quantity
	}
	if quantity < bounds.minimum {
		quantity = bounds.minimum
	}
	if bounds.maximum > 0 && quantity > bounds.maximum {
		quantity = bounds.maximum
	}
	return quantity
}

// verticalBoundKeySegment is the path between a service name and its vertical
// bounds: desired/service/{svc}/scale/vertical/{cpu|memory}/{min|max}.
const verticalBoundKeySegment = "/scale/vertical/"

// extractVerticalBoundsByResource returns the vertical bounds of every
// declared resource, keyed "{svc}/{cpu|memory}". The name is found by the
// /scale/vertical/ segment, so hierarchical names (team/web) work (B5).
func extractVerticalBoundsByResource(facts []store.Fact) map[string]verticalResourceBounds {
	boundsByResource := make(map[string]verticalResourceBounds)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName, boundPath, isVerticalBound := strings.Cut(relativePath, verticalBoundKeySegment)
		resourceKind, boundName, hasBoundName := strings.Cut(boundPath, "/")
		if !isVerticalBound || !hasBoundName || (resourceKind != "cpu" && resourceKind != "memory") {
			continue
		}
		parseBound := types.ParseMemoryBytes
		if resourceKind == "cpu" {
			parseBound = types.ParseCPUMillicores
		}
		resourceKey := serviceName + "/" + resourceKind
		bounds := boundsByResource[resourceKey]
		parsedBound, parseError := parseBound(string(fact.Value))
		switch {
		case parseError != nil:
			types.WarnUnreadableResourceOnce("autoscale", fact.Key, string(fact.Value), parseError, verticalUnreadableValueConsequence)
			bounds.unreadable = true
		case boundName == "min":
			bounds.minimum = parsedBound
		case boundName == "max":
			bounds.maximum = parsedBound
		}
		boundsByResource[resourceKey] = bounds
	}
	return boundsByResource
}

// verticalUnreadableValueConsequence is logged with a vertical bound or a
// current request the autoscaler cannot parse.
const verticalUnreadableValueConsequence = "no vertical recommendation for this resource"

// extractCurrentResources returns each service's current CPU request in
// millicores and memory request in bytes, effective-first, each read with its
// own parser (M243 D6). A service whose value cannot be parsed is left out,
// so it gets no recommendation.
func extractCurrentResources(facts []store.Fact) (map[string]int, map[string]int) {
	cpuMillicores, _ := collectCPUMillicoresBySuffix(facts, "resources/cpu", "autoscale", verticalUnreadableValueConsequence)
	memoryBytes, _ := collectMemoryBytesBySuffix(facts, "resources/memory", "autoscale", verticalUnreadableValueConsequence)
	return convertQuantitiesToInt(cpuMillicores), convertQuantitiesToInt(memoryBytes)
}

// convertQuantitiesToInt converts parsed quantities to the int values the
// recommendation arithmetic uses (64-bit on every release platform).
func convertQuantitiesToInt(quantitiesByService map[string]int64) map[string]int {
	converted := make(map[string]int, len(quantitiesByService))
	for serviceName, quantity := range quantitiesByService {
		converted[serviceName] = int(quantity)
	}
	return converted
}

// extractObservedMetrics scans observed metric facts and returns a map of
// "service/metric" -> current integer value.
func extractObservedMetrics(facts []store.Fact) map[string]int {
	metrics := make(map[string]int)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedMetrics) {
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

// extractActiveInstanceCounts returns a map of service name to the number
// of non-stopped instances, using effective state from derived markers.
func extractActiveInstanceCounts(facts []store.Fact) map[string]int {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	counts := make(map[string]int)
	for _, fields := range instanceFields {
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}
		state := effectiveInstanceState(fields)
		if state != types.InstanceStopped {
			counts[serviceName]++
		}
	}
	return counts
}
