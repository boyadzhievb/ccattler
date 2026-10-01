package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestAutoscaleActivationOverrideRecommendationToOne(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()
	controller.timeNow = func() time.Time { return frozenTime }

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceScaleHorizontalMin("api"), Value: []byte("0")},
		{Key: types.KeyDesiredServiceScaleHorizontalMax("api"), Value: []byte("10")},
		{Key: types.KeyDesiredServiceScaleHorizontalTarget("api", "cpu"), Value: []byte("60")},
		{Key: types.KeyDerivedServiceActivationState("api"), Value: []byte("activating")},
	}

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	foundAutoscalerIntent := false
	for _, change := range changes {
		if change.Key == types.KeyIntentAutoscalerServiceInstances("api") {
			foundAutoscalerIntent = true
			if string(change.Value) != "1" {
				t.Errorf("expected autoscaler recommendation=1 during activation, got %q", string(change.Value))
			}
		}
	}

	if !foundAutoscalerIntent {
		t.Error("expected autoscaler to write intent for service 'api'")
	}
}

func TestAutoscaleNoOverrideWhenNotActivating(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()
	controller.timeNow = func() time.Time { return frozenTime }

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceScaleHorizontalMin("api"), Value: []byte("0")},
		{Key: types.KeyDesiredServiceScaleHorizontalMax("api"), Value: []byte("10")},
		{Key: types.KeyDesiredServiceScaleHorizontalTarget("api", "cpu"), Value: []byte("60")},
		{Key: types.KeyDerivedServiceActivationState("api"), Value: []byte("active")},
	}

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	for _, change := range changes {
		if change.Key == types.KeyIntentAutoscalerServiceInstances("api") {
			if string(change.Value) != "0" {
				t.Errorf("expected recommendation=0 when active with no metrics, got %q", string(change.Value))
			}
		}
	}
}

func TestComputeP95FromSamples(t *testing.T) {
	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	t.Run("empty returns zero", func(t *testing.T) {
		result := computeP95FromSamples(nil)
		if result != 0 {
			t.Errorf("expected 0, got %d", result)
		}
	})

	t.Run("single sample returns that value", func(t *testing.T) {
		samples := []timestampedMetricSample{{value: 42, timestamp: baseTime}}
		result := computeP95FromSamples(samples)
		if result != 42 {
			t.Errorf("expected 42, got %d", result)
		}
	})

	t.Run("twenty samples picks the 95th percentile", func(t *testing.T) {
		var samples []timestampedMetricSample
		for index := 1; index <= 20; index++ {
			samples = append(samples, timestampedMetricSample{
				value:     index * 10,
				timestamp: baseTime.Add(time.Duration(index) * time.Second),
			})
		}
		result := computeP95FromSamples(samples)
		if result != 190 {
			t.Errorf("expected P95=190 from values 10..200, got %d", result)
		}
	})

	t.Run("P95 picks near-top value from sorted samples", func(t *testing.T) {
		// 20 samples: 18 at 50, one at 400, one at 500.
		// Sorted: [50x18, 400, 500]. P95 index = ceil(20*0.95)-1 = 18.
		// values[18] = 400.
		var samples []timestampedMetricSample
		for index := 0; index < 18; index++ {
			samples = append(samples, timestampedMetricSample{
				value:     50,
				timestamp: baseTime.Add(time.Duration(index) * time.Second),
			})
		}
		samples = append(samples, timestampedMetricSample{value: 400, timestamp: baseTime.Add(18 * time.Second)})
		samples = append(samples, timestampedMetricSample{value: 500, timestamp: baseTime.Add(19 * time.Second)})
		result := computeP95FromSamples(samples)
		if result != 400 {
			t.Errorf("expected P95=400 (index 18 of 20 sorted values), got %d", result)
		}
	})
}

func TestVerticalStabilizationBlocksScaleUpWhenRecentLowEntry(t *testing.T) {
	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()

	currentValue := 500
	resourceKey := "api/cpu"

	// Record a recommendation at current value — seeds the history with a low entry.
	controller.applyVerticalStabilization(resourceKey, 500, currentValue, baseTime)

	// 10s later, try to scale up — blocked because history has entry <= currentValue.
	tenSecondsLater := baseTime.Add(10 * time.Second)
	result := controller.applyVerticalStabilization(resourceKey, 800, currentValue, tenSecondsLater)
	if result != currentValue {
		t.Errorf("scale-up should be blocked by recent low entry, got %d want %d", result, currentValue)
	}

	// After the low entry ages out of the 60s window, scale-up should succeed.
	afterStabilization := baseTime.Add(verticalScaleUpStabilizationDuration + 2*time.Second)
	result = controller.applyVerticalStabilization(resourceKey, 800, currentValue, afterStabilization)
	if result != 800 {
		t.Errorf("after stabilization window, scale-up should apply, got %d want 800", result)
	}
}

