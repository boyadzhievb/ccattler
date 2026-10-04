// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestRolloutControllerImplementsInterface(t *testing.T) {
	var _ Controller = NewRolloutController()
}

func TestRolloutControllerName(t *testing.T) {
	rolloutController := NewRolloutController()
	if rolloutController.Name() != "rollout" {
		t.Errorf("name: got %s, want rollout", rolloutController.Name())
	}
}

func TestApplyDisruptionBudgetMinAvailableCapsMaxUnavailable(t *testing.T) {
	// Update policy says max_unavailable=3. Disruption budget says
	// min_available=4 out of 5 desired. That means at most 1 can be
	// unavailable — the budget should cap maxUnavailable to 1.
	originalPolicy := extractedUpdatePolicy{maxUnavailable: 3, maxExtra: 1}
	budget := rolloutDisruptionBudget{minAvailable: 4}
	desiredCount := 5

	effectivePolicy := applyDisruptionBudgetToUpdatePolicy(originalPolicy, budget, desiredCount)

	if effectivePolicy.maxUnavailable != 1 {
		t.Errorf("maxUnavailable: got %d, want 1", effectivePolicy.maxUnavailable)
	}
	if effectivePolicy.maxExtra != 1 {
		t.Errorf("maxExtra should be unchanged: got %d, want 1", effectivePolicy.maxExtra)
	}
}

func TestApplyDisruptionBudgetMaxUnavailableCapsPolicy(t *testing.T) {
	// Update policy says max_unavailable=5. Disruption budget says
	// max_unavailable=2 — the budget should win.
	originalPolicy := extractedUpdatePolicy{maxUnavailable: 5, maxExtra: 1}
	budget := rolloutDisruptionBudget{maxUnavailable: 2}
	desiredCount := 10

	effectivePolicy := applyDisruptionBudgetToUpdatePolicy(originalPolicy, budget, desiredCount)

	if effectivePolicy.maxUnavailable != 2 {
		t.Errorf("maxUnavailable: got %d, want 2", effectivePolicy.maxUnavailable)
	}
}

func TestApplyDisruptionBudgetNoBudgetNoChange(t *testing.T) {
	originalPolicy := extractedUpdatePolicy{maxUnavailable: 3, maxExtra: 2}
	budget := rolloutDisruptionBudget{}
	desiredCount := 10

	effectivePolicy := applyDisruptionBudgetToUpdatePolicy(originalPolicy, budget, desiredCount)

	if effectivePolicy.maxUnavailable != 3 {
		t.Errorf("maxUnavailable should be unchanged: got %d, want 3", effectivePolicy.maxUnavailable)
	}
}

func TestRolloutControllerRespectsDisruptionBudget(t *testing.T) {
	rolloutController := NewRolloutController()

	// Service "web" is rolling from nginx:1.27 → nginx:1.28.
	// 5 desired, update policy max_unavailable=3.
	// Disruption budget: min_available 4 → effective max_unavailable = 1.
	// 3 old-image running, 2 new-image running → should stop at most 1 old.
	inputFacts := []store.Fact{
		{Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx:1.28")},
		{Key: types.KeyDesiredServiceInstances("web"), Value: []byte("5")},
		{Key: types.KeyDesiredServiceUpdateMaxUnavailable("web"), Value: []byte("3")},
		{Key: types.KeyDesiredServiceUpdateMaxExtra("web"), Value: []byte("1")},
		{Key: types.KeyDesiredServiceDisruptionMinAvailable("web"), Value: []byte("4")},
		// 3 old-image instances.
		{Key: types.KeyObservedInstanceState("inst-1"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-1"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-2"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-2"), Value: []byte("nginx:1.27")},
		{Key: types.KeyObservedInstanceState("inst-3"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-3"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-3"), Value: []byte("nginx:1.27")},
		// 2 new-image instances.
		{Key: types.KeyObservedInstanceState("inst-4"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-4"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-4"), Value: []byte("nginx:1.28")},
		{Key: types.KeyObservedInstanceState("inst-5"), Value: []byte("running")},
		{Key: types.KeyObservedInstanceService("inst-5"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceImage("inst-5"), Value: []byte("nginx:1.28")},
		// Rollout already tracked.
		{Key: types.KeyDerivedServiceRolloutImage("web"), Value: []byte("nginx:1.27")},
		{Key: types.KeyDerivedServiceRolloutState("web"), Value: []byte("rolling")},
	}
	store.SortFacts(inputFacts)

	proposedChanges, reconcileError := rolloutController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}

	stoppedCount := 0
	for _, change := range proposedChanges {
		if string(change.Value) == string(types.InstanceStopped) {
			stoppedCount++
		}
	}
	// Budget allows max 1 unavailable (5 desired - 4 min_available = 1).
	if stoppedCount != 1 {
		t.Errorf("expected 1 old instance stopped (disruption budget caps to 1), got %d", stoppedCount)
	}
}
