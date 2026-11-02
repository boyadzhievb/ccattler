// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package storage

import (
	"context"
	"testing"
)

// TestSupportsSharedAttachPerProvider verifies NFS allows ReadWriteMany while
// local-path and simulator volumes stay exclusive.
func TestSupportsSharedAttachPerProvider(t *testing.T) {
	nfsProvider, nfsError := NewNFSStorageProvider(t.TempDir())
	if nfsError != nil {
		t.Fatal(nfsError)
	}
	localProvider, localError := NewLocalPathStorageProvider(t.TempDir())
	if localError != nil {
		t.Fatal(localError)
	}

	if !nfsProvider.SupportsSharedAttach() {
		t.Error("NFS provider should support shared attach")
	}
	if localProvider.SupportsSharedAttach() {
		t.Error("local-path provider should be exclusive")
	}
	if NewSimulatorStorageProvider().SupportsSharedAttach() {
		t.Error("simulator provider should be exclusive")
	}
}

// TestNFSSharedAttachSameMountPath verifies two nodes attach one NFS volume
// and get the same mount path.
func TestNFSSharedAttachSameMountPath(t *testing.T) {
	ctx := context.Background()
	nfsProvider, providerError := NewNFSStorageProvider(t.TempDir())
	if providerError != nil {
		t.Fatal(providerError)
	}
	if createError := nfsProvider.CreateVolume(ctx, "shared-data", 0); createError != nil {
		t.Fatal(createError)
	}

	firstMountPath, firstError := nfsProvider.AttachVolume(ctx, "shared-data", "node-1")
	secondMountPath, secondError := nfsProvider.AttachVolume(ctx, "shared-data", "node-2")
	if firstError != nil || secondError != nil {
		t.Fatalf("attach errors: %v, %v", firstError, secondError)
	}
	if firstMountPath != secondMountPath {
		t.Errorf("mount paths differ: %q vs %q", firstMountPath, secondMountPath)
	}
}
