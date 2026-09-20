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
	controller := &AutoscaleController{
		recommendationHistory: make(map[string][]timestampedRecommendation),
		timeNow:               func() time.Time { return frozenTime },
	}

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
	controller := &AutoscaleController{
		recommendationHistory: make(map[string][]timestampedRecommendation),
		timeNow:               func() time.Time { return frozenTime },
	}

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

func TestAutoscaleActivatingWithHighMetricsKeepsHighRecommendation(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &AutoscaleController{
		recommendationHistory: make(map[string][]timestampedRecommendation),
		timeNow:               func() time.Time { return frozenTime },
	}

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
