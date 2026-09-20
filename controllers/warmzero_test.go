package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// buildWarmZeroFacts creates a standard set of facts for a warm-zero-enabled
// service with customizable state, instances, and endpoints.
func buildWarmZeroFacts(serviceName string, activationState string, runningInstances int, endpointCount int, lastRequestTimeMillis int64, idleTimeout string) []store.Fact {
	var facts []store.Fact

	facts = append(facts, store.Fact{
		Key:   types.KeyDesiredServiceScaleHorizontalMin(serviceName),
		Value: []byte("0"),
	})
	facts = append(facts, store.Fact{
		Key:   types.KeyDesiredServiceScaleIdleTimeout(serviceName),
		Value: []byte(idleTimeout),
	})

	if activationState != "" {
		facts = append(facts, store.Fact{
			Key:   types.KeyDerivedServiceActivationState(serviceName),
			Value: []byte(activationState),
		})
	}

	for instanceIndex := 0; instanceIndex < runningInstances; instanceIndex++ {
		instanceID := serviceName + "-instance-" + string(rune('a'+instanceIndex))
		facts = append(facts, store.Fact{
			Key:   types.KeyObservedInstanceService(instanceID),
			Value: []byte(serviceName),
		})
		facts = append(facts, store.Fact{
			Key:   types.KeyObservedInstanceState(instanceID),
			Value: []byte(string(types.InstanceRunning)),
		})
	}

	for endpointIndex := 0; endpointIndex < endpointCount; endpointIndex++ {
		instanceID := serviceName + "-instance-" + string(rune('a'+endpointIndex))
		facts = append(facts, store.Fact{
			Key:   types.KeyEndpoint(serviceName, instanceID, 8080),
			Value: []byte("10.0.0.1:8080"),
		})
	}

	if lastRequestTimeMillis > 0 {
		facts = append(facts, store.Fact{
			Key:   types.KeyObservedServiceLastRequestTime(serviceName),
			Value: []byte(formatInt64(lastRequestTimeMillis)),
		})
	}

	store.SortFacts(facts)
	return facts
}

func formatInt64(value int64) string {
	result := ""
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	if negative {
		result = "-" + result
	}
	return result
}

func TestWarmZeroIdleTimeoutTriggersInactive(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	lastRequestTime := frozenTime.Add(-6 * time.Minute)

	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "active", 2, 2, lastRequestTime.UnixMilli(), "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "inactive" {
		t.Errorf("expected state 'inactive', got %q", string(changes[0].Value))
	}
}

func TestWarmZeroActivatingTransitionsToActive(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "activating", 1, 1, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "active" {
		t.Errorf("expected state 'active', got %q", string(changes[0].Value))
	}
}

func TestWarmZeroActivatingNoEndpointsNoChange(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "activating", 1, 0, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes during activating without endpoints, got %d", len(changes))
	}
}

func TestWarmZeroActiveNoInstancesTransitionsToInactive(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "active", 0, 0, frozenTime.UnixMilli(), "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "inactive" {
		t.Errorf("expected state 'inactive', got %q", string(changes[0].Value))
	}
}

func TestWarmZeroAlreadyInactiveNoChange(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "inactive", 0, 0, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no redundant changes for already-inactive service, got %d", len(changes))
	}
}

func TestWarmZeroIgnoresNonMinZeroService(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceScaleHorizontalMin("web"), Value: []byte("2")},
		{Key: types.KeyDesiredServiceScaleIdleTimeout("web"), Value: []byte("5m")},
	}

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("should ignore service with min>0, got %d changes", len(changes))
	}
}

func TestWarmZeroInitialStateSetToInactive(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "", 0, 0, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change for initial state, got %d", len(changes))
	}
	if string(changes[0].Value) != "inactive" {
		t.Errorf("expected initial state 'inactive', got %q", string(changes[0].Value))
	}
}

func TestWarmZeroActiveWithinIdleTimeoutNoChange(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	lastRequestTime := frozenTime.Add(-2 * time.Minute)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "active", 2, 2, lastRequestTime.UnixMilli(), "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes when within idle timeout, got %d", len(changes))
	}
}

func TestWarmZeroInactiveToActiveWhenEndpointsAppear(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "inactive", 1, 1, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "active" {
		t.Errorf("expected state 'active', got %q", string(changes[0].Value))
	}
}

func TestWarmZeroMultipleServicesIndependent(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	lastRequestTime := frozenTime.Add(-10 * time.Minute)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	var facts []store.Fact

	facts = append(facts, buildWarmZeroFacts("api", "active", 2, 2, lastRequestTime.UnixMilli(), "5m")...)
	facts = append(facts, buildWarmZeroFacts("web", "activating", 1, 1, 0, "5m")...)

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (one per service), got %d", len(changes))
	}

	changeMap := make(map[string]string)
	for _, change := range changes {
		changeMap[change.Key] = string(change.Value)
	}

	apiState := changeMap[types.KeyDerivedServiceActivationState("api")]
	if apiState != "inactive" {
		t.Errorf("api should be 'inactive' (idle timeout), got %q", apiState)
	}

	webState := changeMap[types.KeyDerivedServiceActivationState("web")]
	if webState != "active" {
		t.Errorf("web should be 'active' (endpoints appeared), got %q", webState)
	}
}

func TestWarmZeroUnknownStateNoChange(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "bogus-state", 1, 1, 0, "5m")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes for unknown state, got %d", len(changes))
	}
}

func TestWarmZeroIdleTimeoutZeroMeansNoScaleDown(t *testing.T) {
	frozenTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	lastRequestTime := frozenTime.Add(-1 * time.Hour)
	controller := &WarmZeroController{timeNow: func() time.Time { return frozenTime }}

	facts := buildWarmZeroFacts("api", "active", 2, 2, lastRequestTime.UnixMilli(), "invalid-value")

	store.SortFacts(facts)
	changes, reconcileError := controller.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	if len(changes) != 0 {
		t.Errorf("expected no changes when idle timeout is 0 (unparseable), got %d", len(changes))
	}
}
