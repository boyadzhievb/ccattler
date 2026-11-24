// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyDesiredVolume returns the store path for a volume's root marker key.
// Path: desired/volume/{name}
func KeyDesiredVolume(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s", PrefixDesired, volumeName)
}

// KeyDesiredVolumeSize returns the store path for a volume's declared size.
// Path: desired/volume/{name}/size
func KeyDesiredVolumeSize(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/size", PrefixDesired, volumeName)
}

// KeyDesiredVolumePersistent returns the store path for a volume's persistence flag.
// Path: desired/volume/{name}/persistent
func KeyDesiredVolumePersistent(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/persistent", PrefixDesired, volumeName)
}

// KeyDesiredVolumeType returns the store path for a volume's storage type.
// Path: desired/volume/{name}/type
func KeyDesiredVolumeType(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/type", PrefixDesired, volumeName)
}

// KeyDesiredVolumeReplicas returns the store path for a volume's desired
// replication count. Value "1" means no replication; higher values enable
// synchronous replication to that many copies.
// Path: desired/volume/{name}/replicas
func KeyDesiredVolumeReplicas(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/replicas", PrefixDesired, volumeName)
}

// KeyDesiredServiceVolume returns the store path binding a service to a named
// volume. The value stored at this key is the mount path inside the instance.
// Path: desired/service/{serviceName}/volume/{volumeName}
func KeyDesiredServiceVolume(serviceName string, volumeName string) string {
	return fmt.Sprintf("%s/service/%s/volume/%s", PrefixDesired, serviceName, volumeName)
}

// KeyObservedVolume returns the store path for an observed volume's root marker.
// Path: observed/volume/{name}
func KeyObservedVolume(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s", PrefixObserved, volumeName)
}

// KeyObservedVolumeState returns the store path for an observed volume's lifecycle state.
// Path: observed/volume/{name}/state
func KeyObservedVolumeState(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/state", PrefixObserved, volumeName)
}

// KeyObservedVolumeNode returns the store path for the node a volume is attached to.
// Path: observed/volume/{name}/node
func KeyObservedVolumeNode(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/node", PrefixObserved, volumeName)
}

// KeyObservedVolumeInstance returns the store path for the instance a volume is mounted into.
// Path: observed/volume/{name}/instance
func KeyObservedVolumeInstance(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/instance", PrefixObserved, volumeName)
}

// KeyObservedVolumeAccess returns the store path for a volume's access mode
// (written as "shared" on first shared attach; absent means exclusive).
// Path: observed/volume/{name}/access
func KeyObservedVolumeAccess(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/access", PrefixObserved, volumeName)
}

// KeyObservedVolumeAttachment returns the store path for one instance's
// attachment of a shared volume on a node. The value is the host mount path.
// Path: observed/volume/{name}/attachment/{node}/{instance}
func KeyObservedVolumeAttachment(volumeName string, nodeID string, instanceID string) string {
	return fmt.Sprintf("%s/volume/%s/attachment/%s/%s", PrefixObserved, volumeName, nodeID, instanceID)
}

// ScanObservedVolumeNodeAttachments returns the prefix for all attachments of
// a shared volume on one node (trailing slash so node-1 never matches node-10).
// Path: observed/volume/{name}/attachment/{node}/
func ScanObservedVolumeNodeAttachments(volumeName string, nodeID string) string {
	return fmt.Sprintf("%s/volume/%s/attachment/%s/", PrefixObserved, volumeName, nodeID)
}

// KeyObservedVolumeSize returns the store path for an observed volume's size.
// Path: observed/volume/{name}/size
func KeyObservedVolumeSize(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/size", PrefixObserved, volumeName)
}

// KeyObservedVolumeMountPath returns the store path for an observed volume's mount path.
// Path: observed/volume/{name}/mount_path
func KeyObservedVolumeMountPath(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/mount_path", PrefixObserved, volumeName)
}

// KeyObservedVolumeType returns the store path for an observed volume's storage type.
// Path: observed/volume/{name}/type
func KeyObservedVolumeType(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/type", PrefixObserved, volumeName)
}

// KeyObservedVolumeHomeNode returns the store path for the node that holds a
// node-local volume's data. Written once by the agent on first attach (Flow 26).
// Path: observed/volume/{name}/home_node
func KeyObservedVolumeHomeNode(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/home_node", PrefixObserved, volumeName)
}

// KeyObservedVolumeMigrationSource returns the store path for the node a volume
// was force-detached from during migration.
// Path: observed/volume/{name}/migration_source
func KeyObservedVolumeMigrationSource(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/migration_source", PrefixObserved, volumeName)
}

// KeyObservedVolumeLastSnapshot returns the store path for a volume's most
// recent snapshot name.
// Path: observed/volume/{name}/last_snapshot
func KeyObservedVolumeLastSnapshot(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/last_snapshot", PrefixObserved, volumeName)
}

// KeyObservedVolumeUsedBytes returns the store path for a volume's current
// disk usage in bytes.
// Path: observed/volume/{name}/used_bytes
func KeyObservedVolumeUsedBytes(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/used_bytes", PrefixObserved, volumeName)
}

// KeyObservedVolumeCapacityBytes returns the store path for a volume's total
// capacity in bytes.
// Path: observed/volume/{name}/capacity_bytes
func KeyObservedVolumeCapacityBytes(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/capacity_bytes", PrefixObserved, volumeName)
}

// KeyObservedVolumeReplicaCount returns the store path for the current number
// of synchronized replicas of a volume.
// Path: observed/volume/{name}/replica_count
func KeyObservedVolumeReplicaCount(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/replica_count", PrefixObserved, volumeName)
}

// KeyObservedVolumeReplicaState returns the store path for the replication
// sync state of a volume ("synced", "syncing", "degraded").
// Path: observed/volume/{name}/replica_state
func KeyObservedVolumeReplicaState(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/replica_state", PrefixObserved, volumeName)
}

// KeyDerivedVolumePendingOperation returns the store path for a volume's
// pending storage operation (resize or snapshot). Written by the storage
// controller during Reconcile; executed post-commit by the executor.
//
// Deprecated: use KeyDerivedVolumePendingResize or KeyDerivedVolumePendingSnapshot.
// Path: derived/volume/{name}/pending_operation
func KeyDerivedVolumePendingOperation(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/pending_operation", PrefixDerived, volumeName)
}

// KeyDerivedVolumePendingResize returns the store path for a volume's pending
// resize operation. Separate from snapshot to avoid duplicate-key collision
// when a volume needs both resize and migration simultaneously.
// Path: derived/volume/{name}/pending_resize
func KeyDerivedVolumePendingResize(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/pending_resize", PrefixDerived, volumeName)
}

// KeyDerivedVolumePendingSnapshot returns the store path for a volume's pending
// snapshot operation. Separate from resize to avoid duplicate-key collision.
// Path: derived/volume/{name}/pending_snapshot
func KeyDerivedVolumePendingSnapshot(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/pending_snapshot", PrefixDerived, volumeName)
}

// KeyDerivedVolumeLastOperation returns the store path for a volume's most
// recently completed storage operation. Used for idempotency — if the
// pending operation ID matches the last operation ID, execution is skipped.
// Path: derived/volume/{name}/last_operation
func KeyDerivedVolumeLastOperation(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/last_operation", PrefixDerived, volumeName)
}
