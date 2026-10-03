package loadtest

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// oscillationTestServiceName is the service used in the oscillation test.
	oscillationTestServiceName = "oscillating-svc"
	// oscillationTestInstanceCount is the current instance count for the test.
	oscillationTestInstanceCount = 10
	// oscillationTestMinInstances is the minimum instance count for the scale policy.
	oscillationTestMinInstances = 2
	// oscillationTestMaxInstances is the maximum instance count for the scale policy.
	oscillationTestMaxInstances = 50
	// oscillationTestCPUTarget is the CPU target percentage for the scale policy.
	oscillationTestCPUTarget = 60
	// oscillationStabilizationUpSeconds is the scale-up stabilization window.
	oscillationStabilizationUpSeconds = 60
	// oscillationStabilizationDownSeconds is the scale-down stabilization window.
	oscillationStabilizationDownSeconds = 300
	// oscillationCycleCount is the number of high-low metric cycles to run.
	oscillationCycleCount = 10
	// oscillationCycleInterval is the simulated time between metric observations.
	oscillationCycleInterval = 10 * time.Second
)

// buildOscillationFacts creates a fact slice for the autoscaler oscillation
// test: a service with a horizontal scale policy, stabilization windows, and
// a configurable current CPU metric value. The instance count is fixed at
// oscillationTestInstanceCount to observe the autoscaler's recommendation
// changes in isolation.
func buildOscillationFacts(cpuMetricValue int) []store.Fact {
	serviceName := oscillationTestServiceName
	facts := []store.Fact{
		{Key: types.KeyDesiredServiceImage(serviceName), Value: []byte("app:v1")},
		{Key: types.KeyEffectiveServiceInstances(serviceName), Value: []byte(strconv.Itoa(oscillationTestInstanceCount))},
		{Key: types.KeyDesiredServiceScaleHorizontalMin(serviceName), Value: []byte(strconv.Itoa(oscillationTestMinInstances))},
		{Key: types.KeyDesiredServiceScaleHorizontalMax(serviceName), Value: []byte(strconv.Itoa(oscillationTestMaxInstances))},
		{Key: types.KeyDesiredServiceScaleHorizontalTarget(serviceName, "cpu"), Value: []byte(strconv.Itoa(oscillationTestCPUTarget))},
		{Key: types.KeyDesiredServiceScaleStabilizationUp(serviceName), Value: []byte(fmt.Sprintf("%ds", oscillationStabilizationUpSeconds))},
		{Key: types.KeyDesiredServiceScaleStabilizationDown(serviceName), Value: []byte(fmt.Sprintf("%ds", oscillationStabilizationDownSeconds))},
		{Key: types.KeyObservedMetric(serviceName, "cpu"), Value: []byte(strconv.Itoa(cpuMetricValue))},
	}

	for instanceIndex := 0; instanceIndex < oscillationTestInstanceCount; instanceIndex++ {
		instanceID := fmt.Sprintf("osc-inst-%03d", instanceIndex)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			store.Fact{Key: types.KeyObservedInstanceService(instanceID), Value: []byte(serviceName)},
			store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstanceRunning))},
		)
	}

	store.SortFacts(facts)
	return facts
}

// extractRecommendedCount reads the autoscaler intent recommendation from the
// changes produced by a Reconcile call.
func extractRecommendedCount(changes []controllers.Change) (int, bool) {
	expectedKey := types.KeyIntentAutoscalerServiceInstances(oscillationTestServiceName)
	for _, change := range changes {
		if change.Key == expectedKey {
			count, parseError := strconv.Atoi(string(change.Value))
			if parseError == nil {
				return count, true
			}
		}
	}
	return 0, false
}

// TestAutoscalerOscillation verifies that the autoscaler's stabilization
// windows prevent rapid scaling oscillation. It repeatedly alternates between
// high (90%) and low (20%) CPU metrics every 10 simulated seconds and checks
// that the recommended instance count does not change on every cycle. Without
// stabilization, the recommendation would swing between ~15 and ~4 every
// cycle. With stabilization (60s up, 300s down), the count should remain
// stable at the initial level because neither window is satisfied by a single
// cycle.
func TestAutoscalerOscillation(testHandle *testing.T) {
	simulatedTime := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	autoscaleController := controllers.NewAutoscaleController()
	autoscaleController.SetTimeNow(func() time.Time { return simulatedTime })

	// Prime the stabilization window with a few consistent observations at
	// current load so the autoscaler has baseline history.
	for primeStep := 0; primeStep < 3; primeStep++ {
		facts := buildOscillationFacts(60)
		autoscaleController.Reconcile(context.Background(), facts)
		simulatedTime = simulatedTime.Add(oscillationCycleInterval)
	}

	var recommendations []int
	changeCount := 0
	var previousRecommendation int

	for cycleIndex := 0; cycleIndex < oscillationCycleCount; cycleIndex++ {
		// Alternate between high and low CPU.
		cpuValue := 90
		if cycleIndex%2 == 1 {
			cpuValue = 20
		}

		facts := buildOscillationFacts(cpuValue)
		changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
		if reconcileError != nil {
			testHandle.Fatalf("reconcile error at cycle %d: %v", cycleIndex, reconcileError)
		}

		recommendedCount, hasRecommendation := extractRecommendedCount(changes)
		if !hasRecommendation {
			testHandle.Fatalf("no recommendation produced at cycle %d", cycleIndex)
		}
		recommendations = append(recommendations, recommendedCount)

		if cycleIndex > 0 && recommendedCount != previousRecommendation {
			changeCount++
		}
		previousRecommendation = recommendedCount

		simulatedTime = simulatedTime.Add(oscillationCycleInterval)
	}

	testHandle.Logf("recommendations over %d cycles: %v", oscillationCycleCount, recommendations)
	testHandle.Logf("recommendation changes: %d", changeCount)

	// Without stabilization the count would change on every cycle (up to 9
	// changes over 10 cycles). With stabilization windows the count should
	// change at most twice (one initial adjustment, possibly one correction).
	maxAllowedChanges := 2
	if changeCount > maxAllowedChanges {
		testHandle.Fatalf("autoscaler oscillated %d times over %d cycles (max allowed %d) — stabilization not effective",
			changeCount, oscillationCycleCount, maxAllowedChanges)
	}
}
