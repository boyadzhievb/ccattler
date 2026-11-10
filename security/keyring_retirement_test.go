// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// completeTestMigration runs the whole legacy key migration on the fixture.
func completeTestMigration(t *testing.T, fixture *migrationTestFixture) {
	t.Helper()
	ctx := context.Background()
	if startError := fixture.keyringCodec.StartLegacyMigration(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	facts, _ := scanStorePrefixes(ctx, fixture.factStore, SecretStorePrefix, CredentialStorePrefix)
	migrationBatch, planError := fixture.keyringCodec.PlanLegacyMigrationBatch(ctx, facts, len(facts))
	if planError != nil || migrationBatch.RemainingV1Count != 0 {
		t.Fatalf("plan: %v, remaining %d", planError, migrationBatch.RemainingV1Count)
	}
	for _, rewrite := range migrationBatch.Rewrites {
		fixture.factStore.Put(ctx, rewrite.Key, rewrite.Value)
	}
	completedJSON, _ := json.Marshal(KeyringMigrationStatus{State: KeyringMigrationCompleted, Migrated: len(migrationBatch.Rewrites)})
	fixture.factStore.Put(ctx, types.KeyObservedKeyringMigration, completedJSON)
	_ = fixture.keyringCodec.Refresh(ctx)
	// The replica republishes its report every few seconds in production.
	fixture.keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)
}

// TestLegacyRetirementGateConditions verifies each retirement condition:
// migration completed, no plaintext gate key, every non-replica node without
// the legacy file; an API replica's own file does not block retirement.
func TestLegacyRetirementGateConditions(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 3)
	unmet, _ := fixture.keyringCodec.CheckLegacyRetirementGate(ctx)
	if !strings.Contains(strings.Join(unmet, "; "), "migration is not_started") {
		t.Fatalf("before migration: %v", unmet)
	}

	completeTestMigration(t, fixture)
	unmet, _ = fixture.keyringCodec.CheckLegacyRetirementGate(ctx)
	if !strings.Contains(strings.Join(unmet, "; "), "worker-1") {
		t.Fatalf("worker without legacy_key_file report not refused: %v", unmet)
	}
	fixture.factStore.Put(ctx, types.KeyObservedNodeLegacyKeyFile("worker-1"), []byte(types.LegacyKeyFileAbsent))
	fixture.factStore.Put(ctx, types.KeyObservedNodeState("cp-1"), []byte(types.NodeAlive))
	fixture.factStore.Put(ctx, types.KeyObservedNodeLegacyKeyFile("cp-1"), []byte(types.LegacyKeyFilePresent))
	fixture.factStore.Put(ctx, types.ScanDerivedRoutes+"web/tls/key", []byte("-----BEGIN EC PRIVATE KEY-----"))
	unmet, _ = fixture.keyringCodec.CheckLegacyRetirementGate(ctx)
	if len(unmet) != 1 || !strings.Contains(unmet[0], "plaintext gate key") {
		t.Fatalf("want only the plaintext gate key condition (cp-1 is a replica): %v", unmet)
	}
	fixture.factStore.Delete(ctx, types.ScanDerivedRoutes+"web/tls/key")
	if unmet, _ = fixture.keyringCodec.CheckLegacyRetirementGate(ctx); len(unmet) != 0 {
		t.Fatalf("ready cluster refused: %v", unmet)
	}
}

// TestRetireLegacyKeyRefusesV1AndDropsKey verifies retirement drops the
// legacy key, refuses v1, closes the gate, and a replica restarted with the
// old file present ignores it.
func TestRetireLegacyKeyRefusesV1AndDropsKey(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 1)
	v1Envelope, _ := SealEnvelope(ctx, fixture.legacyKeyProvider, []byte("old"))
	completeTestMigration(t, fixture)
	if retireError := fixture.keyringCodec.RetireLegacyKey(ctx); retireError != nil {
		t.Fatal(retireError)
	}
	if _, currentLegacyKeyProvider := fixture.keyringCodec.currentState(); currentLegacyKeyProvider != nil {
		t.Error("legacy key still held after retirement")
	}
	if _, openError := fixture.keyringCodec.OpenEnvelopeForKey(ctx, "secrets/db", v1Envelope); openError == nil {
		t.Error("v1 envelope opened after retirement")
	}
	unmet, _ := fixture.keyringCodec.CheckLegacyRetirementGate(ctx)
	if !strings.Contains(strings.Join(unmet, "; "), "keyring is retired") {
		t.Errorf("second retirement not refused: %v", unmet)
	}
	restartedCodec, codecError := NewKeyringEnvelopeCodec(ctx, fixture.factStore, fixture.keyringCodec.rootKeyProvider, fixture.legacyKeyProvider)
	if codecError != nil {
		t.Fatalf("replica restart with the retired file present: %v", codecError)
	}
	if _, currentLegacyKeyProvider := restartedCodec.currentState(); currentLegacyKeyProvider != nil {
		t.Error("restarted replica loaded the retired legacy key")
	}
	secretStore := NewSecretStore(fixture.factStore, restartedCodec)
	if names, _ := secretStore.ListSecrets(ctx); len(names) > 0 {
		if _, getError := secretStore.GetSecret(ctx, names[0]); getError != nil {
			t.Errorf("migrated secret unreadable after retirement: %v", getError)
		}
	}
}
