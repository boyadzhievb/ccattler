// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newFreshKeyringCodec creates a fresh-cluster keyring (legacy none) with one
// live replica report on build v-test.
func newFreshKeyringCodec(t *testing.T) (*KeyringEnvelopeCodec, store.StateStore) {
	t.Helper()
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)
	return keyringCodec, factStore
}

// TestKeyringRotationGateRefusesBeforeRetirement verifies rotation is refused
// in the present and migrating states, and passes once the legacy key is retired.
func TestKeyringRotationGateRefusesBeforeRetirement(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 2)
	unmet, checkError := fixture.keyringCodec.CheckKeyringRotationGate(ctx, "v-test")
	if checkError != nil || len(unmet) != 1 || !strings.Contains(unmet[0], "keyring is present") {
		t.Fatalf("present: %v, %v", unmet, checkError)
	}
	if _, startError := fixture.keyringCodec.StartKeyringRotation(ctx, "user:admin"); startError == nil {
		t.Fatal("StartKeyringRotation passed with keyring/legacy present")
	}

	completeTestMigration(t, fixture)
	unmet, _ = fixture.keyringCodec.CheckKeyringRotationGate(ctx, "v-test")
	if len(unmet) != 1 || !strings.Contains(unmet[0], "keyring is migrating") {
		t.Fatalf("migrating: %v", unmet)
	}

	fixture.factStore.Put(ctx, types.KeyObservedNodeLegacyKeyFile("worker-1"), []byte(types.LegacyKeyFileAbsent))
	if retireError := fixture.keyringCodec.RetireLegacyKey(ctx); retireError != nil {
		t.Fatal(retireError)
	}
	fixture.keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)
	if unmet, _ = fixture.keyringCodec.CheckKeyringRotationGate(ctx, "v-test"); len(unmet) != 0 {
		t.Fatalf("retired: %v", unmet)
	}
}

// TestKeyringRotationGateChecksReplicas verifies a replica on another build or
// with another active KEK blocks the gate, and that no report blocks it too.
func TestKeyringRotationGateChecksReplicas(t *testing.T) {
	ctx := context.Background()
	keyringCodec, factStore := newFreshKeyringCodec(t)
	if unmet, _ := keyringCodec.CheckKeyringRotationGate(ctx, "v-test"); len(unmet) != 0 {
		t.Fatalf("fresh cluster: %v", unmet)
	}
	if unmet, _ := keyringCodec.CheckKeyringRotationGate(ctx, "v-next"); len(unmet) != 1 || !strings.Contains(unmet[0], "replica cp-1") {
		t.Fatalf("other build: %v", unmet)
	}
	staleReport, _ := json.Marshal(keyringReplicaReport{Version: "v-test", ActiveKEKVersion: 7, LegacyKeyState: LegacyKeyNone, RootCheck: keyringReplicaReportRootCheckOK})
	factStore.Put(ctx, types.KeyObservedKeyringReplica("cp-2"), staleReport)
	if unmet, _ := keyringCodec.CheckKeyringRotationGate(ctx, "v-test"); len(unmet) != 1 || !strings.Contains(unmet[0], "replica cp-2") {
		t.Fatalf("stale KEK report: %v", unmet)
	}
	_ = factStore.Delete(ctx, types.KeyObservedKeyringReplica("cp-1"))
	_ = factStore.Delete(ctx, types.KeyObservedKeyringReplica("cp-2"))
	if unmet, _ := keyringCodec.CheckKeyringRotationGate(ctx, "v-test"); len(unmet) != 1 || !strings.Contains(unmet[0], "no API replica") {
		t.Fatalf("no reports: %v", unmet)
	}
}

// TestStartKeyringRotationRecordsIntentOnce verifies the intent and status
// name active + 1, the keyring itself is untouched, and a second start (also
// a concurrent one) is refused while the first rotation runs.
func TestStartKeyringRotationRecordsIntentOnce(t *testing.T) {
	ctx := context.Background()
	keyringCodec, factStore := newFreshKeyringCodec(t)
	activeBefore, _ := factStore.Get(ctx, types.KeyKeyringActive)

	var waitGroup sync.WaitGroup
	startResults := make([]error, 2)
	for requestIndex := range startResults {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, startResults[requestIndex] = keyringCodec.StartKeyringRotation(ctx, "user:admin")
		}()
	}
	waitGroup.Wait()
	if (startResults[0] == nil) == (startResults[1] == nil) {
		t.Fatalf("want exactly one start to win: %v", startResults)
	}

	var rotationIntent keyringRotationIntent
	intentFact, _ := factStore.Get(ctx, types.KeyIntentKeyringRotation)
	if json.Unmarshal(intentFact.Value, &rotationIntent) != nil || rotationIntent.TargetVersion != 2 || rotationIntent.RequestedBy != "user:admin" {
		t.Fatalf("intent = %s", intentFact.Value)
	}
	var rotationStatus KeyringRotationStatus
	statusFact, _ := factStore.Get(ctx, types.KeyObservedKeyringRotation)
	if json.Unmarshal(statusFact.Value, &rotationStatus) != nil || rotationStatus.State != KeyringRotationInProgress || rotationStatus.TargetVersion != 2 {
		t.Fatalf("status = %s", statusFact.Value)
	}
	if activeAfter, _ := factStore.Get(ctx, types.KeyKeyringActive); string(activeAfter.Value) != string(activeBefore.Value) {
		t.Fatalf("keyring/active changed by the gate: %s → %s", activeBefore.Value, activeAfter.Value)
	}
	if unmet, _ := keyringCodec.CheckKeyringRotationGate(ctx, "v-test"); len(unmet) != 1 || !strings.Contains(unmet[0], "already running") {
		t.Fatalf("gate during a rotation: %v", unmet)
	}
}
