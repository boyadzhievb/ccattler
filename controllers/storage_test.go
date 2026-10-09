// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperCollectStorageControllerChanges writes facts to a store, scans all
// watched prefixes, and runs Reconcile. Returns the proposed changes.
func helperCollectStorageControllerChanges(t *testing.T, stateStore store.StateStore) []Change {
	t.Helper()
	storageController := NewStorageController(stateStore)
	ctx := context.Background()

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, err := stateStore.Scan(ctx, prefix)
		if err != nil {
			t.Fatal(err)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)

	changes, err := storageController.Reconcile(ctx, allFacts)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// TestStorageControllerCreatesVolumeFromDesired verifies that a desired
// volume with no observed counterpart produces changes to create it as
// available.
func TestStorageControllerCreatesVolumeFromDesired(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)

	changes := helperCollectStorageControllerChanges(t, stateStore)

	changeMap := changesByKey(changes)
	if string(changeMap[types.KeyObservedVolumeState("pgdata")].Value) != string(types.VolumeAvailable) {
		t.Error("expected observed volume state = available")
	}
	if string(changeMap[types.KeyObservedVolumeSize("pgdata")].Value) != "100Gi" {
		t.Error("expected observed volume size = 100Gi")
	}
}

// TestStorageControllerDoesNotDuplicateExistingVolume verifies that if an
// observed volume already exists, no creation changes are produced.
func TestStorageControllerDoesNotDuplicateExistingVolume(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeState("pgdata") && change.Type == store.OpPut {
			t.Error("should not re-create an already-observed volume")
		}
	}
}

// TestStorageControllerForceDetachOnUnreachableNode verifies that a volume
// attached to an unreachable node is force-detached (set to available).
func TestStorageControllerForceDetachOnUnreachableNode(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa", MountPath: "/mnt/volumes/pgdata",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	changeMap := changesByKey(changes)
	if string(changeMap[types.KeyObservedVolumeState("pgdata")].Value) != string(types.VolumeMigrating) {
		t.Error("expected volume state changed to migrating after force-detach")
	}
	if string(changeMap[types.KeyObservedVolumeMigrationSource("pgdata")].Value) != "node-1" {
		t.Error("expected migration source set to original node")
	}
	if changeMap[types.KeyObservedVolumeNode("pgdata")].Type != store.OpDelete {
		t.Error("expected volume node fact deleted")
	}
	if changeMap[types.KeyObservedVolumeInstance("pgdata")].Type != store.OpDelete {
		t.Error("expected volume instance fact deleted")
	}
}

// TestStorageControllerNoForceDetachOnAliveNode verifies that volumes attached
// to alive nodes are not force-detached.
func TestStorageControllerNoForceDetachOnAliveNode(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeState("pgdata") {
			t.Error("should not change volume state on alive node")
		}
	}
}

// TestStorageControllerCleansUpRemovedVolume verifies that observed volume
// facts are deleted when the desired volume is removed.
func TestStorageControllerCleansUpRemovedVolume(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	deletedKeys := make(map[string]bool)
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deletedKeys[change.Key] = true
		}
	}
	if !deletedKeys[types.KeyObservedVolume("pgdata")] {
		t.Error("expected observed volume marker deleted")
	}
	if !deletedKeys[types.KeyObservedVolumeState("pgdata")] {
		t.Error("expected observed volume state deleted")
	}
}

// TestStorageControllerIdempotent verifies that running reconcile twice on
// the same state produces the same result.
func TestStorageControllerIdempotent(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for stable state, got %d", len(changes))
	}
}

// TestStorageControllerMultipleVolumesIndependent verifies that two volumes
// are handled independently.
func TestStorageControllerMultipleVolumesIndependent(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteDesiredVolume(ctx, stateStore, "cache", "10Gi", false)

	changes := helperCollectStorageControllerChanges(t, stateStore)

	createdVolumes := make(map[string]bool)
	for _, change := range changes {
		if change.Type == store.OpPut && change.Key == types.KeyObservedVolumeState("pgdata") {
			createdVolumes["pgdata"] = true
		}
		if change.Type == store.OpPut && change.Key == types.KeyObservedVolumeState("cache") {
			createdVolumes["cache"] = true
		}
	}
	if !createdVolumes["pgdata"] || !createdVolumes["cache"] {
		t.Errorf("expected both volumes created, got pgdata=%v cache=%v",
			createdVolumes["pgdata"], createdVolumes["cache"])
	}
}