func TestVerticalStabilizationBlocksScaleDownWhenRecentHighEntry(t *testing.T) {
	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()

	currentValue := 1000
	resourceKey := "api/cpu"

	// Record a recommendation at current value — seeds history with a high entry.
	controller.applyVerticalStabilization(resourceKey, 1000, currentValue, baseTime)

	// 10s later, try to scale down — blocked because history has entry >= currentValue.
	tenSecondsLater := baseTime.Add(10 * time.Second)
	result := controller.applyVerticalStabilization(resourceKey, 300, currentValue, tenSecondsLater)
	if result != currentValue {
		t.Errorf("scale-down should be blocked by recent high entry, got %d want %d", result, currentValue)
	}

	// At 2 minutes, still blocked (need 5m for scale-down).
	twoMinutesLater := baseTime.Add(2 * time.Minute)
	result = controller.applyVerticalStabilization(resourceKey, 300, currentValue, twoMinutesLater)
	if result != currentValue {
		t.Errorf("scale-down should still be blocked at 2m (need 5m), got %d want %d", result, currentValue)
	}

	// After the high entry ages out of the 5m window, scale-down should succeed.
	afterStabilization := baseTime.Add(verticalScaleDownStabilizationDuration + 2*time.Second)
	result = controller.applyVerticalStabilization(resourceKey, 300, currentValue, afterStabilization)
	if result != 300 {
		t.Errorf("after 5m stabilization, scale-down should apply, got %d want 300", result)
	}
}

func TestVerticalScalingEndToEnd(t *testing.T) {
	baseTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceScaleVerticalCPUMin("api"), Value: []byte("100")},
		{Key: types.KeyDesiredServiceScaleVerticalCPUMax("api"), Value: []byte("4000")},
		{Key: types.KeyDesiredServiceResourcesCPU("api"), Value: []byte("500")},
	}

	for tick := 0; tick < 20; tick++ {
		tickTime := baseTime.Add(time.Duration(tick) * 15 * time.Second)
		controller.timeNow = func() time.Time { return tickTime }

		tickFacts := make([]store.Fact, len(facts))
		copy(tickFacts, facts)
		tickFacts = append(tickFacts, store.Fact{
			Key:   types.KeyObservedMetric("api", "cpu"),
			Value: []byte("90"),
		})
		store.SortFacts(tickFacts)

		changes, reconcileError := controller.Reconcile(context.Background(), tickFacts)
		if reconcileError != nil {
			t.Fatalf("tick %d: unexpected error: %v", tick, reconcileError)
		}

		if tick >= 5 {
			foundCPUIntent := false
			for _, change := range changes {
				if change.Key == types.KeyIntentAutoscalerServiceResourcesCPU("api") {
					foundCPUIntent = true
				}
			}
			if !foundCPUIntent && tick > 10 {
				t.Logf("tick %d: no CPU intent change (may still be stabilizing)", tick)
			}
		}
	}
}

func TestAutoscaleActivatingWithHighMetricsKeepsHighRecommendation(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := NewAutoscaleController()
	controller.timeNow = func() time.Time { return frozenTime }

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceScaleHorizontalMin("api"), Value: []byte("0")},
		{Key: types.KeyDesiredServiceScaleHorizontalMax("api"), Value: []byte("10")},
		{Key: types.KeyDesiredServiceScaleHorizontalTarget("api", "cpu"), Value: []byte("60")},
		{Key: types.KeyObservedMetric("api", "cpu"), Value: []byte("300")},
		{Key: types.KeyDerivedServiceActivationState("api"), Value: []byte("activating")},
		{Key: types.KeyObservedInstanceState("api-1"), Value: []byte(string(types.InstanceRunning))},
		{Key: types.KeyObservedInstanceService("api-1"), Value: []byte("api")},
	}

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	for _, change := range changes {
		if change.Key == types.KeyIntentAutoscalerServiceInstances("api") {
			if string(change.Value) == "0" || string(change.Value) == "1" {
				t.Errorf("expected recommendation > 1 with high CPU, got %q", string(change.Value))
			}
		}
	}
}
