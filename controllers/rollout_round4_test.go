// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestRolloutDisruptionBudgetUsesExpectedCountForPerNode: review round 4
// (M241 2g.2). A per-node service has no desired instance count; its expected
// count is the effective one (the alive nodes). With 3 nodes, min_available 2
// leaves one instance to stop at a time, even with max_unavailable 2.
func TestRolloutDisruptionBudgetUsesExpectedCountForPerNode(t *testing.T) {
	inputFacts := buildRolloutTestFacts("app:b",
		map[string]string{
			types.KeyEffectiveServicePlacementMode("web"):        string(types.PlacementModePerNode),
			types.KeyEffectiveServiceInstances("web"):            "3",
			types.KeyDesiredServiceUpdateMaxUnavailable("web"):   "2",
			types.KeyDesiredServiceDisruptionMinAvailable("web"): "2",
		},
		rolloutTestInstance{instanceID: "web-1", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-2", image: "app:a", state: types.InstanceRunning},
		rolloutTestInstance{instanceID: "web-3", image: "app:a", state: types.InstanceRunning},
	)
	proposedChanges, _ := reconcileRolloutTestFacts(t, inputFacts)
	if stoppedIDs := stoppedInstanceIDs(proposedChanges); len(stoppedIDs) != 1 {
		t.Errorf("stopped %v, want exactly one (3 expected, min_available 2)", stoppedIDs)
	}
}
