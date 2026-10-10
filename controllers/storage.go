// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// StorageController watches desired volume declarations and observed volume
// state to manage the volume lifecycle. It ensures desired volumes exist in
// the observed state, and force-detaches volumes from unreachable nodes so
// they can be reattached elsewhere. Implements PostCommitController to execute
// storage provider operations (resize, snapshot) only after the store
// transaction succeeds.
type StorageController struct {
	storageProvider storage.StorageProvider // storageProvider is the backend for resize/snapshot calls.
	factStore       store.StateStore        // factStore is used by the post-commit executor to read pending operations and write completion markers.
}

// NewStorageController returns a StorageController ready for registration
// with the controller runner. The storageProvider is optional — when present
// the controller takes snapshots before migrating volumes.
func NewStorageController(factStore store.StateStore) *StorageController {
	return &StorageController{factStore: factStore}
}

// SetStorageProvider configures the storage backend used for snapshots during
// volume migration.
func (storageController *StorageController) SetStorageProvider(storageProvider storage.StorageProvider) {
	storageController.storageProvider = storageProvider
}

// Name returns "storage", identifying this controller in logs and runner
// bookkeeping.
func (storageController *StorageController) Name() string { return "storage" }

// Watch returns the fact prefixes the storage controller monitors: desired
// volume declarations, observed volume state, observed node state (for
// detecting unreachable nodes that hold volumes), and derived volume
// operations (for post-commit execution).
func (storageController *StorageController) Watch() []string {
	return []string{
		types.ScanDesiredVolumes,
		types.ScanObservedVolumes,
		types.ScanObservedNodes,
		types.ScanDerivedVolumes,
	}
}

// Reconcile examines desired volumes, observed volumes, and node state, then
// delegates to sub-reconcilers that create missing volumes, resize volumes,
// update replication, force-detach volumes from unreachable nodes, and clean
// up volumes that are no longer desired. Each sub-reconciler returns proposed
// changes which this coordinator collects into a single slice.
func (storageController *StorageController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	desiredVolumes := collectDesiredVolumes(facts)
	observedVolumes := collectObservedVolumes(facts)
	nodeStates := collectNodeStates(facts)

	var allChanges []Change

	creationChanges := reconcileVolumeCreation(desiredVolumes, observedVolumes)
	allChanges = append(allChanges, creationChanges...)

	resizeChanges := storageController.reconcileVolumeResize(ctx, desiredVolumes, observedVolumes)
	allChanges = append(allChanges, resizeChanges...)

	replicationChanges := reconcileVolumeReplication(desiredVolumes, observedVolumes)
	allChanges = append(allChanges, replicationChanges...)

	migrationChanges := storageController.reconcileVolumeMigration(ctx, observedVolumes, nodeStates)
	allChanges = append(allChanges, migrationChanges...)

	cleanupChanges := reconcileVolumeCleanup(desiredVolumes, observedVolumes)
	allChanges = append(allChanges, cleanupChanges...)

	return allChanges, nil
}

// collectDesiredVolumes parses facts under the desired-volumes prefix and
// returns a map from volume name to its desired configuration (size,
// persistent flag, replica count).
func collectDesiredVolumes(facts []store.Fact) map[string]desiredVolumeInfo {
	desiredVolumes := make(map[string]desiredVolumeInfo)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredVolumes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredVolumes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		volumeName := pathParts[0]

		if _, exists := desiredVolumes[volumeName]; !exists {
			desiredVolumes[volumeName] = desiredVolumeInfo{}
		}

		if len(pathParts) == 2 {
			info := desiredVolumes[volumeName]
			switch pathParts[1] {
			case "size":
				info.size = string(fact.Value)
			case "persistent":
				info.persistent = string(fact.Value) == "true"
			case "replicas":
				parsedReplicas, parseErr := strconv.Atoi(string(fact.Value))
				if parseErr != nil {
					logging.Default().Warn("corrupt volume replicas fact", "volume", volumeName, "value", string(fact.Value))
				}
				info.replicas = parsedReplicas
			}
			desiredVolumes[volumeName] = info
		}
	}
	return desiredVolumes
}

