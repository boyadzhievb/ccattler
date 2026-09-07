package storage

import (
	"context"
	"fmt"
	"sync"
)

// simulatedVolume tracks the state of a single volume in the simulator.
type simulatedVolume struct {
	sizeBytes      int64  // sizeBytes is the declared volume capacity.
	attachedNodeID string // attachedNodeID is the node currently holding the volume, empty if detached.
	mountPath      string // mountPath is the filesystem path where the volume is mounted.
}

// SimulatorStorageProvider implements StorageProvider using in-memory state.
// It does not create real filesystems or block devices — it only tracks
// volume existence and attachment, making it suitable for integration tests
// with SimulatorRuntime.
type SimulatorStorageProvider struct {
	// volumes maps volume name to its simulated state.
	volumes map[string]*simulatedVolume
	// mutex protects the volumes map for concurrent access.
	mutex sync.Mutex
}

// NewSimulatorStorageProvider creates an empty SimulatorStorageProvider with
// no pre-existing volumes.
func NewSimulatorStorageProvider() *SimulatorStorageProvider {
	return &SimulatorStorageProvider{
		volumes: make(map[string]*simulatedVolume),
	}
}

// CreateVolume adds a volume to the simulator's tracked state. Idempotent —
// creating a volume that already exists is a no-op (the size is not updated).
func (simulatorStorageProvider *SimulatorStorageProvider) CreateVolume(_ context.Context, volumeName string, sizeBytes int64) error {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	if _, alreadyExists := simulatorStorageProvider.volumes[volumeName]; alreadyExists {
		return nil
	}

	simulatorStorageProvider.volumes[volumeName] = &simulatedVolume{
		sizeBytes: sizeBytes,
	}
	return nil
}

// DeleteVolume removes a volume from the simulator. Returns an error if the
// volume is currently attached to a node. Idempotent — deleting a
// nonexistent volume is a no-op.
func (simulatorStorageProvider *SimulatorStorageProvider) DeleteVolume(_ context.Context, volumeName string) error {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	existingVolume, volumeExists := simulatorStorageProvider.volumes[volumeName]
	if !volumeExists {
		return nil
	}

	if existingVolume.attachedNodeID != "" {
		return fmt.Errorf("cannot delete volume %q: currently attached to node %q", volumeName, existingVolume.attachedNodeID)
	}

	delete(simulatorStorageProvider.volumes, volumeName)
	return nil
}

// AttachVolume binds a volume to the specified node. Returns a deterministic
// mount path of the form /mnt/volumes/{volumeName}. Enforces exclusive
// attach: returns an error if the volume is already attached to a different
// node. Idempotent — attaching to the same node returns the existing mount
// path without error.
func (simulatorStorageProvider *SimulatorStorageProvider) AttachVolume(_ context.Context, volumeName string, nodeID string) (string, error) {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	existingVolume, volumeExists := simulatorStorageProvider.volumes[volumeName]
	if !volumeExists {
		return "", fmt.Errorf("volume %q does not exist", volumeName)
	}

	if existingVolume.attachedNodeID != "" && existingVolume.attachedNodeID != nodeID {
		return "", fmt.Errorf("volume %q is exclusively attached to node %q, cannot attach to node %q",
			volumeName, existingVolume.attachedNodeID, nodeID)
	}

	existingVolume.attachedNodeID = nodeID
	existingVolume.mountPath = fmt.Sprintf("/mnt/volumes/%s", volumeName)
	return existingVolume.mountPath, nil
}

// DetachVolume unbinds a volume from the specified node. Idempotent —
// detaching an already-detached volume or detaching from a node that doesn't
// hold the volume is a no-op.
func (simulatorStorageProvider *SimulatorStorageProvider) DetachVolume(_ context.Context, volumeName string, nodeID string) error {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	existingVolume, volumeExists := simulatorStorageProvider.volumes[volumeName]
	if !volumeExists {
		return nil
	}

	if existingVolume.attachedNodeID == nodeID {
		existingVolume.attachedNodeID = ""
		existingVolume.mountPath = ""
	}
	return nil
}

// IsAttached reports whether the named volume is currently attached to a
// node. Returns (false, "", nil) for unattached or unknown volumes.
func (simulatorStorageProvider *SimulatorStorageProvider) IsAttached(_ context.Context, volumeName string) (bool, string, error) {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	existingVolume, volumeExists := simulatorStorageProvider.volumes[volumeName]
	if !volumeExists {
		return false, "", nil
	}

	if existingVolume.attachedNodeID == "" {
		return false, "", nil
	}

	return true, existingVolume.attachedNodeID, nil
}

// ForceDetach detaches a volume regardless of which node holds it. This is
// used by the StorageController to reclaim volumes from unreachable nodes.
func (simulatorStorageProvider *SimulatorStorageProvider) ForceDetach(_ context.Context, volumeName string) error {
	simulatorStorageProvider.mutex.Lock()
	defer simulatorStorageProvider.mutex.Unlock()

	existingVolume, volumeExists := simulatorStorageProvider.volumes[volumeName]
	if !volumeExists {
		return nil
	}

	existingVolume.attachedNodeID = ""
	existingVolume.mountPath = ""
	return nil
}
