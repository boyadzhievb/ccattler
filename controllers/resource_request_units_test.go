// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// verticalTestBaseTime is the frozen clock of the vertical autoscaler tests.
var verticalTestBaseTime = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// buildVerticalPolicyFacts returns web's facts: a vertical policy with unit
// bounds, a 500m / 512Mi request, and the given cpu and memory utilization.
func buildVerticalPolicyFacts(cpuPercent string, memoryPercent string, extraFacts ...store.Fact) []store.Fact {
	facts := []store.Fact{
		{Key: types.KeyDesiredService("web"), Value: []byte("")},
		{Key: types.KeyDesiredServiceResourcesCPU("web"), Value: []byte("500m")},
		{Key: types.KeyDesiredServiceResourcesMemory("web"), Value: []byte("512Mi")},
		{Key: types.KeyDesiredServiceScaleVerticalCPUMin("web"), Value: []byte("250m")},
		{Key: types.KeyDesiredServiceScaleVerticalCPUMax("web"), Value: []byte("4")},
		{Key: types.KeyDesiredServiceScaleVerticalMemoryMin("web"), Value: []byte("256Mi")},
		{Key: types.KeyDesiredServiceScaleVerticalMemoryMax("web"), Value: []byte("8Gi")},
		{Key: types.KeyObservedMetric("web", "cpu"), Value: []byte(cpuPercent)},
		{Key: types.KeyObservedMetric("web", "memory"), Value: []byte(memoryPercent)},
	}
	facts = append(facts, extraFacts...)
	store.SortFacts(facts)
	return facts
}

// changeValueByKey indexes put changes by key and records deleted keys with a
// nil value.
func changeValueByKey(changes []Change) map[string][]byte {
	valuesByKey := make(map[string][]byte, len(changes))
	for _, change := range changes {
		if change.Type == store.OpDelete {
			valuesByKey[change.Key] = nil
			continue
		}
		valuesByKey[change.Key] = change.Value
	}
	return valuesByKey
}

// TestVerticalAutoscalerWritesQuantitiesWithUnits: spec resource-requests,
// "CPU recommendation". 105% of a 500m request at a 70% target is 750m, and
// 105% of 512Mi is 768Mi; the intent resolver copies both to effective.
func TestVerticalAutoscalerWritesQuantitiesWithUnits(t *testing.T) {
	autoscaleController := NewAutoscaleController()
	autoscaleController.SetTimeNow(func() time.Time { return verticalTestBaseTime })
	autoscalerChanges, reconcileError := autoscaleController.Reconcile(context.Background(), buildVerticalPolicyFacts("105", "105"))
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	autoscalerValues := changeValueByKey(autoscalerChanges)
	if cpuIntent := string(autoscalerValues[types.KeyIntentAutoscalerServiceResourcesCPU("web")]); cpuIntent != "750m" {
		t.Errorf("intent cpu = %q, want 750m", cpuIntent)
	}
	if memoryIntent := string(autoscalerValues[types.KeyIntentAutoscalerServiceResourcesMemory("web")]); memoryIntent != "768Mi" {
		t.Errorf("intent memory = %q, want 768Mi", memoryIntent)
	}

	intentFacts := []store.Fact{
		{Key: types.KeyDesiredService("web"), Value: []byte("")},
		{Key: types.KeyDesiredServiceResourcesCPU("web"), Value: []byte("500m")},
		{Key: types.KeyIntentAutoscalerServiceResourcesCPU("web"), Value: autoscalerValues[types.KeyIntentAutoscalerServiceResourcesCPU("web")]},
	}
	store.SortFacts(intentFacts)
	intentChanges, intentError := NewIntentResolverController().Reconcile(context.Background(), intentFacts)
	if intentError != nil {
		t.Fatal(intentError)
	}
	if effectiveCPU := string(changeValueByKey(intentChanges)[types.KeyEffectiveServiceResourcesCPU("web")]); effectiveCPU != "750m" {
		t.Errorf("effective cpu = %q, want 750m", effectiveCPU)
	}
}