// collectObservedVolumes parses facts under the observed-volumes prefix and
// returns a map from volume name to its observed state (lifecycle state, node,
// instance, size, migration source, replication info).
func collectObservedVolumes(facts []store.Fact) map[string]observedVolumeInfo {
	observedVolumes := make(map[string]observedVolumeInfo)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedVolumes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedVolumes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		volumeName := pathParts[0]

		if _, exists := observedVolumes[volumeName]; !exists {
			observedVolumes[volumeName] = observedVolumeInfo{}
		}

		if len(pathParts) == 2 {
			info := observedVolumes[volumeName]
			switch pathParts[1] {
			case "state":
				info.state = types.VolumeState(fact.Value)
			case "node":
				info.node = string(fact.Value)
			case "instance":
				info.instance = string(fact.Value)
			case "size":
				info.size = string(fact.Value)
			case "migration_source":
				info.migrationSource = string(fact.Value)
			case "replica_count":
				parsedReplicaCount, parseErr := strconv.Atoi(string(fact.Value))
				if parseErr != nil {
					logging.Default().Warn("corrupt volume replica_count fact", "volume", volumeName, "value", string(fact.Value))
				}
				info.replicaCount = parsedReplicaCount
			case "replica_state":
				info.replicaState = types.ReplicaState(fact.Value)
			}
			observedVolumes[volumeName] = info
		}
	}
	return observedVolumes
}

// collectNodeStates parses facts under the observed-nodes prefix and returns
// a map from node ID to its current state (ready, unreachable, etc.).
func collectNodeStates(facts []store.Fact) map[string]types.NodeState {
	nodeStates := make(map[string]types.NodeState)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 2 && pathParts[1] == "state" {
			nodeStates[pathParts[0]] = types.NodeState(fact.Value)
		}
	}
	return nodeStates
}

// reconcileVolumeCreation returns changes that create observed volume entries
// for every desired volume that does not yet have a corresponding observed
// entry. Each new volume is initialized to VolumeAvailable state with its
// desired size.
func reconcileVolumeCreation(desiredVolumes map[string]desiredVolumeInfo, observedVolumes map[string]observedVolumeInfo) []Change {
	var changes []Change
	for volumeName, desiredInfo := range desiredVolumes {
		if _, alreadyObserved := observedVolumes[volumeName]; !alreadyObserved {
			changes = append(changes, groupedChanges("vol-create/"+volumeName,
				Change{Type: store.OpPut, Key: types.KeyObservedVolume(volumeName), Value: []byte("")},
				Change{Type: store.OpPut, Key: types.KeyObservedVolumeState(volumeName), Value: []byte(string(types.VolumeAvailable))},
				Change{Type: store.OpPut, Key: types.KeyObservedVolumeSize(volumeName), Value: []byte(desiredInfo.size)},
			)...)
		}
	}
	return changes
}

// reconcileVolumeResize returns changes that update the observed size of
// volumes whose desired size differs from the currently observed size. When
// a storage provider is configured, a pending resize operation fact is
// emitted instead of calling the provider directly — the actual resize
// executes post-commit via ExecutePostCommitOperations.
func (storageController *StorageController) reconcileVolumeResize(ctx context.Context, desiredVolumes map[string]desiredVolumeInfo, observedVolumes map[string]observedVolumeInfo) []Change {
	var changes []Change
	for volumeName, desiredInfo := range desiredVolumes {
		observedInfo, exists := observedVolumes[volumeName]
		if !exists || observedInfo.size == desiredInfo.size || desiredInfo.size == "" {
			continue
		}
		if storageController.storageProvider != nil {
			operationID := buildDeterministicOperationID("resize", volumeName, desiredInfo.size)
			pendingOp := pendingVolumeOperation{
				ID:         operationID,
				Kind:       "resize",
				VolumeName: volumeName,
				TargetSize: desiredInfo.size,
			}
			pendingJSON, marshalErr := json.Marshal(pendingOp)
			if marshalErr != nil {
				logging.Default().Error("failed to marshal pending operation", "volume", volumeName, "error", marshalErr.Error())
				continue
			}
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedVolumePendingResize(volumeName),
				Value: pendingJSON,
			})
		} else {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeSize(volumeName),
				Value: []byte(desiredInfo.size),
			})
		}
	}
	return changes
}

