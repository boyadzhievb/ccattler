// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// nfsVolume tracks the in-memory state for a volume on an NFS mount.
type nfsVolume struct {
	sizeBytes     int64    // sizeBytes is the declared volume capacity.
	attachedNodes []string // attachedNodes lists nodes that have this volume attached (NFS allows ReadWriteMany).
}

// NFSStorageProvider implements StorageProvider using directories on an NFS mount.
// Unlike LocalPathStorageProvider, NFS volumes are accessible from any node that
// has the NFS share mounted, so AttachVolume does not enforce exclusive access.
type NFSStorageProvider struct {
	// basePath is the NFS mount point (e.g. /mnt/nfs/cca-volumes).
	basePath string
	// volumes tracks attachment state in memory.
	volumes map[string]*nfsVolume
	// mutex protects all mutable fields for concurrent access.
	mutex sync.Mutex
}

// NewNFSStorageProvider creates a provider that stores volumes as subdirectories
// under the NFS mount at basePath. Returns an error if the path does not exist
// (the operator must mount NFS before starting the agent).
func NewNFSStorageProvider(basePath string) (*NFSStorageProvider, error) {
	info, statError := os.Stat(basePath)
	if statError != nil {
		return nil, fmt.Errorf("NFS mount path %s does not exist: %w (mount NFS before starting the agent)", basePath, statError)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("NFS mount path %s is not a directory", basePath)
	}
	return &NFSStorageProvider{
		basePath: basePath,
		volumes:  make(map[string]*nfsVolume),
	}, nil
}

// volumeDirectoryPath returns the host filesystem path for a volume.
func (nfsProvider *NFSStorageProvider) volumeDirectoryPath(volumeName string) string {
	return filepath.Join(nfsProvider.basePath, volumeName)
}

// CreateVolume creates a directory for the named volume on the NFS share.
// Idempotent — creating a volume that already exists is a no-op.
func (nfsProvider *NFSStorageProvider) CreateVolume(_ context.Context, volumeName string, sizeBytes int64) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	volumePath := nfsProvider.volumeDirectoryPath(volumeName)
	if createError := os.MkdirAll(volumePath, 0750); createError != nil {
		return fmt.Errorf("creating volume directory %s: %w", volumePath, createError)
	}

	if _, alreadyTracked := nfsProvider.volumes[volumeName]; !alreadyTracked {
		nfsProvider.volumes[volumeName] = &nfsVolume{sizeBytes: sizeBytes}
	}
	return nil
}

// DeleteVolume removes the volume directory. Returns an error if the volume
// is currently attached. Idempotent — deleting a nonexistent volume is a no-op.
func (nfsProvider *NFSStorageProvider) DeleteVolume(_ context.Context, volumeName string) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if isTracked && len(trackedVolume.attachedNodes) > 0 {
		return fmt.Errorf("cannot delete volume %q: currently attached to %d node(s)", volumeName, len(trackedVolume.attachedNodes))
	}

	volumePath := nfsProvider.volumeDirectoryPath(volumeName)
	if removeError := os.RemoveAll(volumePath); removeError != nil {
		return fmt.Errorf("removing volume directory %s: %w", volumePath, removeError)
	}

	delete(nfsProvider.volumes, volumeName)
	return nil
}

// AttachVolume records that a node is using this volume and returns the NFS
// directory path. NFS supports ReadWriteMany, so multiple nodes can attach
// the same volume simultaneously. Idempotent for the same node.
func (nfsProvider *NFSStorageProvider) AttachVolume(_ context.Context, volumeName string, nodeID string) (string, error) {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	volumePath := nfsProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return "", fmt.Errorf("volume %q does not exist (no directory at %s)", volumeName, volumePath)
	}

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if !isTracked {
		trackedVolume = &nfsVolume{}
		nfsProvider.volumes[volumeName] = trackedVolume
	}

	for _, existingNode := range trackedVolume.attachedNodes {
		if existingNode == nodeID {
			return volumePath, nil
		}
	}

	trackedVolume.attachedNodes = append(trackedVolume.attachedNodes, nodeID)
	return volumePath, nil
}

// DetachVolume removes a node from the volume's attachment list. Idempotent.
func (nfsProvider *NFSStorageProvider) DetachVolume(_ context.Context, volumeName string, nodeID string) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if !isTracked {
		return nil
	}

	filteredNodes := make([]string, 0, len(trackedVolume.attachedNodes))
	for _, existingNode := range trackedVolume.attachedNodes {
		if existingNode != nodeID {
			filteredNodes = append(filteredNodes, existingNode)
		}
	}
	trackedVolume.attachedNodes = filteredNodes
	return nil
}

// IsAttached reports whether the named volume is attached to any node. For
// NFS ReadWriteMany, returns the first attached node if any.
func (nfsProvider *NFSStorageProvider) IsAttached(_ context.Context, volumeName string) (bool, string, error) {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if !isTracked || len(trackedVolume.attachedNodes) == 0 {
		return false, "", nil
	}
	return true, trackedVolume.attachedNodes[0], nil
}

// SnapshotVolume creates a copy of the volume directory as a snapshot.
func (nfsProvider *NFSStorageProvider) SnapshotVolume(_ context.Context, volumeName string, snapshotName string) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	volumePath := nfsProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return fmt.Errorf("volume %q does not exist", volumeName)
	}

	snapshotPath := filepath.Join(nfsProvider.basePath, ".snapshots", snapshotName)
	if createError := os.MkdirAll(filepath.Dir(snapshotPath), 0750); createError != nil {
		return fmt.Errorf("creating snapshot directory: %w", createError)
	}

	return copyDirectory(volumePath, snapshotPath)
}

// VolumeUsage returns actual disk usage for the volume directory.
func (nfsProvider *NFSStorageProvider) VolumeUsage(_ context.Context, volumeName string) (int64, int64, error) {
	nfsProvider.mutex.Lock()
	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	declaredSize := int64(0)
	if isTracked {
		declaredSize = trackedVolume.sizeBytes
	}
	nfsProvider.mutex.Unlock()

	volumePath := nfsProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return 0, 0, nil
	}

	usedBytes := calculateDirectorySize(volumePath)
	return usedBytes, declaredSize, nil
}

// ResizeVolume updates the declared capacity. Only expansion is supported.
func (nfsProvider *NFSStorageProvider) ResizeVolume(_ context.Context, volumeName string, newSizeBytes int64) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if !isTracked {
		return fmt.Errorf("volume %q does not exist", volumeName)
	}

	if newSizeBytes < trackedVolume.sizeBytes {
		return fmt.Errorf("cannot shrink volume %q from %d to %d bytes", volumeName, trackedVolume.sizeBytes, newSizeBytes)
	}

	trackedVolume.sizeBytes = newSizeBytes
	return nil
}

// ForceDetach clears all node attachments for a volume.
func (nfsProvider *NFSStorageProvider) ForceDetach(_ context.Context, volumeName string) error {
	nfsProvider.mutex.Lock()
	defer nfsProvider.mutex.Unlock()

	trackedVolume, isTracked := nfsProvider.volumes[volumeName]
	if !isTracked {
		return nil
	}

	trackedVolume.attachedNodes = nil
	return nil
}