// TestStorageControllerWatchPrefixes verifies the watch prefixes include
// desired volumes, observed volumes, observed nodes, and derived volumes.
func TestStorageControllerWatchPrefixes(t *testing.T) {
	storageController := NewStorageController(store.NewMemoryStore())
	watchPrefixes := storageController.Watch()

	expectedPrefixes := map[string]bool{
		types.ScanDesiredVolumes:  false,
		types.ScanObservedVolumes: false,
		types.ScanObservedNodes:   false,
		types.ScanDerivedVolumes:  false,
	}
	for _, prefix := range watchPrefixes {
		expectedPrefixes[prefix] = true
	}
	for prefix, found := range expectedPrefixes {
		if !found {
			t.Errorf("missing watch prefix: %s", prefix)
		}
	}
}

// TestStorageControllerName verifies the controller name.
func TestStorageControllerName(t *testing.T) {
	storageController := NewStorageController(store.NewMemoryStore())
	if storageController.Name() != "storage" {
		t.Errorf("name: got %s, want storage", storageController.Name())
	}
}

// TestStorageControllerMigrationIdempotent verifies that a volume already in
// VolumeMigrating state is not re-processed by the force-detach logic.
func TestStorageControllerMigrationIdempotent(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name:            "pgdata",
		Size:            "100Gi",
		State:           types.VolumeMigrating,
		MigrationSource: "node-1",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeState("pgdata") {
			t.Error("should not re-process a volume already in migrating state")
		}
	}
}

// TestStorageControllerMigrationCompletesOnReattach verifies that once the agent
// reattaches a migrating volume (setting state to attached), subsequent
// reconciles produce no migration-related changes.
func TestStorageControllerMigrationCompletesOnReattach(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name:     "pgdata",
		Size:     "100Gi",
		State:    types.VolumeAttached,
		Node:     "node-2",
		Instance: "bbb",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-2", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeState("pgdata") {
			t.Error("should not change state of volume reattached on alive node")
		}
	}
}

// TestStorageControllerSnapshotBeforeMigration verifies that when a storage
// provider is configured, the controller takes a snapshot before migrating a
// volume from an unreachable node, and records the snapshot name in the store.
func TestStorageControllerSnapshotBeforeMigration(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)

	storageController := NewStorageController(stateStore)
	storageController.SetStorageProvider(simulatorStorageProvider)

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa", MountPath: "/mnt/volumes/pgdata",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, err := stateStore.Scan(ctx, prefix)
		if err != nil {
			t.Fatal(err)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)

	changes, err := storageController.Reconcile(ctx, allFacts)
	if err != nil {
		t.Fatal(err)
	}

	// Reconcile no longer calls the provider directly — it emits a
	// pending-operation fact. Verify the pending op is in the changes.
	changeMap := changesByKey(changes)
	pendingChange, hasPending := changeMap[types.KeyDerivedVolumePendingSnapshot("pgdata")]
	if !hasPending {
		t.Fatal("expected pending_operation change for snapshot")
	}

	// Simulate the runner committing changes to the store.
	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			if _, putErr := stateStore.Put(ctx, change.Key, change.Value); putErr != nil {
				t.Fatal(putErr)
			}
		case store.OpDelete:
			_ = stateStore.Delete(ctx, change.Key)
		}
	}

	// Execute post-commit operations — this is where the actual snapshot happens.
	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}

	snapshots := simulatorStorageProvider.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot after post-commit, got %d", len(snapshots))
	}
	if !strings.HasPrefix(snapshots[0], "pgdata-pre-migration-") {
		t.Errorf("snapshot name %q does not match expected prefix", snapshots[0])
	}

	// Verify the snapshot name was written to observed state.
	snapshotFact, getErr := stateStore.Get(ctx, types.KeyObservedVolumeLastSnapshot("pgdata"))
	if getErr != nil {
		t.Fatal("expected last_snapshot fact after post-commit execution")
	}
	if string(snapshotFact.Value) != snapshots[0] {
		t.Errorf("snapshot fact = %s, want %s", snapshotFact.Value, snapshots[0])
	}

	// Verify pending op was cleaned up and last-operation was written.
	if _, getErr := stateStore.Get(ctx, pendingChange.Key); getErr == nil {
		t.Error("pending_operation should be deleted after successful execution")
	}

	// Idempotency: running executor again should not create another snapshot.
	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}
	if len(simulatorStorageProvider.Snapshots()) != 1 {
		t.Errorf("expected still 1 snapshot after idempotent re-run, got %d", len(simulatorStorageProvider.Snapshots()))
	}
}

