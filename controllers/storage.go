package controllers

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// StorageController watches desired volume declarations and observed volume
// state to manage the volume lifecycle. It ensures desired volumes exist in
// the observed state, and force-detaches volumes from unreachable nodes so
// they can be reattached elsewhere.
type StorageController struct{}

// NewStorageController returns a StorageController ready for registration
// with the controller runner.
func NewStorageController() *StorageController {
	return &StorageController{}
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
func (storageController *StorageController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
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

	// Force-detach volumes attached to unreachable nodes.
	for volumeName, observedInfo := range observedVolumes {
		if observedInfo.state != types.VolumeAttached || observedInfo.node == "" {
			continue
		}

		nodeState, nodeExists := nodeStates[observedInfo.node]
		if !nodeExists || nodeState == types.NodeUnreachable {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedVolumeState(volumeName),
				Value: []byte(string(types.VolumeAvailable)),
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
		}
	}

	return changes, nil
}

// desiredVolumeInfo holds parsed fields from desired volume facts.
type desiredVolumeInfo struct {
	size       string // size is the raw DSL size string (e.g. "100Gi").
	persistent bool   // persistent is true if the volume survives instance deletion.
}

// observedVolumeInfo holds parsed fields from observed volume facts.
type observedVolumeInfo struct {
	state    types.VolumeState // state is the current lifecycle state (available, attached).
	node     string            // node is the ID of the node this volume is attached to.
	instance string            // instance is the ID of the instance this volume is mounted into.
	size     string            // size is the volume's size.
}