// reconcileVolumeReplication returns changes that bring observed replica count
// and replication state into alignment with the desired replica count. Volumes
// with replicas <= 1 or volumes that are already synced at the correct count
// are skipped.
func reconcileVolumeReplication(desiredVolumes map[string]desiredVolumeInfo, observedVolumes map[string]observedVolumeInfo) []Change {
	var changes []Change
	for volumeName, desiredInfo := range desiredVolumes {
		if desiredInfo.replicas <= 1 {
			continue
		}
		observedInfo, exists := observedVolumes[volumeName]
		if !exists {
			continue
		}
		if observedInfo.replicaCount == desiredInfo.replicas && observedInfo.replicaState == types.ReplicaSynced {
			continue
		}
		if observedInfo.replicaCount != desiredInfo.replicas {
			changes = append(changes, groupedChanges("vol-replicate/"+volumeName,
				Change{Type: store.OpPut, Key: types.KeyObservedVolumeReplicaCount(volumeName), Value: []byte(strconv.Itoa(desiredInfo.replicas))},
				Change{Type: store.OpPut, Key: types.KeyObservedVolumeReplicaState(volumeName), Value: []byte(string(types.ReplicaSyncing))},
			)...)
		}
	}
	return changes
}

// reconcileVolumeMigration returns changes that force-detach volumes currently
// attached to unreachable nodes. The volume transitions to VolumeMigrating
// state, records the original node as migration source, and clears node,
// instance, and mount-path bindings. When a storage provider is available, a
// pending snapshot operation is emitted — the actual snapshot executes
// post-commit via ExecutePostCommitOperations.
func (storageController *StorageController) reconcileVolumeMigration(ctx context.Context, observedVolumes map[string]observedVolumeInfo, nodeStates map[string]types.NodeState) []Change {
	var changes []Change
	for volumeName, observedInfo := range observedVolumes {
		if observedInfo.state != types.VolumeAttached || observedInfo.node == "" {
			continue
		}
		nodeState, nodeExists := nodeStates[observedInfo.node]
		if nodeExists && nodeState != types.NodeUnreachable {
			continue
		}

		groupID := "vol-migrate/" + volumeName
		var migrationChanges []Change
		if storageController.storageProvider != nil {
			operationID := buildDeterministicOperationID("snapshot", volumeName, observedInfo.node)
			snapshotName := fmt.Sprintf("%s-pre-migration-%s", volumeName, operationID)
			pendingOp := pendingVolumeOperation{
				ID:           operationID,
				Kind:         "snapshot",
				VolumeName:   volumeName,
				SnapshotName: snapshotName,
			}
			pendingJSON, marshalErr := json.Marshal(pendingOp)
			if marshalErr != nil {
				logging.Default().Error("failed to marshal pending operation", "volume", volumeName, "error", marshalErr.Error())
			} else {
				migrationChanges = append(migrationChanges,
					Change{Type: store.OpPut, Key: types.KeyDerivedVolumePendingSnapshot(volumeName), Value: pendingJSON},
				)
			}
		}
		migrationChanges = append(migrationChanges,
			Change{Type: store.OpPut, Key: types.KeyObservedVolumeState(volumeName), Value: []byte(string(types.VolumeMigrating))},
			Change{Type: store.OpPut, Key: types.KeyObservedVolumeMigrationSource(volumeName), Value: []byte(observedInfo.node)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeNode(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeInstance(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeMountPath(volumeName)},
		)
		changes = append(changes, groupedChanges(groupID, migrationChanges...)...)
	}
	return changes
}

// reconcileVolumeCleanup returns changes that delete all observed-state and
// derived-state keys for volumes which are no longer present in the desired
// set. Derived keys (pending_resize, pending_snapshot, last_operation) must
// be cleaned up alongside observed keys to prevent the post-commit executor
// from processing operations for deleted volumes.
func reconcileVolumeCleanup(desiredVolumes map[string]desiredVolumeInfo, observedVolumes map[string]observedVolumeInfo) []Change {
	var changes []Change
	for volumeName := range observedVolumes {
		if _, stillDesired := desiredVolumes[volumeName]; stillDesired {
			continue
		}
		changes = append(changes, groupedChanges("vol-cleanup/"+volumeName,
			Change{Type: store.OpDelete, Key: types.KeyObservedVolume(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeState(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeSize(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeNode(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeInstance(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeMountPath(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeMigrationSource(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeLastSnapshot(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeUsedBytes(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeCapacityBytes(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeReplicaCount(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyObservedVolumeReplicaState(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyDerivedVolumePendingResize(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyDerivedVolumePendingSnapshot(volumeName)},
			Change{Type: store.OpDelete, Key: types.KeyDerivedVolumeLastOperation(volumeName)},
		)...)
	}
	return changes
}

// desiredVolumeInfo holds parsed fields from desired volume facts.
type desiredVolumeInfo struct {
	size       string // size is the raw DSL size string (e.g. "100Gi").
	persistent bool   // persistent is true if the volume survives instance deletion.
	replicas   int    // replicas is the desired number of synchronized copies (0 or 1 means no replication).
}

// observedVolumeInfo holds parsed fields from observed volume facts.
type observedVolumeInfo struct {
	state           types.VolumeState  // state is the current lifecycle state (available, attached, migrating).
	node            string             // node is the ID of the node this volume is attached to.
	instance        string             // instance is the ID of the instance this volume is mounted into.
	size            string             // size is the volume's size.
	migrationSource string             // migrationSource is the node the volume was force-detached from.
	replicaCount    int                // replicaCount is the current number of synchronized replicas.
	replicaState    types.ReplicaState // replicaState is the replication sync state.
}

// parseSizeToBytes converts a human-readable size string like "100Gi" or "10Mi"
// to bytes. Returns 0 for unparseable input.
func parseSizeToBytes(sizeString string) int64 {
	sizeString = strings.TrimSpace(sizeString)
	if sizeString == "" {
		return 0
	}

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(sizeString, "Gi"):
		multiplier = 1024 * 1024 * 1024
		sizeString = strings.TrimSuffix(sizeString, "Gi")
	case strings.HasSuffix(sizeString, "Mi"):
		multiplier = 1024 * 1024
		sizeString = strings.TrimSuffix(sizeString, "Mi")
	case strings.HasSuffix(sizeString, "Ki"):
		multiplier = 1024
		sizeString = strings.TrimSuffix(sizeString, "Ki")
	}

	numericValue, parseErr := strconv.ParseInt(sizeString, 10, 64)
	if parseErr != nil {
		return 0
	}
	return numericValue * multiplier
}

// pendingVolumeOperation describes a storage provider call that must execute
// after a successful CAS commit. Serialized as JSON into the derived/volume/
// pending_operation key.
type pendingVolumeOperation struct {
	ID           string `json:"id"`            // ID is a deterministic identifier for idempotency checking.
	Kind         string `json:"kind"`          // Kind is "resize" or "snapshot".
	VolumeName   string `json:"volume_name"`   // VolumeName is the target volume.
	TargetSize   string `json:"target_size"`   // TargetSize is the desired size (resize only).
	SnapshotName string `json:"snapshot_name"` // SnapshotName is the deterministic snapshot name (snapshot only).
}

// buildDeterministicOperationID produces a stable, short hash from the
// operation kind, volume name, and a distinguishing parameter (target size
// for resize, source node for snapshot). The same inputs always produce the
// same ID, preventing duplicate external calls on CAS retry.
func buildDeterministicOperationID(kind string, volumeName string, parameter string) string {
	hashInput := fmt.Sprintf("%s:%s:%s", kind, volumeName, parameter)
	hashBytes := sha256.Sum256([]byte(hashInput))
	return fmt.Sprintf("%x", hashBytes[:8])
}

// ExecutePostCommitOperations reads pending volume operations from the store
// and calls the storage provider to execute them. Each operation is checked
// for idempotency against the last-operation key — if the IDs match, the
// operation is skipped. On success the pending key is deleted and the
// last-operation key is updated. On failure the pending key is left in place
// for the next reconciliation cycle to retry.
func (storageController *StorageController) ExecutePostCommitOperations(ctx context.Context) error {
	if storageController.storageProvider == nil {
		return nil
	}

	pendingFacts, scanError := storageController.factStore.Scan(ctx, types.ScanDerivedVolumes)
	if scanError != nil {
		return fmt.Errorf("scanning pending volume operations: %w", scanError)
	}

	for _, pendingFact := range pendingFacts {
		if !strings.HasSuffix(pendingFact.Key, "/pending_resize") &&
			!strings.HasSuffix(pendingFact.Key, "/pending_snapshot") &&
			!strings.HasSuffix(pendingFact.Key, "/pending_operation") {
			continue
		}

		var pendingOp pendingVolumeOperation
		if unmarshalErr := json.Unmarshal(pendingFact.Value, &pendingOp); unmarshalErr != nil {
			logging.Default().Error("corrupt pending operation", "key", pendingFact.Key, "error", unmarshalErr.Error())
			continue
		}

		lastOpFact, lastOpErr := storageController.factStore.Get(ctx, types.KeyDerivedVolumeLastOperation(pendingOp.VolumeName))
		if lastOpErr == nil && string(lastOpFact.Value) == pendingOp.ID {
			if deleteErr := storageController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
				logging.Default().Error("failed to clean up duplicate pending operation", "volume", pendingOp.VolumeName, "error", deleteErr.Error())
			}
			continue
		}

		if _, desiredErr := storageController.factStore.Get(ctx, types.KeyDesiredVolume(pendingOp.VolumeName)); desiredErr != nil {
			logging.Default().Warn("skipping pending operation for deleted volume",
				"volume", pendingOp.VolumeName, "kind", pendingOp.Kind)
			if deleteErr := storageController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
				logging.Default().Error("failed to clean up orphaned pending operation", "volume", pendingOp.VolumeName, "error", deleteErr.Error())
			}
			continue
		}

		var executeError error
		switch pendingOp.Kind {
		case "resize":
			executeError = storageController.storageProvider.ResizeVolume(ctx, pendingOp.VolumeName, parseSizeToBytes(pendingOp.TargetSize))
		case "snapshot":
			executeError = storageController.storageProvider.SnapshotVolume(ctx, pendingOp.VolumeName, pendingOp.SnapshotName)
		default:
			logging.Default().Error("unknown pending operation kind", "volume", pendingOp.VolumeName, "kind", pendingOp.Kind)
			continue
		}

		if executeError != nil {
			logging.Default().Error("volume operation failed, will retry",
				"volume", pendingOp.VolumeName, "kind", pendingOp.Kind, "error", executeError.Error())
			continue
		}

		if _, putErr := storageController.factStore.Put(ctx, types.KeyDerivedVolumeLastOperation(pendingOp.VolumeName), []byte(pendingOp.ID)); putErr != nil {
			logging.Default().Error("failed to write last-operation marker", "volume", pendingOp.VolumeName, "error", putErr.Error())
		}
		if deleteErr := storageController.factStore.Delete(ctx, pendingFact.Key); deleteErr != nil {
			logging.Default().Error("failed to clean up pending operation", "volume", pendingOp.VolumeName, "error", deleteErr.Error())
		}

		if pendingOp.Kind == "resize" && pendingOp.TargetSize != "" {
			if _, putErr := storageController.factStore.Put(ctx, types.KeyObservedVolumeSize(pendingOp.VolumeName), []byte(pendingOp.TargetSize)); putErr != nil {
				logging.Default().Error("failed to update observed volume size after resize", "volume", pendingOp.VolumeName, "error", putErr.Error())
			}
		}

		if pendingOp.Kind == "snapshot" {
			if _, putErr := storageController.factStore.Put(ctx, types.KeyObservedVolumeLastSnapshot(pendingOp.VolumeName), []byte(pendingOp.SnapshotName)); putErr != nil {
				logging.Default().Error("failed to write snapshot name", "volume", pendingOp.VolumeName, "error", putErr.Error())
			}
		}
	}

	return nil
}