// TestStorageControllerNoSnapshotWithoutProvider verifies that migration works
// without a storage provider — no snapshot is taken but migration still proceeds.
func TestStorageControllerNoSnapshotWithoutProvider(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	changeMap := changesByKey(changes)
	if string(changeMap[types.KeyObservedVolumeState("pgdata")].Value) != string(types.VolumeMigrating) {
		t.Error("expected volume state migrating even without provider")
	}
	if _, hasSnapshot := changeMap[types.KeyObservedVolumeLastSnapshot("pgdata")]; hasSnapshot {
		t.Error("should not record snapshot when no provider is set")
	}
}

// TestStorageControllerResizesVolume verifies that when the desired size differs
// from the observed size, the controller emits a pending resize operation and
// an observed-size change. The actual ResizeVolume call happens post-commit.
func TestStorageControllerResizesVolume(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	storageController := NewStorageController(stateStore)
	storageController.SetStorageProvider(simulatorStorageProvider)

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "200Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, err := stateStore.Scan(ctx, prefix)
		if err != nil {
			t.Fatal(err)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)

	changes, err := storageController.Reconcile(ctx, allFacts)
	if err != nil {
		t.Fatal(err)
	}

	changeMap := changesByKey(changes)

	// With a provider configured, the observed size is NOT updated during
	// Reconcile — it is deferred to post-commit after the provider call.
	if _, hasSizeChange := changeMap[types.KeyObservedVolumeSize("pgdata")]; hasSizeChange {
		t.Error("observed size should NOT be updated during Reconcile when provider is set")
	}

	if _, hasPending := changeMap[types.KeyDerivedVolumePendingResize("pgdata")]; !hasPending {
		t.Fatal("expected pending_resize change for resize")
	}

	// Commit changes, then execute post-commit.
	for _, change := range changes {
		if change.Type == store.OpPut {
			if _, putErr := stateStore.Put(ctx, change.Key, change.Value); putErr != nil {
				t.Fatal(putErr)
			}
		}
	}
	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}

	// Verify the provider was actually called via the reported capacity.
	_, capacityBytes, _ := simulatorStorageProvider.VolumeUsage(ctx, "pgdata")
	if capacityBytes != 200*1024*1024*1024 {
		t.Errorf("provider volume capacity = %d, want %d", capacityBytes, 200*1024*1024*1024)
	}

	// Verify the observed size was updated by the post-commit executor.
	sizeFact, getErr := stateStore.Get(ctx, types.KeyObservedVolumeSize("pgdata"))
	if getErr != nil {
		t.Fatal("expected observed size updated after post-commit")
	}
	if string(sizeFact.Value) != "200Gi" {
		t.Errorf("observed size after post-commit = %s, want 200Gi", sizeFact.Value)
	}
}

// TestStorageControllerResizeNoOpWhenSameSize verifies that no resize changes
// are emitted when desired and observed sizes match.
func TestStorageControllerResizeNoOpWhenSameSize(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeSize("pgdata") {
			t.Error("should not emit size change when sizes match")
		}
	}
}

// TestStorageControllerReplicationInitialized verifies that when desired
// replicas > 1, the controller writes replica_count and sets state to syncing.
func TestStorageControllerReplicationInitialized(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteDesiredVolumeReplicas(ctx, stateStore, "pgdata", 3)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	changeMap := changesByKey(changes)
	replicaCountChange, hasCount := changeMap[types.KeyObservedVolumeReplicaCount("pgdata")]
	if !hasCount {
		t.Fatal("expected replica_count change")
	}
	if string(replicaCountChange.Value) != "3" {
		t.Errorf("replica_count = %s, want 3", replicaCountChange.Value)
	}

	replicaStateChange, hasState := changeMap[types.KeyObservedVolumeReplicaState("pgdata")]
	if !hasState {
		t.Fatal("expected replica_state change")
	}
	if string(replicaStateChange.Value) != string(types.ReplicaSyncing) {
		t.Errorf("replica_state = %s, want syncing", replicaStateChange.Value)
	}
}

