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
// and observed instances (current count for ratio computation).
func (autoscaleController *AutoscaleController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedMetrics,
		types.ScanObservedInstances,
		types.ScanIntentAutoscalerServices,
		types.ScanDerivedServices,
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
	activationStates := extractActivationStates(facts)

	currentTime := autoscaleController.timeNow()

	horizontalChanges := autoscaleController.buildHorizontalScalingChanges(
		scalePolicies, observedMetrics, activeInstanceCounts,
		eventTargets, scheduleRules, stabilizationWindows,
		activationStates, currentTime,
	)
	verticalChanges := autoscaleController.buildVerticalScalingChanges(
		verticalPolicies, currentCPU, currentMemory, observedMetrics, currentTime,
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

// buildVerticalScalingChanges evaluates vertical autoscaling policies for each
// service and produces changes that recommend CPU and memory resource levels.
// Uses P95 of the metric sliding window (not instantaneous values) and applies
// asymmetric stabilization (fast scale-up, slow scale-down).
func (autoscaleController *AutoscaleController) buildVerticalScalingChanges(
	verticalPolicies map[string]*extractedVerticalPolicy,
	currentCPU map[string]int,
	currentMemory map[string]int,
	observedMetrics map[string]int,
	currentTime time.Time,
) []Change {
	autoscaleController.recommendationHistoryMutex.Lock()
	defer autoscaleController.recommendationHistoryMutex.Unlock()

	autoscaleController.recordVerticalMetricSamples(observedMetrics, currentTime)

	var changes []Change
	for serviceName, verticalPolicy := range verticalPolicies {
		cpuChange := autoscaleController.computeVerticalResourceChange(
			serviceName, "cpu", currentCPU[serviceName],
			verticalPolicy.cpuMin, verticalPolicy.cpuMax,
			types.KeyIntentAutoscalerServiceResourcesCPU(serviceName), currentTime,
		)
		if cpuChange != nil {
			changes = append(changes, *cpuChange)
		}

		memoryChange := autoscaleController.computeVerticalResourceChange(
			serviceName, "memory", currentMemory[serviceName],
			verticalPolicy.memoryMin, verticalPolicy.memoryMax,
			types.KeyIntentAutoscalerServiceResourcesMemory(serviceName), currentTime,
		)
		if memoryChange != nil {
			changes = append(changes, *memoryChange)
		}
	}
	return changes
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
// recommendation for a single resource dimension (cpu or memory). Returns nil
// if no change is needed or if insufficient data is available.
func (autoscaleController *AutoscaleController) computeVerticalResourceChange(
	serviceName string,
	resourceType string,
	currentResourceValue int,
	policyMin int,
	policyMax int,
	intentKey string,
	currentTime time.Time,
) *Change {
	if currentResourceValue <= 0 || policyMax <= 0 {
		return nil
	}

	metricKey := serviceName + "/" + resourceType
	p95Usage := computeP95FromSamples(autoscaleController.verticalMetricHistory[metricKey])
	if p95Usage <= 0 {
		return nil
	}

	rawRecommendation := int(math.Ceil(
		float64(currentResourceValue) * float64(p95Usage) / float64(verticalTargetUtilizationPercent),
	))
	rawRecommendation = clampToRange(rawRecommendation, policyMin, policyMax)

	stabilizedRecommendation := autoscaleController.applyVerticalStabilization(
		metricKey, rawRecommendation, currentResourceValue, currentTime,
	)

	if stabilizedRecommendation == currentResourceValue {
		return nil
	}

	return &Change{
		Type:  store.OpPut,
		Key:   intentKey,
		Value: []byte(strconv.Itoa(stabilizedRecommendation)),
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
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
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
			rules[serviceName].start = normalizeHHMM(string(fact.Value))
		case "end":
			rules[serviceName].end = normalizeHHMM(string(fact.Value))
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
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
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
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
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

// extractActiveInstanceCounts scans observed instance facts and returns a map
// of service name to the number of non-stopped instances.
func extractActiveInstanceCounts(facts []store.Fact) map[string]int {
	serviceByID := make(map[string]string)
	stateByID := make(map[string]types.InstanceState)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
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
