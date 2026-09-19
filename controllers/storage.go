package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// StorageController watches desired volume declarations and observed volume
// state to manage the volume lifecycle. It ensures desired volumes exist in
// the observed state, and force-detaches volumes from unreachable nodes so
// they can be reattached elsewhere.
type StorageController struct {
	storageProvider storage.StorageProvider
}

// NewStorageController returns a StorageController ready for registration
// with the controller runner. The storageProvider is optional — when present
// the controller takes snapshots before migrating volumes.
func NewStorageController() *StorageController {
	return &StorageController{}
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
// volume declarations, observed volume state, and observed node state (for
// detecting unreachable nodes that hold volumes).
func (storageController *StorageController) Watch() []string {
	return []string{
		types.ScanDesiredVolumes,
		types.ScanObservedVolumes,
		types.ScanObservedNodes,
	}
}

// Reconcile examines desired volumes, observed volumes, and node state, then
// emits changes to create missing volumes, force-detach volumes from
// unreachable nodes, and clean up volumes that are no longer desired.
func (storageController *StorageController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	// Collect desired volumes: volumeName -> {size, persistent}.
	desiredVolumes := make(map[string]desiredVolumeInfo)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredVolumes) {
			continue
		}
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
				info.replicas, _ = strconv.Atoi(string(fact.Value))
			}
			desiredVolumes[volumeName] = info
		}
	}

	// Collect observed volumes: volumeName -> {state, node, instance}.
	observedVolumes := make(map[string]observedVolumeInfo)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedVolumes) {
			continue
		}
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
				info.replicaCount, _ = strconv.Atoi(string(fact.Value))
			case "replica_state":
				info.replicaState = types.ReplicaState(fact.Value)
			}
			observedVolumes[volumeName] = info
		}
	}

	// Collect node states: nodeID -> state.
	nodeStates := make(map[string]types.NodeState)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedNodes) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 2 && pathParts[1] == "state" {
			nodeStates[pathParts[0]] = types.NodeState(fact.Value)
		}
	}

	var changes []Change

	// Create observed volumes for desired volumes that don't exist yet.
	for volumeName, desiredInfo := range desiredVolumes {
		if _, alreadyObserved := observedVolumes[volumeName]; !alreadyObserved {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolume(volumeName),
				Value: []byte(""),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeState(volumeName),
				Value: []byte(string(types.VolumeAvailable)),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeSize(volumeName),
				Value: []byte(desiredInfo.size),
			})
		}
	}

	// Resize volumes where desired size differs from observed.
	for volumeName, desiredInfo := range desiredVolumes {
		observedInfo, exists := observedVolumes[volumeName]
		if !exists || observedInfo.size == desiredInfo.size || desiredInfo.size == "" {
			continue
		}
		if storageController.storageProvider != nil {
			resizeErr := storageController.storageProvider.ResizeVolume(ctx, volumeName, parseSizeToBytes(desiredInfo.size))
			if resizeErr != nil {
				logging.Default().Error("volume resize failed", "volume", volumeName, "error", resizeErr.Error())
				continue
			}
		}
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedVolumeSize(volumeName),
			Value: []byte(desiredInfo.size),
		})
	}

	// Reconcile volume replication: when desired replicas > 1 and observed
	// replication state is missing or replica count differs, update state.
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
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeReplicaCount(volumeName),
				Value: []byte(strconv.Itoa(desiredInfo.replicas)),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeReplicaState(volumeName),
				Value: []byte(string(types.ReplicaSyncing)),
			})
		}
	}

	// Force-detach volumes attached to unreachable nodes. Transition through
	// VolumeMigrating so the agent can observe the migration and reattach.
	for volumeName, observedInfo := range observedVolumes {
		if observedInfo.state == types.VolumeAttached && observedInfo.node != "" {
			nodeState, nodeExists := nodeStates[observedInfo.node]
			if !nodeExists || nodeState == types.NodeUnreachable {
				if storageController.storageProvider != nil {
					snapshotName := fmt.Sprintf("%s-pre-migration-%d", volumeName, time.Now().UnixMilli())
					snapshotErr := storageController.storageProvider.SnapshotVolume(ctx, volumeName, snapshotName)
					if snapshotErr != nil {
						logging.Default().Error("pre-migration snapshot failed", "volume", volumeName, "error", snapshotErr.Error())
					} else {
						changes = append(changes, Change{
							Type:  store.OpPut,
							Key:   types.KeyObservedVolumeLastSnapshot(volumeName),
							Value: []byte(snapshotName),
						})
					}
				}

				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedVolumeState(volumeName),
					Value: []byte(string(types.VolumeMigrating)),
				})
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyObservedVolumeMigrationSource(volumeName),
					Value: []byte(observedInfo.node),
				})
				changes = append(changes, Change{
					Type:  store.OpDelete,
					Key:   types.KeyObservedVolumeNode(volumeName),
				})
				changes = append(changes, Change{
					Type:  store.OpDelete,
					Key:   types.KeyObservedVolumeInstance(volumeName),
				})
				changes = append(changes, Change{
					Type:  store.OpDelete,
					Key:   types.KeyObservedVolumeMountPath(volumeName),
				})
			}
		}
	}

	// Clean up observed volumes that are no longer desired.
	for volumeName := range observedVolumes {
		if _, stillDesired := desiredVolumes[volumeName]; !stillDesired {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolume(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeState(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeSize(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeNode(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeInstance(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeMountPath(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeMigrationSource(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeLastSnapshot(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeUsedBytes(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeCapacityBytes(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeReplicaCount(volumeName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyObservedVolumeReplicaState(volumeName),
			})
		}
	}

	return changes, nil
}

// desiredVolumeInfo holds parsed fields from desired volume facts.
type desiredVolumeInfo struct {
	size       string // size is the raw DSL size string (e.g. "100Gi").
	persistent bool   // persistent is true if the volume survives instance deletion.
	replicas   int    // replicas is the desired number of synchronized copies (0 or 1 means no replication).
}

// observedVolumeInfo holds parsed fields from observed volume facts.
type observedVolumeInfo struct {
	state           types.VolumeState   // state is the current lifecycle state (available, attached, migrating).
	node            string              // node is the ID of the node this volume is attached to.
	instance        string              // instance is the ID of the instance this volume is mounted into.
	size            string              // size is the volume's size.
	migrationSource string              // migrationSource is the node the volume was force-detached from.
	replicaCount    int                 // replicaCount is the current number of synchronized replicas.
	replicaState    types.ReplicaState  // replicaState is the replication sync state.
}

// parseSizeToBytes converts a human-readable size string like "100Gi" or "10Mi"
// to bytes. Returns 0 for unparseable input.
func parseSizeToBytes(sizeString string) int64 {
	sizeString = strings.TrimSpace(sizeString)
	if sizeString == "" {
		return 0
	}

	multiplier := int64(1)
	if strings.HasSuffix(sizeString, "Gi") {
		multiplier = 1024 * 1024 * 1024
		sizeString = strings.TrimSuffix(sizeString, "Gi")
	} else if strings.HasSuffix(sizeString, "Mi") {
		multiplier = 1024 * 1024
		sizeString = strings.TrimSuffix(sizeString, "Mi")
	} else if strings.HasSuffix(sizeString, "Ki") {
		multiplier = 1024
		sizeString = strings.TrimSuffix(sizeString, "Ki")
	}

	numericValue, parseErr := strconv.ParseInt(sizeString, 10, 64)
	if parseErr != nil {
		return 0
	}
	return numericValue * multiplier
}
