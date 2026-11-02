// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// writeSharedVolumeFixture declares a shared volume with the given state and
// attachments (node → instance), and registers each listed node with state.
func writeSharedVolumeFixture(t *testing.T, stateStore store.StateStore, volumeState types.VolumeState, attachments map[string]string, nodeStates map[string]types.NodeState) {
	t.Helper()
	ctx := context.Background()
	types.WriteDesiredVolume(ctx, stateStore, "shared-data", "1Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{Name: "shared-data", Size: "1Gi", State: volumeState})
	stateStore.Put(ctx, types.KeyObservedVolumeAccess("shared-data"), []byte(types.VolumeAccessShared))
	for nodeID, instanceID := range attachments {
		stateStore.Put(ctx, types.KeyObservedVolumeAttachment("shared-data", nodeID, instanceID), []byte("/mnt/nfs/shared-data"))
	}
	for nodeID, nodeState := range nodeStates {
		types.WriteNode(ctx, stateStore, types.Node{ID: nodeID, State: nodeState, CapacityCPU: 4000, CapacityMemory: 8192})
	}
}

// TestStorageControllerSharedVolumeStateFromAttachments verifies the
// controller derives attached/available for a shared volume from its
// attachment facts and leaves a matching state alone.
func TestStorageControllerSharedVolumeStateFromAttachments(t *testing.T) {
	testCases := []struct {
		name          string
		observedState types.VolumeState
		attachments   map[string]string
		expectedState types.VolumeState // expectedState is "" when no state change is expected.
	}{
		{"first attachment marks attached", types.VolumeAvailable, map[string]string{"node-1": "inst-1"}, types.VolumeAttached},
		{"no attachments marks available", types.VolumeAttached, nil, types.VolumeAvailable},
		{"attached with attachments is stable", types.VolumeAttached, map[string]string{"node-1": "inst-1", "node-2": "inst-2"}, ""},
		{"available without attachments is stable", types.VolumeAvailable, nil, ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stateStore := store.NewMemoryStore()
			defer stateStore.Close()
			writeSharedVolumeFixture(t, stateStore, testCase.observedState, testCase.attachments,
				map[string]types.NodeState{"node-1": types.NodeAlive, "node-2": types.NodeAlive})

			stateChange, hasStateChange := changesByKey(helperCollectStorageControllerChanges(t, stateStore))[types.KeyObservedVolumeState("shared-data")]
			if testCase.expectedState == "" {
				if hasStateChange {
					t.Errorf("unexpected state change to %s", stateChange.Value)
				}
				return
			}
			if !hasStateChange || string(stateChange.Value) != string(testCase.expectedState) {
				t.Errorf("state change = %q, want %s", stateChange.Value, testCase.expectedState)
			}
		})
	}
}

// TestStorageControllerSharedVolumeDropsUnreachableNodeAttachment verifies a
// shared volume loses only the unreachable node's attachment, stays attached
// for the live node, and is never migrated or snapshotted.
func TestStorageControllerSharedVolumeDropsUnreachableNodeAttachment(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	writeSharedVolumeFixture(t, stateStore, types.VolumeAttached,
		map[string]string{"node-1": "inst-1", "node-2": "inst-2"},
		map[string]types.NodeState{"node-1": types.NodeUnreachable, "node-2": types.NodeAlive})

	changeMap := changesByKey(helperCollectStorageControllerChanges(t, stateStore))

	if changeMap[types.KeyObservedVolumeAttachment("shared-data", "node-1", "inst-1")].Type != store.OpDelete {
		t.Error("expected unreachable node-1 attachment deleted")
	}
	if _, touched := changeMap[types.KeyObservedVolumeAttachment("shared-data", "node-2", "inst-2")]; touched {
		t.Error("live node-2 attachment must not change")
	}
	if _, touched := changeMap[types.KeyObservedVolumeState("shared-data")]; touched {
		t.Error("state must stay attached while node-2 holds the volume")
	}
	if _, migrated := changeMap[types.KeyObservedVolumeMigrationSource("shared-data")]; migrated {
		t.Error("shared volume must not be migrated")
	}
}

// TestStorageControllerSharedVolumeLastNodeUnreachable verifies that when the
// only attached node is unreachable the attachment is dropped and the volume
// becomes available in the same reconcile.
func TestStorageControllerSharedVolumeLastNodeUnreachable(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	writeSharedVolumeFixture(t, stateStore, types.VolumeAttached,
		map[string]string{"node-1": "inst-1"}, map[string]types.NodeState{"node-1": types.NodeUnreachable})

	changeMap := changesByKey(helperCollectStorageControllerChanges(t, stateStore))

	if changeMap[types.KeyObservedVolumeAttachment("shared-data", "node-1", "inst-1")].Type != store.OpDelete {
		t.Error("expected node-1 attachment deleted")
	}
	if string(changeMap[types.KeyObservedVolumeState("shared-data")].Value) != string(types.VolumeAvailable) {
		t.Error("expected state available once no live attachment remains")
	}
}

// TestStorageControllerSharedVolumeCleanup verifies removing a shared volume
// deletes its access marker and every attachment fact.
func TestStorageControllerSharedVolumeCleanup(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	writeSharedVolumeFixture(t, stateStore, types.VolumeAttached,
		map[string]string{"node-1": "inst-1", "node-2": "inst-2"},
		map[string]types.NodeState{"node-1": types.NodeAlive, "node-2": types.NodeAlive})
	stateStore.Delete(context.Background(), types.KeyDesiredVolume("shared-data"))
	stateStore.Delete(context.Background(), types.KeyDesiredVolumeSize("shared-data"))
	stateStore.Delete(context.Background(), types.KeyDesiredVolumePersistent("shared-data"))

	changeMap := changesByKey(helperCollectStorageControllerChanges(t, stateStore))

	for _, expectedDeletedKey := range []string{
		types.KeyObservedVolumeAccess("shared-data"),
		types.KeyObservedVolumeAttachment("shared-data", "node-1", "inst-1"),
		types.KeyObservedVolumeAttachment("shared-data", "node-2", "inst-2"),
	} {
		if changeMap[expectedDeletedKey].Type != store.OpDelete {
			t.Errorf("expected %s deleted", expectedDeletedKey)
		}
	}
	if string(changeMap[types.KeyObservedVolumeState("shared-data")].Value) == string(types.VolumeAvailable) {
		t.Error("cleanup must not also write a derived state")
	}
}