// TestStorageControllerReplicationNoOpWhenSynced verifies that no replication
// changes are emitted when the volume is already synced at the desired count.
func TestStorageControllerReplicationNoOpWhenSynced(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteDesiredVolumeReplicas(ctx, stateStore, "pgdata", 3)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name:         "pgdata",
		Size:         "100Gi",
		State:        types.VolumeAvailable,
		ReplicaCount: 3,
		ReplicaState: types.ReplicaSynced,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	for _, change := range changes {
		if change.Key == types.KeyObservedVolumeReplicaCount("pgdata") ||
			change.Key == types.KeyObservedVolumeReplicaState("pgdata") {
			t.Error("should not emit replication changes when already synced at desired count")
		}
	}
}

// TestStorageControllerReplicationScaleUp verifies that increasing the desired
// replica count triggers a re-sync.
func TestStorageControllerReplicationScaleUp(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "100Gi", true)
	types.WriteDesiredVolumeReplicas(ctx, stateStore, "pgdata", 5)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name:         "pgdata",
		Size:         "100Gi",
		State:        types.VolumeAvailable,
		ReplicaCount: 3,
		ReplicaState: types.ReplicaSynced,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	changeMap := changesByKey(changes)
	if string(changeMap[types.KeyObservedVolumeReplicaCount("pgdata")].Value) != "5" {
		t.Error("expected replica_count updated to 5")
	}
	if string(changeMap[types.KeyObservedVolumeReplicaState("pgdata")].Value) != string(types.ReplicaSyncing) {
		t.Error("expected replica_state set to syncing during scale-up")
	}
}

// TestStorageControllerVolumeUsageCleanedOnRemoval verifies that volume usage
// keys are cleaned up when the volume is no longer desired.
func TestStorageControllerVolumeUsageCleanedOnRemoval(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name:          "pgdata",
		Size:          "100Gi",
		State:         types.VolumeAvailable,
		UsedBytes:     50000,
		CapacityBytes: 100000,
	})

	changes := helperCollectStorageControllerChanges(t, stateStore)

	deletedKeys := make(map[string]bool)
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deletedKeys[change.Key] = true
		}
	}
	if !deletedKeys[types.KeyObservedVolume("pgdata")] {
		t.Error("expected observed volume marker deleted")
	}
	if !deletedKeys[types.KeyObservedVolumeUsedBytes("pgdata")] {
		t.Error("expected used_bytes deleted")
	}
	if !deletedKeys[types.KeyObservedVolumeCapacityBytes("pgdata")] {
		t.Error("expected capacity_bytes deleted")
	}
}

// TestStorageControllerResizeRetryAfterProviderFailure verifies that when the
// provider fails a resize, the pending operation remains in the store and
// succeeds on the next post-commit execution.
func TestStorageControllerResizeRetryAfterProviderFailure(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	storageController := NewStorageController(stateStore)
	storageController.SetStorageProvider(simulatorStorageProvider)

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "200Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, scanErr := stateStore.Scan(ctx, prefix)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)

	changes, reconcileErr := storageController.Reconcile(ctx, allFacts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	for _, change := range changes {
		if change.Type == store.OpPut {
			if _, putErr := stateStore.Put(ctx, change.Key, change.Value); putErr != nil {
				t.Fatal(putErr)
			}
		}
	}

	simulatorStorageProvider.InjectResizeError(fmt.Errorf("simulated cloud provider timeout"))

	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}

	// Pending operation should still exist after failure.
	_, pendingGetErr := stateStore.Get(ctx, types.KeyDerivedVolumePendingResize("pgdata"))
	if pendingGetErr != nil {
		t.Fatal("pending_resize should remain after provider failure")
	}

	// Observed size should NOT have been updated.
	sizeFact, sizeGetErr := stateStore.Get(ctx, types.KeyObservedVolumeSize("pgdata"))
	if sizeGetErr != nil {
		t.Fatal("observed size should still exist")
	}
	if string(sizeFact.Value) != "100Gi" {
		t.Errorf("observed size should remain 100Gi after failed resize, got %s", sizeFact.Value)
	}

	// Retry: this time the provider succeeds.
	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}

	// Pending operation should be cleaned up.
	if _, getErr := stateStore.Get(ctx, types.KeyDerivedVolumePendingResize("pgdata")); getErr == nil {
		t.Error("pending_resize should be deleted after successful retry")
	}

	// Observed size should now reflect the resize.
	sizeFact, sizeGetErr = stateStore.Get(ctx, types.KeyObservedVolumeSize("pgdata"))
	if sizeGetErr != nil {
		t.Fatal("expected observed size after retry")
	}
	if string(sizeFact.Value) != "200Gi" {
		t.Errorf("observed size after retry = %s, want 200Gi", sizeFact.Value)
	}

	// Provider should have the new capacity.
	_, capacityBytes, _ := simulatorStorageProvider.VolumeUsage(ctx, "pgdata")
	if capacityBytes != 200*1024*1024*1024 {
		t.Errorf("provider capacity = %d, want %d", capacityBytes, 200*1024*1024*1024)
	}
}

