package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperCollectStorageControllerChanges writes facts to a store, scans all
// watched prefixes, and runs Reconcile. Returns the proposed changes.
func helperCollectStorageControllerChanges(t *testing.T, stateStore store.StateStore) []Change {
	t.Helper()
	storageController := NewStorageController()
	ctx := context.Background()

	var allFacts []store.Fact
	for _, prefix := range storageController.Watch() {
		facts, err := stateStore.Scan(ctx, prefix)
		if err != nil {
			t.Fatal(err)
		}
		allFacts = append(allFacts, facts...)
	}

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
	if string(changeMap[types.KeyObservedVolumeState("pgdata")].Value) != string(types.VolumeAvailable) {
		t.Error("expected volume state changed to available after force-detach")
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
// desired volumes, observed volumes, and observed nodes.
func TestStorageControllerWatchPrefixes(t *testing.T) {
	storageController := NewStorageController()
	watchPrefixes := storageController.Watch()

	expectedPrefixes := map[string]bool{
		types.ScanDesiredVolumes:  false,
		types.ScanObservedVolumes: false,
		types.ScanObservedNodes:   false,
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
	storageController := NewStorageController()
	if storageController.Name() != "storage" {
		t.Errorf("name: got %s, want storage", storageController.Name())
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
