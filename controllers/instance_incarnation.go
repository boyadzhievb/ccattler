// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// staleIncarnationFieldKeys maps the observed instance fields that belong to
// one incarnation of an instance ID to their keys; a new incarnation of the
// same ID (a recreated stateful ordinal) must not inherit them.
var staleIncarnationFieldKeys = map[string]func(string) string{
	"restarts":   types.KeyObservedInstanceRestarts,  // restart count (M241 D2)
	"created-at": types.KeyObservedInstanceCreatedAt, // creation time, compared with rollout/started_at (D13)
	"started_at": types.KeyObservedInstanceStartedAt, // last start, the min_healthy_time window (D10)
	"last_exit":  types.KeyObservedInstanceLastExit,  // last exit, the rollback reason
}

// probeFieldPrefix is the prefix of an instance's observed probe results in
// its merged fields (probe/{type}).
const probeFieldPrefix = "probe/"

// buildInstanceIncarnationChanges returns the changes, in groupID, that start a
// new incarnation of instanceID: the recorded image when desiredImage is known
// (M241 D1; none: the agent falls back to the service image) and deletes of
// the previous incarnation's per-start facts and probe results (D2, D13, B4).
// Shared by the instance controller's creations and the failure controller's
// replacements; previousFields are the ID's current fields (nil for a new ID).
func buildInstanceIncarnationChanges(groupID string, instanceID string, desiredImage string, previousFields map[string]string) []Change {
	var changes []Change
	if desiredImage != "" {
		changes = append(changes, Change{
			Type: store.OpPut, Key: types.KeyDerivedInstanceImage(instanceID), Value: []byte(desiredImage), Group: groupID,
		})
	}
	staleKeys := make([]string, 0, len(previousFields))
	for fieldName := range previousFields {
		if keyFunction, isStale := staleIncarnationFieldKeys[fieldName]; isStale {
			staleKeys = append(staleKeys, keyFunction(instanceID))
		} else if probeType, isProbe := strings.CutPrefix(fieldName, probeFieldPrefix); isProbe {
			staleKeys = append(staleKeys, types.KeyObservedInstanceProbeState(instanceID, types.ProbeType(probeType)))
		}
	}
	sort.Strings(staleKeys)
	for _, staleKey := range staleKeys {
		changes = append(changes, Change{Type: store.OpDelete, Key: staleKey, Group: groupID})
	}
	return changes
}
