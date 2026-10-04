// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestStatefulVolumeCreatesPerOrdinalVolumes verifies that a stateful service
// with 3 instances and a volume mount creates per-ordinal volume declarations.
func TestStatefulVolumeCreatesPerOrdinalVolumes(t *testing.T) {
	volumeController := NewStatefulVolumeController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "3"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyDesiredServiceVolume("postgres", "pgdata"), "/var/lib/postgresql/data"),
		kv(types.KeyDesiredVolume("pgdata"), ""),
		kv(types.KeyDesiredVolumeSize("pgdata"), "100Gi"),
		kv(types.KeyDesiredVolumePersistent("pgdata"), "true"),
	)

	changes, reconcileErr := volumeController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// 3 ordinals × (marker + size + persistent) = 9 changes
	if len(changes) != 9 {
		t.Fatalf("expected 9 changes (3 per-ordinal volumes × 3 keys), got %d", len(changes))
	}

	changeMap := changesByKeyStateful(changes)
	for ordinal := 0; ordinal < 3; ordinal++ {
		volumeName := "postgres-" + itoaStateful(ordinal) + "-pgdata"
		if _, exists := changeMap[types.KeyDesiredVolume(volumeName)]; !exists {
			t.Errorf("missing volume marker for %s", volumeName)
		}
		sizeChange, sizeExists := changeMap[types.KeyDesiredVolumeSize(volumeName)]
		if !sizeExists {
			t.Errorf("missing volume size for %s", volumeName)
		} else if string(sizeChange.Value) != "100Gi" {
			t.Errorf("volume %s size: got %q, want %q", volumeName, sizeChange.Value, "100Gi")
		}
		persistChange, persistExists := changeMap[types.KeyDesiredVolumePersistent(volumeName)]
		if !persistExists {
			t.Errorf("missing volume persistent for %s", volumeName)
		} else if string(persistChange.Value) != "true" {
			t.Errorf("volume %s persistent: got %q, want %q", volumeName, persistChange.Value, "true")
		}
	}
}

// TestStatefulVolumeSkipsExistingPerOrdinalVolumes verifies that already-
// existing per-ordinal volumes are not recreated.
func TestStatefulVolumeSkipsExistingPerOrdinalVolumes(t *testing.T) {
	volumeController := NewStatefulVolumeController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "2"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyDesiredServiceVolume("postgres", "pgdata"), "/var/lib/postgresql/data"),
		kv(types.KeyDesiredVolume("pgdata"), ""),
		kv(types.KeyDesiredVolumeSize("pgdata"), "50Gi"),
		kv(types.KeyDesiredVolumePersistent("pgdata"), "true"),
		// Ordinal 0 volume already exists
		kv(types.KeyDesiredVolume("postgres-0-pgdata"), ""),
		kv(types.KeyDesiredVolumeSize("postgres-0-pgdata"), "50Gi"),
		kv(types.KeyDesiredVolumePersistent("postgres-0-pgdata"), "true"),
	)

	changes, reconcileErr := volumeController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// Only ordinal 1 volume should be created (3 keys)
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (1 new per-ordinal volume), got %d", len(changes))
	}

	changeMap := changesByKeyStateful(changes)
	if _, exists := changeMap[types.KeyDesiredVolume("postgres-1-pgdata")]; !exists {
		t.Error("missing volume marker for postgres-1-pgdata")
	}
}

// TestStatefulVolumeSkipsNonStatefulServices verifies that non-stateful
// services do not get per-ordinal volumes.
func TestStatefulVolumeSkipsNonStatefulServices(t *testing.T) {
	volumeController := NewStatefulVolumeController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "3"),
		kv(types.KeyDesiredServiceVolume("web", "cache"), "/tmp/cache"),
		kv(types.KeyDesiredVolume("cache"), ""),
		kv(types.KeyDesiredVolumeSize("cache"), "10Gi"),
	)

	changes, reconcileErr := volumeController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for non-stateful service, got %d", len(changes))
	}
}

// TestStatefulVolumeDefaultsPersistentTrue verifies that per-ordinal volumes
// default to persistent=true when the template doesn't specify.
func TestStatefulVolumeDefaultsPersistentTrue(t *testing.T) {
	volumeController := NewStatefulVolumeController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "1"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyDesiredServiceVolume("postgres", "data"), "/data"),
		kv(types.KeyDesiredVolume("data"), ""),
	)

	changes, reconcileErr := volumeController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	changeMap := changesByKeyStateful(changes)
	persistChange, exists := changeMap[types.KeyDesiredVolumePersistent("postgres-0-data")]
	if !exists {
		t.Fatal("missing persistent fact for postgres-0-data")
	}
	if string(persistChange.Value) != "true" {
		t.Errorf("persistent default: got %q, want %q", persistChange.Value, "true")
	}
}

// TestStatefulVolumeControllerInterface verifies that StatefulVolumeController
// satisfies the Controller interface.
func TestStatefulVolumeControllerInterface(t *testing.T) {
	volumeController := NewStatefulVolumeController()
	var _ Controller = volumeController

	if volumeController.Name() != "stateful-volume" {
		t.Errorf("name: got %q, want %q", volumeController.Name(), "stateful-volume")
	}
	if len(volumeController.Watch()) != 3 {
		t.Errorf("expected 3 watch prefixes, got %d", len(volumeController.Watch()))
	}
}

// changesByKeyStateful groups changes into a map keyed by their store key for
// easy lookup in assertions. (Named differently to avoid conflict with
// storage_test.go's changesByKey.)
func changesByKeyStateful(changes []Change) map[string]Change {
	result := make(map[string]Change, len(changes))
	for _, change := range changes {
		result[change.Key] = change
	}
	return result
}

// itoaStateful converts an int to a string for test assertions.
func itoaStateful(value int) string {
	return fmt.Sprintf("%d", value)
}
