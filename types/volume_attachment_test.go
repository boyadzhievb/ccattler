// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"context"
	"reflect"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

// TestSharedVolumeAttachmentsRoundTrip verifies the read and list codecs parse
// the access marker and per-instance attachments, sorted, and that the
// summary reports every node and instance.
func TestSharedVolumeAttachmentsRoundTrip(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()
	ctx := context.Background()

	WriteObservedVolume(ctx, stateStore, Volume{Name: "shared-data", Size: "1Gi", State: VolumeAttached})
	stateStore.Put(ctx, KeyObservedVolumeAccess("shared-data"), []byte(VolumeAccessShared))
	stateStore.Put(ctx, KeyObservedVolumeAttachment("shared-data", "node-2", "inst-b"), []byte("/mnt/nfs/shared-data"))
	stateStore.Put(ctx, KeyObservedVolumeAttachment("shared-data", "node-1", "inst-c"), []byte("/mnt/nfs/shared-data"))
	stateStore.Put(ctx, KeyObservedVolumeAttachment("shared-data", "node-1", "inst-a"), []byte("/mnt/nfs/shared-data"))
	WriteObservedVolume(ctx, stateStore, Volume{Name: "pgdata", Size: "1Gi", State: VolumeAttached, Node: "node-3", Instance: "inst-p", MountPath: "/mnt/local/pgdata"})

	expectedAttachments := []VolumeAttachment{
		{Node: "node-1", Instance: "inst-a", MountPath: "/mnt/nfs/shared-data"},
		{Node: "node-1", Instance: "inst-c", MountPath: "/mnt/nfs/shared-data"},
		{Node: "node-2", Instance: "inst-b", MountPath: "/mnt/nfs/shared-data"},
	}

	readVolume, readError := ReadObservedVolume(ctx, stateStore, "shared-data")
	if readError != nil {
		t.Fatal(readError)
	}
	if readVolume.Access != VolumeAccessShared || !reflect.DeepEqual(readVolume.Attachments, expectedAttachments) {
		t.Errorf("read: access=%s attachments=%+v", readVolume.Access, readVolume.Attachments)
	}

	listedVolumes, listError := ListObservedVolumes(ctx, stateStore)
	if listError != nil {
		t.Fatal(listError)
	}
	volumesByName := make(map[string]Volume)
	for _, listedVolume := range listedVolumes {
		volumesByName[listedVolume.Name] = listedVolume
	}
	if !reflect.DeepEqual(volumesByName["shared-data"].Attachments, expectedAttachments) {
		t.Errorf("list: attachments=%+v", volumesByName["shared-data"].Attachments)
	}

	nodeIDs, instanceIDs, mountPaths := volumesByName["shared-data"].AttachmentSummary()
	if !reflect.DeepEqual(nodeIDs, []string{"node-1", "node-2"}) ||
		!reflect.DeepEqual(instanceIDs, []string{"inst-a", "inst-b", "inst-c"}) ||
		!reflect.DeepEqual(mountPaths, []string{"/mnt/nfs/shared-data"}) {
		t.Errorf("shared summary: nodes=%v instances=%v mounts=%v", nodeIDs, instanceIDs, mountPaths)
	}

	exclusiveVolume := volumesByName["pgdata"]
	if exclusiveVolume.Access != VolumeAccessExclusive {
		t.Errorf("exclusive access = %q, want exclusive", exclusiveVolume.Access)
	}
	nodeIDs, instanceIDs, mountPaths = exclusiveVolume.AttachmentSummary()
	if !reflect.DeepEqual(nodeIDs, []string{"node-3"}) || !reflect.DeepEqual(instanceIDs, []string{"inst-p"}) ||
		!reflect.DeepEqual(mountPaths, []string{"/mnt/local/pgdata"}) {
		t.Errorf("exclusive summary: nodes=%v instances=%v mounts=%v", nodeIDs, instanceIDs, mountPaths)
	}
}
