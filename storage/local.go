// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// localVolume tracks the in-memory attachment state for a local-path volume.
type localVolume struct {
	sizeBytes      int64  // sizeBytes is the declared volume capacity.
	attachedNodeID string // attachedNodeID is the node currently holding the volume, empty if detached.
}

// LocalPathStorageProvider implements StorageProvider using host-local directories.
// Each volume is a subdirectory under the configured base path. Volumes survive
// container restarts on the same node but are not accessible from other nodes.
type LocalPathStorageProvider struct {
	// basePath is the root directory for all volumes (e.g. /var/lib/cca/volumes).
	basePath string
	// volumes tracks attachment state in memory. The source of truth for
	// existence is the filesystem; attachment state is ephemeral.
	volumes map[string]*localVolume
	// mutex protects all mutable fields for concurrent access.
	mutex sync.Mutex
}

// NewLocalPathStorageProvider creates a provider that stores volumes as
// subdirectories under basePath. The basePath directory is created if it
// does not exist.
func NewLocalPathStorageProvider(basePath string) (*LocalPathStorageProvider, error) {
	if createError := os.MkdirAll(basePath, 0750); createError != nil {
		return nil, fmt.Errorf("creating storage base path %s: %w", basePath, createError)
	}
	return &LocalPathStorageProvider{
		basePath: basePath,
		volumes:  make(map[string]*localVolume),
	}, nil
}

// volumeDirectoryPath returns the host filesystem path for a volume.
func (localProvider *LocalPathStorageProvider) volumeDirectoryPath(volumeName string) string {
	return filepath.Join(localProvider.basePath, volumeName)
}

// CreateVolume creates a directory for the named volume. Idempotent — creating
// a volume that already exists is a no-op.
func (localProvider *LocalPathStorageProvider) CreateVolume(_ context.Context, volumeName string, sizeBytes int64) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	volumePath := localProvider.volumeDirectoryPath(volumeName)
	if createError := os.MkdirAll(volumePath, 0750); createError != nil {
		return fmt.Errorf("creating volume directory %s: %w", volumePath, createError)
	}

	if _, alreadyTracked := localProvider.volumes[volumeName]; !alreadyTracked {
		localProvider.volumes[volumeName] = &localVolume{sizeBytes: sizeBytes}
	}
	return nil
}

// DeleteVolume removes the directory for a volume. Returns an error if the
// volume is currently attached. Idempotent — deleting a nonexistent volume
// is a no-op.
func (localProvider *LocalPathStorageProvider) DeleteVolume(_ context.Context, volumeName string) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if isTracked && trackedVolume.attachedNodeID != "" {
		return fmt.Errorf("cannot delete volume %q: currently attached to node %q", volumeName, trackedVolume.attachedNodeID)
	}

	volumePath := localProvider.volumeDirectoryPath(volumeName)
	if removeError := os.RemoveAll(volumePath); removeError != nil {
		return fmt.Errorf("removing volume directory %s: %w", volumePath, removeError)
	}

	delete(localProvider.volumes, volumeName)
	return nil
}

// AttachVolume marks a volume as attached to the specified node and returns
// the host directory path. Enforces exclusive attach: returns an error if
// attached to a different node. Idempotent for the same node.
func (localProvider *LocalPathStorageProvider) AttachVolume(_ context.Context, volumeName string, nodeID string) (string, error) {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	volumePath := localProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return "", fmt.Errorf("volume %q does not exist (no directory at %s)", volumeName, volumePath)
	}

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if !isTracked {
		trackedVolume = &localVolume{}
		localProvider.volumes[volumeName] = trackedVolume
	}

	if trackedVolume.attachedNodeID != "" && trackedVolume.attachedNodeID != nodeID {
		return "", fmt.Errorf("volume %q is exclusively attached to node %q, cannot attach to node %q",
			volumeName, trackedVolume.attachedNodeID, nodeID)
	}

	trackedVolume.attachedNodeID = nodeID
	return volumePath, nil
}

// DetachVolume clears the attachment for a volume. Idempotent — detaching an
// already-detached volume is a no-op.
func (localProvider *LocalPathStorageProvider) DetachVolume(_ context.Context, volumeName string, nodeID string) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if !isTracked {
		return nil
	}

	if trackedVolume.attachedNodeID == nodeID {
		trackedVolume.attachedNodeID = ""
	}
	return nil
}

