// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newUpgradedKeyringCluster writes v1SecretCount v1 secrets with a legacy key,
// opens the keyring (not_started) and returns the store, the codec and the
// legacy key so a test can build a second replica.
func newUpgradedKeyringCluster(t *testing.T, v1SecretCount int) (*store.MemoryStore, *security.KeyringEnvelopeCodec, security.KeyProvider, security.KeyProvider) {
	t.Helper()
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	legacyKey, _ := security.GenerateMasterKey()
	legacyStore, _ := security.NewSecretStoreWithMasterKey(factStore, legacyKey)
	for secretIndex := 0; secretIndex < v1SecretCount; secretIndex++ {
		if putError := legacyStore.PutSecret(ctx, fmt.Sprintf("app-%03d", secretIndex), []byte(fmt.Sprintf("value-%03d", secretIndex))); putError != nil {
			t.Fatal(putError)
		}
	}
	legacyKeyProvider, _ := security.NewLocalKeyProvider(legacyKey)
	rootKey, _ := security.GenerateMasterKey()
	rootKeyProvider, _ := security.NewLocalKeyProvider(rootKey)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, legacyKeyProvider)
	if codecError != nil {
		t.Fatal(codecError)
	}
	return factStore, keyringCodec, rootKeyProvider, legacyKeyProvider
}

// readMigrationStatus parses observed/keyring/migration from the store.
func readMigrationStatus(t *testing.T, factStore store.StateStore) security.KeyringMigrationStatus {
	t.Helper()
	var migrationStatus security.KeyringMigrationStatus
	migrationFact, getError := factStore.Get(context.Background(), types.KeyObservedKeyringMigration)
	if getError != nil {
		t.Fatal(getError)
	}
	if unmarshalError := json.Unmarshal(migrationFact.Value, &migrationStatus); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	return migrationStatus
}

// assertAllSecretsV2AndReadable checks every secret is v2 and still holds its value.
func assertAllSecretsV2AndReadable(t *testing.T, factStore store.StateStore, keyringCodec *security.KeyringEnvelopeCodec, v1SecretCount int) {
	t.Helper()
	ctx := context.Background()
	secretStore := security.NewSecretStore(factStore, keyringCodec)
	for secretIndex := 0; secretIndex < v1SecretCount; secretIndex++ {
		secretName := fmt.Sprintf("app-%03d", secretIndex)
		storedFact, _ := factStore.Get(ctx, security.SecretStorePrefix+secretName)
		envelope, _ := base64.StdEncoding.DecodeString(string(storedFact.Value))
		if len(envelope) == 0 || envelope[0] == 0 {
			t.Fatalf("%s is still v1", secretName)
		}
		if value, getError := secretStore.GetSecret(ctx, secretName); getError != nil || string(value) != fmt.Sprintf("value-%03d", secretIndex) {
			t.Fatalf("%s after migration = %q, %v", secretName, value, getError)
		}
	}
}

// TestKeyringMigrationIdleUntilGatePassed verifies the controller writes
// nothing while the migration has not started.
func TestKeyringMigrationIdleUntilGatePassed(t *testing.T) {
	factStore, keyringCodec, _, _ := newUpgradedKeyringCluster(t, 3)
	facts, _ := factStore.Scan(context.Background(), "")
	store.SortFacts(facts)
	changes, reconcileError := NewKeyringMigrationController(keyringCodec).Reconcile(context.Background(), facts)
	if reconcileError != nil || len(changes) != 0 {
		t.Fatalf("changes before the gate = %d, %v", len(changes), reconcileError)
	}
}

