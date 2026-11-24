// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"slices"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestGCInstanceKeysIncludeNetworkAllocation: M267 review round 5 B3. The
// on-demand GC deletes network/allocation/{id} with the instance tree.
func TestGCInstanceKeysIncludeNetworkAllocation(t *testing.T) {
	keysToDelete := collectInstanceFactKeys("old-abc", nil, nil, map[string]string{"old-abc": "node02"})
	if !slices.Contains(keysToDelete, types.KeyNetworkAllocation("old-abc")) {
		t.Errorf("collectInstanceFactKeys %v lacks %s", keysToDelete, types.KeyNetworkAllocation("old-abc"))
	}
}
