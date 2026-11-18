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

// PlacementMode is a service's placement mode (placement { mode … }); the
// empty mode is the default placement.
type PlacementMode string

// PlacementModePerNode is the placement mode of a service that runs one
// instance on every alive node (placement { mode per-node }).
const PlacementModePerNode PlacementMode = "per-node"

// WorkloadKind is how a service's instances are laid out, which decides its
// rollout defaults (M241 D14).
type WorkloadKind string

// Workload kinds.
const (
	// WorkloadStateless is a service with interchangeable, randomly named instances.
	WorkloadStateless WorkloadKind = "stateless"
	// WorkloadStateful is a service with ordinal instance IDs (stateful true).
	WorkloadStateful WorkloadKind = "stateful"
	// WorkloadPerNode is a service with one instance per node (placement mode per-node).
	WorkloadPerNode WorkloadKind = "per-node"
)

// RolloutPacing is a service's effective rollout pace: how many instances a
// rollout may take down and create beyond the expected count (M241 D12).
type RolloutPacing struct {
	MaxUnavailable          int  // MaxUnavailable is how many expected instances may be not healthy at once.
	MaxExtra                int  // MaxExtra is how many instances may run beyond the expected count.
	MaxUnavailableIsDefault bool // MaxUnavailableIsDefault is true when update { max_unavailable } is unset.
	MaxExtraIsDefault       bool // MaxExtraIsDefault is true when update { max_extra } is unset.
	MaxUnavailableForced    bool // MaxUnavailableForced is true when max_unavailable was raised to 1 so the rollout can move.
	MaxExtraIgnored         bool // MaxExtraIgnored is true when an explicit max_extra is ignored (stateful, per-node; D14).
}

// replaceInPlaceMaxUnavailable is the default max_unavailable of stateful and
// per-node services, which replace one instance at a time and never surge
// (StatefulSet and DaemonSet defaults, M241 D14).
const replaceInPlaceMaxUnavailable = 1

// ResolveRolloutPacing returns the effective pace for expectedCount instances
// of the given kind. Stateless: a nil value takes its default (25 % down
// rounded down, 25 % up rounded up). Stateful and per-node: max_unavailable 1
// by default and never a surge, an explicit max_extra is ignored (D14). If both
// end up 0, max_unavailable is 1, or nothing could move.
func ResolveRolloutPacing(workloadKind WorkloadKind, expectedCount int, explicitMaxUnavailable *int, explicitMaxExtra *int) RolloutPacing {
	expectedCount = max(expectedCount, 0)
	pacing := RolloutPacing{
		MaxUnavailable:          expectedCount * RolloutDefaultMaxUnavailablePercent / percentDenominator,
		MaxExtra:                (expectedCount*RolloutDefaultMaxExtraPercent + percentDenominator - 1) / percentDenominator,
		MaxUnavailableIsDefault: explicitMaxUnavailable == nil,
		MaxExtraIsDefault:       explicitMaxExtra == nil,
	}
	if workloadKind == WorkloadStateful || workloadKind == WorkloadPerNode {
		pacing.MaxUnavailable = replaceInPlaceMaxUnavailable
		pacing.MaxExtra = 0
		pacing.MaxExtraIgnored = explicitMaxExtra != nil && *explicitMaxExtra != 0
		explicitMaxExtra = nil
	}
	if explicitMaxUnavailable != nil {
		pacing.MaxUnavailable = *explicitMaxUnavailable
	}
	if explicitMaxExtra != nil {
		pacing.MaxExtra = *explicitMaxExtra
	}
	if pacing.MaxUnavailable <= 0 && pacing.MaxExtra <= 0 {
		pacing.MaxUnavailable = rolloutMinimumPace
		pacing.MaxUnavailableForced = true
	}
	return pacing
}

// WorkloadKindOf returns the kind of a service from its effective stateful
// flag and placement mode; stateful wins when both are set.
func WorkloadKindOf(isStateful bool, placementMode PlacementMode) WorkloadKind {
	switch {
	case isStateful:
		return WorkloadStateful
	case placementMode == PlacementModePerNode:
		return WorkloadPerNode
	default:
		return WorkloadStateless
	}
}

// CappedByDisruptionBudget lowers max_unavailable to the service's disruption
// budget: its max_unavailable, and expectedCount − min_available (at least 1),
// expectedCount being the effective count (a per-node service has no desired
// one, M241 2g.2). Zero budget values are unset and change nothing.
func (pacing RolloutPacing) CappedByDisruptionBudget(budgetMinAvailable int, budgetMaxUnavailable int, expectedCount int) RolloutPacing {
	if budgetMaxUnavailable > 0 {
		pacing.MaxUnavailable = min(pacing.MaxUnavailable, budgetMaxUnavailable)
	}
	if budgetMinAvailable > 0 && expectedCount > 0 {
		pacing.MaxUnavailable = min(pacing.MaxUnavailable, max(expectedCount-budgetMinAvailable, rolloutMinimumPace))
	}
	return pacing
}