// putFacts writes every key/value pair into the store, failing the test on error.
func putFacts(t *testing.T, factStore store.StateStore, valuesByKey map[string]string) {
	t.Helper()
	for factKey, factValue := range valuesByKey {
		if _, putError := factStore.Put(context.Background(), factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
}

// readValue returns the stored value of key, or "<absent>".
func readValue(factStore store.StateStore, key string) string {
	fact, getError := factStore.Get(context.Background(), key)
	if getError != nil {
		return "<absent>"
	}
	return string(fact.Value)
}

// TestMigrateUnitlessResourceRecommendations: spec resource-requests, "Legacy
// recommendation without a unit" and "Legacy value outside the bounds" (D9).
// Bare recommendations and effective values get units (cpu millicores, memory
// bytes), clamped to the bounds; desired/ is untouched; a second run is a no-op.
func TestMigrateUnitlessResourceRecommendations(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	putFacts(t, factStore, map[string]string{
		types.KeyDesiredServiceResourcesCPU("web"):                  "750",
		types.KeyDesiredServiceScaleVerticalCPUMin("web"):           "250m",
		types.KeyDesiredServiceScaleVerticalCPUMax("web"):           "4",
		types.KeyIntentAutoscalerServiceResourcesCPU("web"):         "750",
		types.KeyEffectiveServiceResourcesCPU("web"):                "750",
		types.KeyIntentAutoscalerServiceResourcesMemory("web"):      "805306368",
		types.KeyEffectiveServiceResourcesMemory("web"):             "805306368",
		types.KeyDesiredServiceScaleVerticalCPUMin("team/api"):      "250m",
		types.KeyDesiredServiceScaleVerticalCPUMax("team/api"):      "4",
		types.KeyIntentAutoscalerServiceResourcesCPU("team/api"):    "4",
		types.KeyEffectiveServiceResourcesCPU("team/api"):           "4",
		types.KeyIntentAutoscalerServiceResourcesMemory("team/api"): "512Mi",
	})

	rewrittenCount, migrationError := MigrateUnitlessResourceRecommendations(context.Background(), factStore)
	if migrationError != nil {
		t.Fatal(migrationError)
	}
	wantValues := map[string]string{
		types.KeyIntentAutoscalerServiceResourcesCPU("web"):         "750m",
		types.KeyEffectiveServiceResourcesCPU("web"):                "750m",
		types.KeyIntentAutoscalerServiceResourcesMemory("web"):      "768Mi",
		types.KeyEffectiveServiceResourcesMemory("web"):             "768Mi",
		types.KeyIntentAutoscalerServiceResourcesCPU("team/api"):    "250m",
		types.KeyEffectiveServiceResourcesCPU("team/api"):           "250m",
		types.KeyIntentAutoscalerServiceResourcesMemory("team/api"): "512Mi",
		types.KeyDesiredServiceResourcesCPU("web"):                  "750",
	}
	for factKey, wantValue := range wantValues {
		if gotValue := readValue(factStore, factKey); gotValue != wantValue {
			t.Errorf("%s = %q after the migration, want %q", factKey, gotValue, wantValue)
		}
	}
	if rewrittenCount != 6 {
		t.Errorf("rewrote %d values, want 6", rewrittenCount)
	}

	revisionBefore, _ := factStore.Revision(context.Background())
	secondCount, secondError := MigrateUnitlessResourceRecommendations(context.Background(), factStore)
	revisionAfter, _ := factStore.Revision(context.Background())
	if secondError != nil || secondCount != 0 || revisionAfter != revisionBefore {
		t.Errorf("second run: %d rewritten, error %v, revision %d → %d; want a no-op", secondCount, secondError, revisionBefore, revisionAfter)
	}
}

// TestVerticalAutoscalerSkipsResourceWithUnreadableBound: spec
// resource-requests, "Unreadable bound" (S4). A CPU min that cannot be read
// means no CPU recommendation at all; memory is still recommended.
func TestVerticalAutoscalerSkipsResourceWithUnreadableBound(t *testing.T) {
	autoscaleController := NewAutoscaleController()
	autoscaleController.SetTimeNow(func() time.Time { return verticalTestBaseTime })
	facts := buildVerticalPolicyFacts("200", "105")
	for factIndex := range facts {
		if facts[factIndex].Key == types.KeyDesiredServiceScaleVerticalCPUMin("web") {
			facts[factIndex].Value = []byte("lots")
		}
	}
	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	changedValues := changeValueByKey(changes)
	if cpuIntent, changed := changedValues[types.KeyIntentAutoscalerServiceResourcesCPU("web")]; changed {
		t.Errorf("CPU recommendation %q with an unreadable min bound, want none", cpuIntent)
	}
	if memoryIntent := string(changedValues[types.KeyIntentAutoscalerServiceResourcesMemory("web")]); memoryIntent != "768Mi" {
		t.Errorf("memory recommendation = %q, want 768Mi", memoryIntent)
	}
}

// TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU: spec resource-requests,
// "One resource removed" (B3, B5). With only the memory policy left, the CPU
// recommendation and effective CPU are deleted and memory's stay, for a flat
// and a hierarchical service name.
func TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU(t *testing.T) {
	for _, serviceName := range []string{"web", "team/web"} {
		facts := []store.Fact{
			{Key: types.KeyDesiredService(serviceName), Value: []byte("")},
			{Key: types.KeyDesiredServiceResourcesMemory(serviceName), Value: []byte("256Mi")},
			{Key: types.KeyDesiredServiceScaleVerticalMemoryMin(serviceName), Value: []byte("128Mi")},
			{Key: types.KeyDesiredServiceScaleVerticalMemoryMax(serviceName), Value: []byte("1Gi")},
			{Key: types.KeyIntentAutoscalerServiceResourcesCPU(serviceName), Value: []byte("750m")},
			{Key: types.KeyIntentAutoscalerServiceResourcesMemory(serviceName), Value: []byte("512Mi")},
			{Key: types.KeyEffectiveServiceResourcesCPU(serviceName), Value: []byte("750m")},
			{Key: types.KeyEffectiveServiceResourcesMemory(serviceName), Value: []byte("512Mi")},
		}
		store.SortFacts(facts)
		autoscalerChanges, reconcileError := NewAutoscaleController().Reconcile(context.Background(), facts)
		if reconcileError != nil {
			t.Fatal(reconcileError)
		}
		assertOnlyDeletion(t, serviceName+" autoscaler", autoscalerChanges,
			types.KeyIntentAutoscalerServiceResourcesCPU(serviceName), types.KeyIntentAutoscalerServiceResourcesMemory(serviceName))

		var withoutCPUIntent []store.Fact
		for _, fact := range facts {
			if fact.Key != types.KeyIntentAutoscalerServiceResourcesCPU(serviceName) {
				withoutCPUIntent = append(withoutCPUIntent, fact)
			}
		}
		intentChanges, intentError := NewIntentResolverController().Reconcile(context.Background(), withoutCPUIntent)
		if intentError != nil {
			t.Fatal(intentError)
		}
		assertOnlyDeletion(t, serviceName+" intent resolver", intentChanges,
			types.KeyEffectiveServiceResourcesCPU(serviceName), types.KeyEffectiveServiceResourcesMemory(serviceName))
	}
}

// assertOnlyDeletion fails unless changes delete deletedKey and leave keptKey
// alone (no put or delete).
func assertOnlyDeletion(t *testing.T, description string, changes []Change, deletedKey string, keptKey string) {
	t.Helper()
	changedValues := changeValueByKey(changes)
	if deletedValue, changed := changedValues[deletedKey]; !changed || deletedValue != nil {
		t.Errorf("%s: %s change = %q (changed %v), want a delete", description, deletedKey, deletedValue, changed)
	}
	if keptValue, changed := changedValues[keptKey]; changed {
		t.Errorf("%s: %s changed to %q, want it kept", description, keptKey, keptValue)
	}
}

// evictionTestFacts returns a node at 95% memory (1024 MiB, 50 available) and
// two running BestEffort instances, inst-a of service alpha and inst-b of beta,
// plus the given request facts.
func evictionTestFacts(requestFacts ...struct{ k, v string }) []store.Fact {
	entries := []struct{ k, v string }{
		kv(types.KeyObservedNodeCapacityMemory("node01"), "1024"),
		kv(types.KeyObservedNodeAvailableMemory("node01"), "50"),
		kv(types.KeyObservedInstanceNode("inst-a"), "node01"),
		kv(types.KeyObservedInstanceService("inst-a"), "alpha"),
		kv(types.KeyObservedInstanceState("inst-a"), "running"),
		kv(types.KeyObservedInstanceNode("inst-b"), "node01"),
		kv(types.KeyObservedInstanceService("inst-b"), "beta"),
		kv(types.KeyObservedInstanceState("inst-b"), "running"),
	}
	return buildFacts(append(entries, requestFacts...)...)
}

// evictedInstances returns which of inst-a and inst-b the changes stop.
func evictedInstances(changes []Change) map[string]bool {
	evicted := make(map[string]bool)
	for _, instanceID := range []string{"inst-a", "inst-b"} {
		if _, stopped := changeValueByKey(changes)[types.KeyDerivedInstanceControllerStopped(instanceID)]; stopped {
			evicted[instanceID] = true
		}
	}
	return evicted
}

// TestEvictionReadsResourceRequestsWithUnits checks D6 for eviction (T5):
// the effective request wins over the desired one, a request is credited in
// MiB against MiB node facts, and an unreadable request is skipped.
func TestEvictionReadsResourceRequestsWithUnits(t *testing.T) {
	testCases := []struct {
		description  string                  // description names the case
		requestFacts []struct{ k, v string } // requestFacts are the memory request facts
		wantEvicted  map[string]bool         // wantEvicted is the set of stopped instances
	}{
		{description: "effective 512Mi of alpha frees enough, desired 64Mi would not", requestFacts: []struct{ k, v string }{
			kv(types.KeyDesiredServiceResourcesMemory("alpha"), "64Mi"), kv(types.KeyEffectiveServiceResourcesMemory("alpha"), "512Mi"),
			kv(types.KeyDesiredServiceResourcesMemory("beta"), "256Mi")}, wantEvicted: map[string]bool{"inst-a": true}},
		{description: "100Mi each is credited as 100 MiB, so both go", requestFacts: []struct{ k, v string }{
			kv(types.KeyDesiredServiceResourcesMemory("alpha"), "100Mi"), kv(types.KeyDesiredServiceResourcesMemory("beta"), "100Mi")},
			wantEvicted: map[string]bool{"inst-a": true, "inst-b": true}},
		{description: "alpha's unreadable request is skipped", requestFacts: []struct{ k, v string }{
			kv(types.KeyDesiredServiceResourcesMemory("alpha"), "512MB"), kv(types.KeyDesiredServiceResourcesMemory("beta"), "512Mi")},
			wantEvicted: map[string]bool{"inst-b": true}},
	}
	for _, testCase := range testCases {
		changes, reconcileError := NewEvictionController().Reconcile(context.Background(), evictionTestFacts(testCase.requestFacts...))
		if reconcileError != nil {
			t.Fatal(reconcileError)
		}
		if gotEvicted := evictedInstances(changes); len(gotEvicted) != len(testCase.wantEvicted) ||
			gotEvicted["inst-a"] != testCase.wantEvicted["inst-a"] || gotEvicted["inst-b"] != testCase.wantEvicted["inst-b"] {
			t.Errorf("%s: evicted %v, want %v", testCase.description, gotEvicted, testCase.wantEvicted)
		}
	}
}

// TestVerticalAutoscalerDeletesRecommendationWithoutPolicy checks D7: a
// service with no scale/vertical/ facts loses its recommendation, and the
// intent resolver then deletes the effective value so the desired one applies.
func TestVerticalAutoscalerDeletesRecommendationWithoutPolicy(t *testing.T) {
	staleFacts := []store.Fact{
		{Key: types.KeyDesiredService("web"), Value: []byte("")},
		{Key: types.KeyDesiredServiceResourcesMemory("web"), Value: []byte("256Mi")},
		{Key: types.KeyIntentAutoscalerServiceResourcesMemory("web"), Value: []byte("8Gi")},
		{Key: types.KeyEffectiveServiceResourcesMemory("web"), Value: []byte("8Gi")},
	}
	store.SortFacts(staleFacts)
	autoscalerChanges, reconcileError := NewAutoscaleController().Reconcile(context.Background(), staleFacts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	if deletedValue, changed := changeValueByKey(autoscalerChanges)[types.KeyIntentAutoscalerServiceResourcesMemory("web")]; !changed || deletedValue != nil {
		t.Errorf("intent memory change = %q (changed %v), want a delete", deletedValue, changed)
	}

	var withoutIntentFacts []store.Fact
	for _, fact := range staleFacts {
		if fact.Key != types.KeyIntentAutoscalerServiceResourcesMemory("web") {
			withoutIntentFacts = append(withoutIntentFacts, fact)
		}
	}
	intentChanges, intentError := NewIntentResolverController().Reconcile(context.Background(), withoutIntentFacts)
	if intentError != nil {
		t.Fatal(intentError)
	}
	if deletedValue, changed := changeValueByKey(intentChanges)[types.KeyEffectiveServiceResourcesMemory("web")]; !changed || deletedValue != nil {
		t.Errorf("effective memory change = %q (changed %v), want a delete", deletedValue, changed)
	}
}