// IsAttached reports whether the named volume is currently attached.
func (localProvider *LocalPathStorageProvider) IsAttached(_ context.Context, volumeName string) (bool, string, error) {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if !isTracked || trackedVolume.attachedNodeID == "" {
		return false, "", nil
	}
	return true, trackedVolume.attachedNodeID, nil
}

// SnapshotVolume creates a copy of the volume directory as a snapshot.
func (localProvider *LocalPathStorageProvider) SnapshotVolume(_ context.Context, volumeName string, snapshotName string) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	volumePath := localProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return fmt.Errorf("volume %q does not exist", volumeName)
	}

	snapshotPath := filepath.Join(localProvider.basePath, ".snapshots", snapshotName)
	if createError := os.MkdirAll(filepath.Dir(snapshotPath), 0750); createError != nil {
		return fmt.Errorf("creating snapshot directory: %w", createError)
	}

	// cp -a preserves permissions and timestamps
	return copyDirectory(volumePath, snapshotPath)
}

// VolumeUsage returns actual disk usage for the volume directory.
func (localProvider *LocalPathStorageProvider) VolumeUsage(_ context.Context, volumeName string) (int64, int64, error) {
	localProvider.mutex.Lock()
	trackedVolume, isTracked := localProvider.volumes[volumeName]
	declaredSize := int64(0)
	if isTracked {
		declaredSize = trackedVolume.sizeBytes
	}
	localProvider.mutex.Unlock()

	volumePath := localProvider.volumeDirectoryPath(volumeName)
	if _, statError := os.Stat(volumePath); os.IsNotExist(statError) {
		return 0, 0, nil
	}

	usedBytes := calculateDirectorySize(volumePath)
	return usedBytes, declaredSize, nil
}

// ResizeVolume updates the declared capacity. Only expansion is supported.
func (localProvider *LocalPathStorageProvider) ResizeVolume(_ context.Context, volumeName string, newSizeBytes int64) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if !isTracked {
		return fmt.Errorf("volume %q does not exist", volumeName)
	}

	if newSizeBytes < trackedVolume.sizeBytes {
		return fmt.Errorf("cannot shrink volume %q from %d to %d bytes", volumeName, trackedVolume.sizeBytes, newSizeBytes)
	}

	trackedVolume.sizeBytes = newSizeBytes
	return nil
}

// ForceDetach clears the attachment regardless of which node holds it.
func (localProvider *LocalPathStorageProvider) ForceDetach(_ context.Context, volumeName string) error {
	localProvider.mutex.Lock()
	defer localProvider.mutex.Unlock()

	trackedVolume, isTracked := localProvider.volumes[volumeName]
	if !isTracked {
		return nil
	}

	trackedVolume.attachedNodeID = ""
	return nil
}

// calculateDirectorySize walks a directory tree and sums file sizes.
func calculateDirectorySize(directoryPath string) int64 {
	var totalBytes int64
	_ = filepath.Walk(directoryPath, func(_ string, fileInfo os.FileInfo, walkError error) error {
		if walkError != nil {
			return nil
		}
		if !fileInfo.IsDir() {
			totalBytes += fileInfo.Size()
		}
		return nil
	})
	return totalBytes
}

// copyDirectory recursively copies src to dst using hard links where possible.
func copyDirectory(sourceDir string, destinationDir string) error {
	return filepath.Walk(sourceDir, func(sourcePath string, fileInfo os.FileInfo, walkError error) error {
		if walkError != nil {
			return walkError
		}

		relativePath, relError := filepath.Rel(sourceDir, sourcePath)
		if relError != nil {
			return relError
		}
		destinationPath := filepath.Join(destinationDir, relativePath)

		if fileInfo.IsDir() {
			return os.MkdirAll(destinationPath, fileInfo.Mode())
		}

		if linkError := syscall.Link(sourcePath, destinationPath); linkError == nil {
			return nil
		}

		sourceContent, readError := os.ReadFile(sourcePath) // #nosec G304,G122 — path constructed from internal basePath
		if readError != nil {
			return readError
		}
		return os.WriteFile(destinationPath, sourceContent, fileInfo.Mode()) // #nosec G703 — path constructed from internal basePath
	})
}
