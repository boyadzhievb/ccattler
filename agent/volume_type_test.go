// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestStorageProviderForVolumeUsesDesiredTypeUntilObservedIsRecorded(testContext *testing.T) {
	memoryStore := store.NewMemoryStore()
	ctx := context.Background()
	nodeAgent := New("node-1", memoryStore, runtime.NewSimulatorRuntime())
	defaultProvider, _ := storage.NewLocalPathStorageProvider(testContext.TempDir())
	localProvider, _ := storage.NewLocalPathStorageProvider(testContext.TempDir())
	nodeAgent.SetStorageProvider(defaultProvider)
	nodeAgent.SetLocalStorageProvider(localProvider)

	if nodeAgent.storageProviderForVolume(ctx, "pgdata") != defaultProvider {
		testContext.Fatal("a volume without any type uses the default provider")
	}
	_, _ = memoryStore.Put(ctx, types.KeyDesiredVolumeType("pgdata"), []byte("local"))
	if nodeAgent.storageProviderForVolume(ctx, "pgdata") != localProvider {
		testContext.Fatal("desired type local must route to the local provider before the observed type exists")
	}
	_, _ = memoryStore.Put(ctx, types.KeyObservedVolumeType("pgdata"), []byte("nfs"))
	if nodeAgent.storageProviderForVolume(ctx, "pgdata") != defaultProvider {
		testContext.Fatal("a recorded observed type takes precedence")
	}
}
