// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

// RolloutDefaultMaxUnavailablePercent is the share of the expected instances a
// rollout may take down when update { max_unavailable } is unset, rounded down
// (Kubernetes' maxUnavailable default, M241 D12).
const RolloutDefaultMaxUnavailablePercent = 25

// RolloutDefaultMaxExtraPercent is the share of the expected instances a
// rollout may create beyond them when update { max_extra } is unset, rounded
// up (Kubernetes' maxSurge default, M241 D12).
const RolloutDefaultMaxExtraPercent = 25

// percentDenominator turns a percentage into a fraction.
const percentDenominator = 100

// rolloutMinimumPace is the max_unavailable a rollout gets when both values
// end up 0, which would let it neither stop nor create an instance.
const rolloutMinimumPace = 1

// RolloutPacing is a service's effective rollout pace: how many instances a
// rollout may take down and create beyond the expected count (M241 D12).
type RolloutPacing struct {
	MaxUnavailable          int  // MaxUnavailable is how many expected instances may be not healthy at once.
	MaxExtra                int  // MaxExtra is how many instances may run beyond the expected count.
	MaxUnavailableIsDefault bool // MaxUnavailableIsDefault is true when update { max_unavailable } is unset.
	MaxExtraIsDefault       bool // MaxExtraIsDefault is true when update { max_extra } is unset.
}

// ResolveRolloutPacing returns the effective pace for expectedCount instances.
// A nil explicit value takes its default (25 % down rounded down, 25 % up
// rounded up). If both end up 0, max_unavailable is 1, or nothing could move.
func ResolveRolloutPacing(expectedCount int, explicitMaxUnavailable *int, explicitMaxExtra *int) RolloutPacing {
	expectedCount = max(expectedCount, 0)
	pacing := RolloutPacing{
		MaxUnavailable:          expectedCount * RolloutDefaultMaxUnavailablePercent / percentDenominator,
		MaxExtra:                (expectedCount*RolloutDefaultMaxExtraPercent + percentDenominator - 1) / percentDenominator,
		MaxUnavailableIsDefault: explicitMaxUnavailable == nil,
		MaxExtraIsDefault:       explicitMaxExtra == nil,
	}
	if explicitMaxUnavailable != nil {
		pacing.MaxUnavailable = *explicitMaxUnavailable
	}
	if explicitMaxExtra != nil {
		pacing.MaxExtra = *explicitMaxExtra
	}
	if pacing.MaxUnavailable <= 0 && pacing.MaxExtra <= 0 {
		pacing.MaxUnavailable = rolloutMinimumPace
	}
	return pacing
}

// CappedByDisruptionBudget lowers max_unavailable to the service's disruption
// budget: its max_unavailable, and desiredCount − min_available (at least 1).
// Zero budget values are unset and change nothing.
func (pacing RolloutPacing) CappedByDisruptionBudget(budgetMinAvailable int, budgetMaxUnavailable int, desiredCount int) RolloutPacing {
	if budgetMaxUnavailable > 0 {
		pacing.MaxUnavailable = min(pacing.MaxUnavailable, budgetMaxUnavailable)
	}
	if budgetMinAvailable > 0 && desiredCount > 0 {
		pacing.MaxUnavailable = min(pacing.MaxUnavailable, max(desiredCount-budgetMinAvailable, rolloutMinimumPace))
	}
	return pacing
}
