// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "testing"

// intPointer returns a pointer to value, for explicit update settings.
func intPointer(value int) *int {
	return &value
}

// TestResolveRolloutPacing: design D12, Kubernetes defaults when update {}
// leaves a value unset; explicit values keep their meaning.
func TestResolveRolloutPacing(t *testing.T) {
	testCases := []struct {
		name                   string        // name describes the case.
		expectedCount          int           // expectedCount is the service's expected instance count.
		explicitMaxUnavailable *int          // explicitMaxUnavailable is update { max_unavailable }, nil when unset.
		explicitMaxExtra       *int          // explicitMaxExtra is update { max_extra }, nil when unset.
		wantPacing             RolloutPacing // wantPacing is the effective pace.
	}{
		{name: "1 instance, defaults surge", expectedCount: 1,
			wantPacing: RolloutPacing{MaxUnavailable: 0, MaxExtra: 1, MaxUnavailableIsDefault: true, MaxExtraIsDefault: true}},
		{name: "4 instances, defaults", expectedCount: 4,
			wantPacing: RolloutPacing{MaxUnavailable: 1, MaxExtra: 1, MaxUnavailableIsDefault: true, MaxExtraIsDefault: true}},
		{name: "10 instances, defaults round down and up", expectedCount: 10,
			wantPacing: RolloutPacing{MaxUnavailable: 2, MaxExtra: 3, MaxUnavailableIsDefault: true, MaxExtraIsDefault: true}},
		{name: "explicit max_unavailable 1 on 1 instance keeps its meaning", expectedCount: 1, explicitMaxUnavailable: intPointer(1),
			wantPacing: RolloutPacing{MaxUnavailable: 1, MaxExtra: 1, MaxExtraIsDefault: true}},
		{name: "explicit max_extra 0 and default max_unavailable 0 → 1", expectedCount: 2, explicitMaxExtra: intPointer(0),
			wantPacing: RolloutPacing{MaxUnavailable: 1, MaxExtra: 0, MaxUnavailableIsDefault: true}},
		{name: "both explicit 0 → max_unavailable 1", expectedCount: 4, explicitMaxUnavailable: intPointer(0), explicitMaxExtra: intPointer(0),
			wantPacing: RolloutPacing{MaxUnavailable: 1, MaxExtra: 0}},
		{name: "no instances yet", expectedCount: 0,
			wantPacing: RolloutPacing{MaxUnavailable: 1, MaxExtra: 0, MaxUnavailableIsDefault: true, MaxExtraIsDefault: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			gotPacing := ResolveRolloutPacing(testCase.expectedCount, testCase.explicitMaxUnavailable, testCase.explicitMaxExtra)
			if gotPacing != testCase.wantPacing {
				t.Errorf("ResolveRolloutPacing(%d) = %+v, want %+v", testCase.expectedCount, gotPacing, testCase.wantPacing)
			}
		})
	}
}

// TestRolloutPacingCappedByDisruptionBudget: the disruption budget caps
// max_unavailable, never raises it, and leaves max_extra alone (moved here
// from the rollout controller's applyDisruptionBudgetToUpdatePolicy tests).
func TestRolloutPacingCappedByDisruptionBudget(t *testing.T) {
	testCases := []struct {
		name                 string        // name describes the case.
		pacing               RolloutPacing // pacing is the pace before the budget.
		budgetMinAvailable   int           // budgetMinAvailable is disruption { min_available }, 0 when unset.
		budgetMaxUnavailable int           // budgetMaxUnavailable is disruption { max_unavailable }, 0 when unset.
		desiredCount         int           // desiredCount is the desired instance count.
		wantMaxUnavailable   int           // wantMaxUnavailable is the capped max_unavailable.
	}{
		{name: "min_available 4 of 5 caps 3 to 1", pacing: RolloutPacing{MaxUnavailable: 3, MaxExtra: 1}, budgetMinAvailable: 4, desiredCount: 5, wantMaxUnavailable: 1},
		{name: "max_unavailable 2 caps 5", pacing: RolloutPacing{MaxUnavailable: 5, MaxExtra: 1}, budgetMaxUnavailable: 2, desiredCount: 10, wantMaxUnavailable: 2},
		{name: "no budget, no change", pacing: RolloutPacing{MaxUnavailable: 3, MaxExtra: 2}, desiredCount: 10, wantMaxUnavailable: 3},
		{name: "budget never raises a default 0", pacing: RolloutPacing{MaxUnavailable: 0, MaxExtra: 1}, budgetMinAvailable: 1, desiredCount: 3, wantMaxUnavailable: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cappedPacing := testCase.pacing.CappedByDisruptionBudget(testCase.budgetMinAvailable, testCase.budgetMaxUnavailable, testCase.desiredCount)
			if cappedPacing.MaxUnavailable != testCase.wantMaxUnavailable || cappedPacing.MaxExtra != testCase.pacing.MaxExtra {
				t.Errorf("capped pacing = %+v, want max_unavailable %d and max_extra %d", cappedPacing, testCase.wantMaxUnavailable, testCase.pacing.MaxExtra)
			}
		})
	}
}