// TestKeyringMigrationCompletesThroughRunner verifies the controller, run by
// the real runner, migrates more secrets than one batch, every value is
// unchanged and v2, and the status reaches completed with count 0.
func TestKeyringMigrationCompletesThroughRunner(t *testing.T) {
	const v1SecretCount = 120
	factStore, keyringCodec, _, _ := newUpgradedKeyringCluster(t, v1SecretCount)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if startError := keyringCodec.StartLegacyMigration(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	migrationRunner := NewRunner(factStore, NewKeyringMigrationController(keyringCodec))
	migrationRunner.SetDebounce(0)
	go func() { _ = migrationRunner.Run(ctx) }()

	waitForStoreValue(t, factStore, types.KeyObservedKeyringMigration, func(value string) bool {
		return strings.Contains(value, `"state":"completed"`)
	})
	migrationStatus := readMigrationStatus(t, factStore)
	if migrationStatus.LegacyV1EnvelopeCount != 0 || migrationStatus.Migrated != v1SecretCount || migrationStatus.UnreadableCount != 0 {
		t.Errorf("final status = %+v", migrationStatus)
	}
	assertAllSecretsV2AndReadable(t, factStore, keyringCodec, v1SecretCount)
}

// TestKeyringMigrationResumesAfterCrash verifies a batch committed by one
// replica is continued by a fresh replica (new codec, new controller) that
// only sees the store, and the migration completes.
func TestKeyringMigrationResumesAfterCrash(t *testing.T) {
	const v1SecretCount = 70
	ctx := context.Background()
	factStore, firstCodec, rootKeyProvider, legacyKeyProvider := newUpgradedKeyringCluster(t, v1SecretCount)
	_ = firstCodec.StartLegacyMigration(ctx, "user:admin")
	firstRunner := NewRunner(factStore, NewKeyringMigrationController(firstCodec))
	if cycleError := firstRunner.executeReconciliationCycle(ctx, NewKeyringMigrationController(firstCodec), nil); cycleError != nil {
		t.Fatal(cycleError)
	}
	if partial := readMigrationStatus(t, factStore); partial.State != security.KeyringMigrationInProgress || partial.Migrated != keyringMigrationBatchSize {
		t.Fatalf("after one cycle = %+v, want in_progress with %d migrated", partial, keyringMigrationBatchSize)
	}
	secondCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, legacyKeyProvider)
	if codecError != nil {
		t.Fatal(codecError)
	}
	secondController := NewKeyringMigrationController(secondCodec)
	if cycleError := NewRunner(factStore, secondController).executeReconciliationCycle(ctx, secondController, nil); cycleError != nil {
		t.Fatal(cycleError)
	}
	if final := readMigrationStatus(t, factStore); final.State != security.KeyringMigrationCompleted || final.Migrated != v1SecretCount {
		t.Fatalf("after resume = %+v", final)
	}
	assertAllSecretsV2AndReadable(t, factStore, secondCodec, v1SecretCount)
}

// TestKeyringMigrationReportsUnreadableAndNeverCompletes verifies a v1
// envelope the legacy key cannot open is counted, left in place, and keeps
// the migration in_progress (retirement stays blocked).
func TestKeyringMigrationReportsUnreadableAndNeverCompletes(t *testing.T) {
	ctx := context.Background()
	factStore, keyringCodec, _, _ := newUpgradedKeyringCluster(t, 2)
	foreignKey, _ := security.GenerateMasterKey()
	foreignStore, _ := security.NewSecretStoreWithMasterKey(factStore, foreignKey)
	_ = foreignStore.PutSecret(ctx, "written-by-other-replica", []byte("lost"))
	unreadableBefore, _ := factStore.Get(ctx, security.SecretStorePrefix+"written-by-other-replica")
	_ = keyringCodec.StartLegacyMigration(ctx, "user:admin")
	migrationController := NewKeyringMigrationController(keyringCodec)
	migrationRunner := NewRunner(factStore, migrationController)
	for cycle := 0; cycle < 3; cycle++ {
		if cycleError := migrationRunner.executeReconciliationCycle(ctx, migrationController, nil); cycleError != nil {
			t.Fatal(cycleError)
		}
	}
	migrationStatus := readMigrationStatus(t, factStore)
	if migrationStatus.State != security.KeyringMigrationInProgress || migrationStatus.UnreadableCount != 1 || migrationStatus.LegacyV1EnvelopeCount != 1 {
		t.Errorf("status = %+v, want in_progress with 1 unreadable", migrationStatus)
	}
	if unreadableAfter, _ := factStore.Get(ctx, security.SecretStorePrefix+"written-by-other-replica"); string(unreadableAfter.Value) != string(unreadableBefore.Value) {
		t.Error("unreadable envelope was modified")
	}
}

// TestKeyringMigrationReopensForLateV1Envelope verifies code-review finding 4:
// a v1 envelope written after completion (e.g. by a replica that had not
// refreshed yet) re-opens the migration and is re-encrypted.
func TestKeyringMigrationReopensForLateV1Envelope(t *testing.T) {
	ctx := context.Background()
	factStore, keyringCodec, _, legacyKeyProvider := newUpgradedKeyringCluster(t, 2)
	_ = keyringCodec.StartLegacyMigration(ctx, "user:admin")
	migrationController := NewKeyringMigrationController(keyringCodec)
	migrationRunner := NewRunner(factStore, migrationController)
	_ = migrationRunner.executeReconciliationCycle(ctx, migrationController, nil)
	if status := readMigrationStatus(t, factStore); status.State != security.KeyringMigrationCompleted {
		t.Fatalf("first run = %+v", status)
	}
	lateEnvelope, _ := security.SealEnvelope(ctx, legacyKeyProvider, []byte("late-value"))
	factStore.Put(ctx, security.SecretStorePrefix+"late", []byte(base64.StdEncoding.EncodeToString(lateEnvelope)))
	_ = migrationRunner.executeReconciliationCycle(ctx, migrationController, nil)
	if status := readMigrationStatus(t, factStore); status.State != security.KeyringMigrationCompleted || status.Migrated != 3 {
		t.Fatalf("after the late v1 envelope = %+v, want completed with 3 migrated", status)
	}
	if value, getError := security.NewSecretStore(factStore, keyringCodec).GetSecret(ctx, "late"); getError != nil || string(value) != "late-value" {
		t.Errorf("late secret = %q, %v", value, getError)
	}
}
