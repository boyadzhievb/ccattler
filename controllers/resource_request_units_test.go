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
// recommendation without a unit", "Legacy value outside the bounds" and
// "Effective value without a recommendation" (D9). Bare recommendations, and
// effective values of a resource that has one, get units, clamped to the
// bounds; desired/ is untouched; a second run is a no-op.
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
		// "Effective value without a recommendation": copied from desired by an
		// older intent resolver, so it already means 2 cores and is left alone.
		types.KeyDesiredServiceResourcesCPU("batch"):   "2",
		types.KeyEffectiveServiceResourcesCPU("batch"): "2",
		// solo has a CPU recommendation but none for memory: the check is per
		// resource, so its bare effective memory is left alone too.
		types.KeyIntentAutoscalerServiceResourcesCPU("solo"): "500m",
		types.KeyEffectiveServiceResourcesMemory("solo"):     "2048",
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
		types.KeyEffectiveServiceResourcesCPU("batch"):              "2",
		types.KeyEffectiveServiceResourcesMemory("solo"):            "2048",
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

// buildVerticalMemoryFacts returns web's facts for a memory-only vertical
// policy with the given bounds, desired request, utilization and extra facts.
func buildVerticalMemoryFacts(minimum string, maximum string, request string, memoryPercent string, extraFacts ...store.Fact) []store.Fact {
	facts := []store.Fact{
		{Key: types.KeyDesiredService("web"), Value: []byte("")},
		{Key: types.KeyDesiredServiceResourcesMemory("web"), Value: []byte(request)},
		{Key: types.KeyDesiredServiceScaleVerticalMemoryMin("web"), Value: []byte(minimum)},
		{Key: types.KeyDesiredServiceScaleVerticalMemoryMax("web"), Value: []byte(maximum)},
		{Key: types.KeyObservedMetric("web", "memory"), Value: []byte(memoryPercent)},
	}
	facts = append(facts, extraFacts...)
	store.SortFacts(facts)
	return facts
}

// reconcileVerticalMemoryIntent runs one autoscaler reconcile at atTime and
// returns the proposed memory intent value and whether one was proposed.
func reconcileVerticalMemoryIntent(t *testing.T, autoscaleController *AutoscaleController, atTime time.Time, facts []store.Fact) (string, bool) {
	t.Helper()
	autoscaleController.SetTimeNow(func() time.Time { return atTime })
	changes, reconcileError := autoscaleController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	memoryIntent, proposed := changeValueByKey(changes)[types.KeyIntentAutoscalerServiceResourcesMemory("web")]
	return string(memoryIntent), proposed
}

