// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// migrationTestFixture is an upgraded cluster: legacy key, v1 secrets, a
// keyring in not_started, one live replica report and one v2-delivery node.
type migrationTestFixture struct {
	factStore         *store.MemoryStore    // factStore holds the cluster state.
	keyringCodec      *KeyringEnvelopeCodec // keyringCodec is the replica's codec.
	legacyKeyProvider KeyProvider           // legacyKeyProvider sealed the v1 secrets.
}

// newMigrationTestFixture builds the upgraded cluster with v1SecretCount secrets.
func newMigrationTestFixture(t *testing.T, v1SecretCount int) *migrationTestFixture {
	t.Helper()
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	legacyKeyProvider := newTestKeyProvider(t)
	legacyStore := NewSecretStore(factStore, legacyOnlyEnvelopeCodec{keyProvider: legacyKeyProvider})
	for secretIndex := 0; secretIndex < v1SecretCount; secretIndex++ {
		if putError := legacyStore.PutSecret(ctx, "app"+string(rune('a'+secretIndex%26))+strings.Repeat("x", secretIndex/26), []byte("value")); putError != nil {
			t.Fatal(putError)
		}
	}
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), legacyKeyProvider)
	if codecError != nil {
		t.Fatal(codecError)
	}
	keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)
	factStore.Put(ctx, EnrolledNodePrefix+"worker-1", []byte("{}"))
	factStore.Put(ctx, types.KeyObservedNodeSecretDelivery("worker-1"), []byte(types.SecretDeliveryVersion))
	return &migrationTestFixture{factStore: factStore, keyringCodec: keyringCodec, legacyKeyProvider: legacyKeyProvider}
}

// TestLegacyMigrationGateConditions verifies the gate passes only with backup
// confirmation, matching live replica reports, every node on delivery v2 and
// a migration not started yet; each refusal names the condition.
func TestLegacyMigrationGateConditions(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 1)
	confirmed := LegacyMigrationGateRequest{ConfirmBackup: true}

	if unmet, _ := fixture.keyringCodec.CheckLegacyMigrationGate(ctx, confirmed, "v-test"); len(unmet) != 0 {
		t.Fatalf("gate refused a ready cluster: %v", unmet)
	}
	testCases := []struct {
		name            string
		prepare         func()
		gateRequest     LegacyMigrationGateRequest
		buildVersion    string
		expectedMessage string
	}{
		{"no backup confirmation", func() {}, LegacyMigrationGateRequest{}, "v-test", "confirm_backup"},
		{"replica on another version", func() {}, confirmed, "v-other", "replica cp-1"},
		{"node not on delivery v2", func() {
			fixture.factStore.Put(ctx, EnrolledNodePrefix+"worker-2", []byte("{}"))
		}, confirmed, "v-test", "worker-2"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.prepare()
			unmet, checkError := fixture.keyringCodec.CheckLegacyMigrationGate(ctx, testCase.gateRequest, testCase.buildVersion)
			if checkError != nil || !strings.Contains(strings.Join(unmet, "; "), testCase.expectedMessage) {
				t.Fatalf("unmet = %v, %v; want one containing %q", unmet, checkError, testCase.expectedMessage)
			}
		})
	}
}

// TestLegacyMigrationGateRequiresReplicaReport verifies a cluster without any
// live replica report is refused.
func TestLegacyMigrationGateRequiresReplicaReport(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 1)
	fixture.factStore.Delete(ctx, types.KeyObservedKeyringReplica("cp-1"))
	unmet, _ := fixture.keyringCodec.CheckLegacyMigrationGate(ctx, LegacyMigrationGateRequest{ConfirmBackup: true}, "v-test")
	if !strings.Contains(strings.Join(unmet, "; "), "no API replica") {
		t.Fatalf("unmet = %v, want the missing replica report", unmet)
	}
}

