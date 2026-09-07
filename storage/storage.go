package storage

import "context"

// StorageProvider is the pluggable interface for managing persistent volumes.
// It follows the same adapter pattern as NetworkProvider and Runtime:
// SimulatorStorageProvider for tests, with future real backends for local
// disk, NFS, or cloud block storage.
type StorageProvider interface {
	// CreateVolume ensures a volume with the given name exists. The sizeBytes
	// parameter is informational for the simulator but would be enforced by
	// real backends. Idempotent — creating an already-existing volume is a
	// no-op.
	CreateVolume(ctx context.Context, volumeName string, sizeBytes int64) error

	// DeleteVolume removes a volume. Returns an error if the volume is
	// currently attached. Idempotent — deleting a nonexistent volume is a
	// no-op.
	DeleteVolume(ctx context.Context, volumeName string) error

	// AttachVolume binds a volume to a node, making it available for mounting
	// by instances on that node. Returns the filesystem mount path. Enforces
	// exclusive attach (ReadWriteOnce): returns an error if the volume is
	// already attached to a different node. Idempotent for the same node.
	AttachVolume(ctx context.Context, volumeName string, nodeID string) (string, error)

	// DetachVolume unbinds a volume from a node. Idempotent — detaching an
	// already-detached volume is a no-op.
	DetachVolume(ctx context.Context, volumeName string, nodeID string) error

	// IsAttached reports whether a volume is currently attached and to which
	// node. Returns (false, "", nil) for unattached or unknown volumes.
	IsAttached(ctx context.Context, volumeName string) (bool, string, error)
}
