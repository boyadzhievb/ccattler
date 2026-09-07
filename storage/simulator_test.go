package storage

import (
	"context"
	"sync"
	"testing"
)

// TestCreateVolumeAddsToState verifies that creating a volume makes it
// available for subsequent attach operations.
func TestCreateVolumeAddsToState(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	err := simulatorStorageProvider.CreateVolume(ctx, "pgdata", 107374182400)
	if err != nil {
		t.Fatalf("CreateVolume failed: %v", err)
	}

	attached, nodeID, err := simulatorStorageProvider.IsAttached(ctx, "pgdata")
	if err != nil {
		t.Fatalf("IsAttached failed: %v", err)
	}
	if attached {
		t.Errorf("newly created volume should not be attached, got node=%s", nodeID)
	}
}

// TestCreateVolumeIdempotent verifies that creating a volume that already
// exists is a no-op.
func TestCreateVolumeIdempotent(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	err := simulatorStorageProvider.CreateVolume(ctx, "pgdata", 200)
	if err != nil {
		t.Fatalf("second CreateVolume should be no-op, got: %v", err)
	}
}

// TestDeleteVolumeRemovesFromState verifies that deleting a volume makes it
// unknown to IsAttached and prevents subsequent attach.
func TestDeleteVolumeRemovesFromState(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	err := simulatorStorageProvider.DeleteVolume(ctx, "pgdata")
	if err != nil {
		t.Fatalf("DeleteVolume failed: %v", err)
	}

	_, err = simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")
	if err == nil {
		t.Error("expected error attaching deleted volume")
	}
}

// TestDeleteVolumeWhileAttachedReturnsError verifies that deleting an
// attached volume is rejected.
func TestDeleteVolumeWhileAttachedReturnsError(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")

	err := simulatorStorageProvider.DeleteVolume(ctx, "pgdata")
	if err == nil {
		t.Error("expected error deleting attached volume")
	}
}

// TestAttachVolumeReturnsMountPath verifies that attaching a volume returns
// a deterministic mount path.
func TestAttachVolumeReturnsMountPath(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	mountPath, err := simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")
	if err != nil {
		t.Fatalf("AttachVolume failed: %v", err)
	}
	if mountPath != "/mnt/volumes/pgdata" {
		t.Errorf("mount path = %q, want /mnt/volumes/pgdata", mountPath)
	}

	attached, attachedNode, _ := simulatorStorageProvider.IsAttached(ctx, "pgdata")
	if !attached || attachedNode != "node-1" {
		t.Errorf("IsAttached = (%v, %s), want (true, node-1)", attached, attachedNode)
	}
}

// TestDetachVolumeReleasesNode verifies that detaching a volume makes it
// available for reattachment.
func TestDetachVolumeReleasesNode(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")

	err := simulatorStorageProvider.DetachVolume(ctx, "pgdata", "node-1")
	if err != nil {
		t.Fatalf("DetachVolume failed: %v", err)
	}

	attached, _, _ := simulatorStorageProvider.IsAttached(ctx, "pgdata")
	if attached {
		t.Error("volume should not be attached after detach")
	}

	mountPath, err := simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-2")
	if err != nil {
		t.Fatalf("reattach to node-2 failed: %v", err)
	}
	if mountPath != "/mnt/volumes/pgdata" {
		t.Errorf("mount path = %q, want /mnt/volumes/pgdata", mountPath)
	}
}

// TestExclusiveAttachEnforced verifies that attaching a volume to a different
// node while it is already attached returns an error (ReadWriteOnce).
func TestExclusiveAttachEnforced(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")

	_, err := simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-2")
	if err == nil {
		t.Error("expected error for exclusive attach violation")
	}
}

// TestAttachSameNodeIdempotent verifies that attaching a volume to the same
// node it is already attached to is a no-op.
func TestAttachSameNodeIdempotent(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")

	mountPath, err := simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")
	if err != nil {
		t.Fatalf("idempotent attach failed: %v", err)
	}
	if mountPath != "/mnt/volumes/pgdata" {
		t.Errorf("mount path = %q, want /mnt/volumes/pgdata", mountPath)
	}
}

// TestAttachUnknownVolumeReturnsError verifies that attaching a volume
// that was never created returns an error.
func TestAttachUnknownVolumeReturnsError(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	_, err := simulatorStorageProvider.AttachVolume(ctx, "nonexistent", "node-1")
	if err == nil {
		t.Error("expected error attaching unknown volume")
	}
}

// TestConcurrentAttachDetachSafe verifies that concurrent attach and detach
// operations do not cause data races.
func TestConcurrentAttachDetachSafe(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	for volumeIndex := 0; volumeIndex < 10; volumeIndex++ {
		volumeName := "vol-" + string(rune('a'+volumeIndex))
		simulatorStorageProvider.CreateVolume(ctx, volumeName, 100)
	}

	var waitGroup sync.WaitGroup
	for goroutineIndex := 0; goroutineIndex < 10; goroutineIndex++ {
		volumeName := "vol-" + string(rune('a'+goroutineIndex))
		nodeID := "node-1"
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			simulatorStorageProvider.AttachVolume(ctx, volumeName, nodeID)
			simulatorStorageProvider.DetachVolume(ctx, volumeName, nodeID)
			simulatorStorageProvider.AttachVolume(ctx, volumeName, nodeID)
		}()
	}
	waitGroup.Wait()
}

// TestForceDetachReleasesRegardlessOfNode verifies that ForceDetach clears
// the attachment without requiring the node ID.
func TestForceDetachReleasesRegardlessOfNode(t *testing.T) {
	simulatorStorageProvider := NewSimulatorStorageProvider()
	ctx := context.Background()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-1")

	err := simulatorStorageProvider.ForceDetach(ctx, "pgdata")
	if err != nil {
		t.Fatalf("ForceDetach failed: %v", err)
	}

	attached, _, _ := simulatorStorageProvider.IsAttached(ctx, "pgdata")
	if attached {
		t.Error("volume should not be attached after force detach")
	}

	mountPath, err := simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-2")
	if err != nil {
		t.Fatalf("attach to node-2 after force detach failed: %v", err)
	}
	if mountPath != "/mnt/volumes/pgdata" {
		t.Errorf("mount path = %q, want /mnt/volumes/pgdata", mountPath)
	}
}