// TestStartLegacyMigrationSwitchesWritesToV2 verifies starting the migration
// records the intent, sets in_progress, makes new writes v2 and refuses a
// second start through the gate.
func TestStartLegacyMigrationSwitchesWritesToV2(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 1)
	if startError := fixture.keyringCodec.StartLegacyMigration(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	if _, getError := fixture.factStore.Get(ctx, types.KeyIntentKeyringLegacyMigration); getError != nil {
		t.Errorf("intent not recorded: %v", getError)
	}
	if migrationState := readTestMigrationState(t, fixture.factStore); migrationState != KeyringMigrationInProgress {
		t.Errorf("state = %s, want in_progress", migrationState)
	}
	envelope, _ := fixture.keyringCodec.SealEnvelopeForKey(ctx, "secrets/new", []byte("v"))
	if !isEnvelopeV2(envelope) {
		t.Error("write after the migration start is not v2")
	}
	unmet, _ := fixture.keyringCodec.CheckLegacyMigrationGate(ctx, LegacyMigrationGateRequest{ConfirmBackup: true}, "v-test")
	if !strings.Contains(strings.Join(unmet, "; "), "already in_progress") {
		t.Errorf("second start not refused: %v", unmet)
	}
}

// TestPlanLegacyMigrationBatch verifies a batch is bounded, re-encrypts v1 as
// v2 bound to the same key with the same value, skips v2, counts unreadable
// envelopes and covers credentials (raw, not base64).
func TestPlanLegacyMigrationBatch(t *testing.T) {
	ctx := context.Background()
	fixture := newMigrationTestFixture(t, 5)
	_ = fixture.keyringCodec.StartLegacyMigration(ctx, "user:admin")
	foreignEnvelope, _ := SealEnvelope(ctx, newTestKeyProvider(t), []byte("lost"))
	fixture.factStore.Put(ctx, SecretStorePrefix+"zz-foreign", []byte(base64.StdEncoding.EncodeToString(foreignEnvelope)))
	credentialEnvelope, _ := SealEnvelope(ctx, fixture.legacyKeyProvider, []byte("cloud-token"))
	fixture.factStore.Put(ctx, CredentialStorePrefix+"inst-1/s3", credentialEnvelope)
	alreadyV2Store := NewSecretStore(fixture.factStore, fixture.keyringCodec)
	_ = alreadyV2Store.PutSecret(ctx, "already-v2", []byte("value"))

	facts, _ := scanStorePrefixes(ctx, fixture.factStore, SecretStorePrefix, CredentialStorePrefix)
	migrationBatch, planError := fixture.keyringCodec.PlanLegacyMigrationBatch(ctx, facts, 3)
	if planError != nil {
		t.Fatal(planError)
	}
	if len(migrationBatch.Rewrites) != 3 {
		t.Fatalf("rewrites = %d, want the batch limit 3", len(migrationBatch.Rewrites))
	}
	// 5 v1 secrets + 1 v1 credential + 1 unreadable = 7 v1; 3 rewritten.
	if migrationBatch.RemainingV1Count != 4 || migrationBatch.UnreadableV1Count != 1 {
		t.Errorf("remaining = %d, unreadable = %d; want 4, 1", migrationBatch.RemainingV1Count, migrationBatch.UnreadableV1Count)
	}
	for _, rewrite := range migrationBatch.Rewrites {
		envelope, isV1 := decodeStoredEnvelope(rewrite.Value, strings.HasPrefix(rewrite.Key, SecretStorePrefix))
		if isV1 {
			t.Fatalf("%s rewritten as v1", rewrite.Key)
		}
		expectedValue := "value"
		if strings.HasPrefix(rewrite.Key, CredentialStorePrefix) {
			expectedValue = "cloud-token"
		}
		if opened, openError := fixture.keyringCodec.OpenEnvelopeForKey(ctx, rewrite.Key, envelope); openError != nil || string(opened) != expectedValue {
			t.Errorf("%s: %q, %v", rewrite.Key, opened, openError)
		}
	}
	allRewrites, _ := fixture.keyringCodec.PlanLegacyMigrationBatch(ctx, facts, 100)
	for _, rewrite := range allRewrites.Rewrites {
		if rewrite.Key == CredentialStorePrefix+"inst-1/s3" && isEnvelopeV2(rewrite.Value) {
			return
		}
	}
	t.Error("credential envelope not rewritten as raw v2")
}

// TestKeyringReplicaReportExpiresWithLease is a guard that the report is
// written with a lease (memory store: lease 0, so this only checks the write
// path does not fail without one).
func TestKeyringReplicaReportExpiresWithLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	fixture := newMigrationTestFixture(t, 0)
	fixture.keyringCodec.RunKeyringReplicaReporter(ctx, "cp-2", "v-test")
	if _, getError := fixture.factStore.Get(context.Background(), types.KeyObservedKeyringReplica("cp-2")); getError != nil {
		t.Fatalf("report not written: %v", getError)
	}
}