// TestStorageControllerSimultaneousResizeAndMigration verifies that a volume
// that needs both resize and migration produces two distinct pending keys
// (pending_resize and pending_snapshot) without key collision.
func TestStorageControllerSimultaneousResizeAndMigration(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100*1024*1024*1024)

	storageController := NewStorageController(stateStore)
	storageController.SetStorageProvider(simulatorStorageProvider)

	types.WriteDesiredVolume(ctx, stateStore, "pgdata", "200Gi", true)
	types.WriteObservedVolume(ctx, stateStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached,
		Node: "node-1", Instance: "aaa", MountPath: "/mnt/volumes/pgdata",
	})
	types.WriteNode(ctx, stateStore, types.Node{
		ID: "node-1", State: types.NodeUnreachable,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, scanErr := stateStore.Scan(ctx, prefix)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)

	changes, reconcileErr := storageController.Reconcile(ctx, allFacts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	changeMap := changesByKey(changes)

	resizeKey := types.KeyDerivedVolumePendingResize("pgdata")
	snapshotKey := types.KeyDerivedVolumePendingSnapshot("pgdata")

	if _, hasResize := changeMap[resizeKey]; !hasResize {
		t.Fatal("expected pending_resize change")
	}
	if _, hasSnapshot := changeMap[snapshotKey]; !hasSnapshot {
		t.Fatal("expected pending_snapshot change")
	}

	if resizeKey == snapshotKey {
		t.Fatal("resize and snapshot pending keys must be distinct")
	}

	// Commit all changes and execute post-commit.
	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			if _, putErr := stateStore.Put(ctx, change.Key, change.Value); putErr != nil {
				t.Fatal(putErr)
			}
		case store.OpDelete:
			_ = stateStore.Delete(ctx, change.Key)
		}
	}

	if postCommitErr := storageController.ExecutePostCommitOperations(ctx); postCommitErr != nil {
		t.Fatal(postCommitErr)
	}

	// Both operations should have completed.
	if _, getErr := stateStore.Get(ctx, resizeKey); getErr == nil {
		t.Error("pending_resize should be cleaned up after execution")
	}
	if _, getErr := stateStore.Get(ctx, snapshotKey); getErr == nil {
		t.Error("pending_snapshot should be cleaned up after execution")
	}

	// Provider should reflect both operations.
	_, capacityBytes, _ := simulatorStorageProvider.VolumeUsage(ctx, "pgdata")
	if capacityBytes != 200*1024*1024*1024 {
		t.Errorf("provider capacity = %d, want %d", capacityBytes, 200*1024*1024*1024)
	}
	snapshots := simulatorStorageProvider.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if !strings.HasPrefix(snapshots[0], "pgdata-pre-migration-") {
		t.Errorf("snapshot name %q missing expected prefix", snapshots[0])
	}
}

// changesByKey creates a map from fact key to the last Change for that key.
func changesByKey(changes []Change) map[string]Change {
	result := make(map[string]Change)
	for _, change := range changes {
		result[change.Key] = change
	}
	return result
}