// TestVerticalMemoryRecommendationSettlesOnWholeMiB: spec resource-requests,
// "Memory bounds that are not whole MiB" (D7, review finding 1). min 100M is
// 96Mi rounded up and written once; the next reconcile, reading 96Mi back,
// writes nothing. max 1G caps at 953Mi (rounded down). A small request whose
// recommendation stays in the same MiB is not written either.
func TestVerticalMemoryRecommendationSettlesOnWholeMiB(t *testing.T) {
	lowUsageController := NewAutoscaleController()
	firstIntent, _ := reconcileVerticalMemoryIntent(t, lowUsageController, verticalTestBaseTime,
		buildVerticalMemoryFacts("100M", "1G", "128Mi", "10"))
	if firstIntent != "96Mi" {
		t.Fatalf("first low-usage recommendation = %q, want 96Mi", firstIntent)
	}
	settledFacts := buildVerticalMemoryFacts("100M", "1G", "128Mi", "10",
		store.Fact{Key: types.KeyIntentAutoscalerServiceResourcesMemory("web"), Value: []byte("96Mi")},
		store.Fact{Key: types.KeyEffectiveServiceResourcesMemory("web"), Value: []byte("96Mi")})
	if secondIntent, proposed := reconcileVerticalMemoryIntent(t, lowUsageController,
		verticalTestBaseTime.Add(time.Minute), settledFacts); proposed {
		t.Errorf("second reconcile proposed memory %q again, want no write", secondIntent)
	}

	highUsageIntent, _ := reconcileVerticalMemoryIntent(t, NewAutoscaleController(), verticalTestBaseTime,
		buildVerticalMemoryFacts("100M", "1G", "900Mi", "200"))
	if highUsageIntent != "953Mi" {
		t.Errorf("high-usage recommendation = %q, want 953Mi (1G rounded down)", highUsageIntent)
	}

	if smallIntent, proposed := reconcileVerticalMemoryIntent(t, NewAutoscaleController(), verticalTestBaseTime,
		buildVerticalMemoryFacts("512Ki", "1G", "1Mi", "60")); proposed {
		t.Errorf("1Mi request at 60%% proposed %q, want no write (the recommendation is still 1Mi)", smallIntent)
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
// MiB against MiB node facts, and an unreadable request is credited 0 MiB.
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
		// Spec "Still evictable" (D6, decision A): alpha stays a candidate in normal
		// order with 0 MiB credit, so beta is evicted too in the same cycle.
		{description: "alpha's unreadable request is evicted with 0 MiB credit, then beta", requestFacts: []struct{ k, v string }{
			kv(types.KeyDesiredServiceResourcesMemory("alpha"), "512MB"), kv(types.KeyDesiredServiceResourcesMemory("beta"), "512Mi")},
			wantEvicted: map[string]bool{"inst-a": true, "inst-b": true}},
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

// TestVerticalResourceSpecsCoverEveryKind: review R5-N3. Every kind in
// types.ResourceKindSpecs has exactly one vertical spec, with its own key
// builders, so a new kind cannot be sized without one.
func TestVerticalResourceSpecsCoverEveryKind(t *testing.T) {
	if len(verticalResourceSpecs) != len(types.ResourceKindSpecs()) {
		t.Errorf("%d vertical specs for %d resource kinds", len(verticalResourceSpecs), len(types.ResourceKindSpecs()))
	}
	for _, kindSpec := range types.ResourceKindSpecs() {
		verticalSpec := lookupVerticalResourceSpec(kindSpec.Kind)
		if verticalSpec.Kind != kindSpec.Kind || verticalSpec.IntentKey("web") != kindSpec.IntentKey("web") {
			t.Errorf("vertical spec for %s does not carry the kind's storage spec", kindSpec.Kind)
		}
		if verticalSpec.toVerticalUnit == nil || verticalSpec.boundsInVerticalUnit == nil || verticalSpec.formatVerticalUnit == nil {
			t.Errorf("vertical spec for %s has an unset function", kindSpec.Kind)
		}
	}
}

// TestBoundsInVerticalUnitMaximumBelowOneUnit: review R5-N2. The exact branch
// of the migration keys on boundsInVerticalUnit(bounds).maximum == 0: true for
// a memory max under 1 MiB, never for a positive CPU max (millicores are the
// parsed unit).
func TestBoundsInVerticalUnitMaximumBelowOneUnit(t *testing.T) {
	memorySpec := lookupVerticalResourceSpec(types.ResourceKindMemory)
	if maximumInUnit := memorySpec.boundsInVerticalUnit(verticalResourceBounds{maximum: 512 << 10}).maximum; maximumInUnit != 0 {
		t.Errorf("memory max 512Ki in MiB = %d, want 0 (below one sizing unit)", maximumInUnit)
	}
	if maximumInUnit := memorySpec.boundsInVerticalUnit(verticalResourceBounds{maximum: 2 << 30}).maximum; maximumInUnit != 2048 {
		t.Errorf("memory max 2Gi in MiB = %d, want 2048", maximumInUnit)
	}
	cpuSpec := lookupVerticalResourceSpec(types.ResourceKindCPU)
	if maximumInUnit := cpuSpec.boundsInVerticalUnit(verticalResourceBounds{maximum: 1}).maximum; maximumInUnit != 1 {
		t.Errorf("cpu max 1m in millicores = %d, want 1", maximumInUnit)
	}
}
