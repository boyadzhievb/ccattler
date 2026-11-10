// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestKeyProvider returns a local key provider over a fresh random key.
func newTestKeyProvider(t *testing.T) KeyProvider {
	t.Helper()
	randomKey, generateError := GenerateMasterKey()
	if generateError != nil {
		t.Fatal(generateError)
	}
	keyProvider, providerError := NewLocalKeyProvider(randomKey)
	if providerError != nil {
		t.Fatal(providerError)
	}
	return keyProvider
}

// readTestMigrationState returns the persisted migration state.
func readTestMigrationState(t *testing.T, factStore store.StateStore) KeyringMigrationState {
	t.Helper()
	migrationStatus, readError := readKeyringMigrationStatus(context.Background(), factStore)
	if readError != nil {
		t.Fatal(readError)
	}
	return migrationStatus.State
}

// writeTestMigrationState overwrites the persisted migration state.
func writeTestMigrationState(t *testing.T, factStore store.StateStore, migrationState KeyringMigrationState) {
	t.Helper()
	migrationJSON, _ := json.Marshal(KeyringMigrationStatus{State: migrationState})
	if _, putError := factStore.Put(context.Background(), types.KeyObservedKeyringMigration, migrationJSON); putError != nil {
		t.Fatal(putError)
	}
}

// TestKeyringFreshClusterWritesV2BoundToStoreKey verifies a fresh cluster
// (no legacy key, no envelopes) starts with migration completed, seals v2 and
// binds each envelope to its store key.
func TestKeyringFreshClusterWritesV2BoundToStoreKey(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), nil)
	if codecError != nil {
		t.Fatalf("codec: %v", codecError)
	}
	if migrationState := readTestMigrationState(t, factStore); migrationState != KeyringMigrationCompleted {
		t.Errorf("fresh migration state = %s, want completed", migrationState)
	}
	legacyFact, _ := factStore.Get(ctx, types.KeyKeyringLegacy)
	if LegacyKeyState(legacyFact.Value) != LegacyKeyNone {
		t.Errorf("legacy state = %s, want none", legacyFact.Value)
	}
	envelope, sealError := keyringCodec.SealEnvelopeForKey(ctx, "secrets/db", []byte("hunter2"))
	if sealError != nil {
		t.Fatal(sealError)
	}
	if !isEnvelopeV2(envelope) {
		t.Fatal("fresh cluster sealed a v1 envelope")
	}
	if opened, openError := keyringCodec.OpenEnvelopeForKey(ctx, "secrets/db", envelope); openError != nil || string(opened) != "hunter2" {
		t.Fatalf("open = %q, %v", opened, openError)
	}
	if _, openError := keyringCodec.OpenEnvelopeForKey(ctx, "secrets/other", envelope); openError == nil {
		t.Error("envelope opened under a different store key (AAD not bound)")
	}
}

// TestKeyringUpgradeKeepsV1UntilMigration verifies an upgraded cluster with
// v1 secrets records the legacy key, keeps writing v1 and still opens v1,
// then writes v2 once the migration state is in_progress.
func TestKeyringUpgradeKeepsV1UntilMigration(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	legacyKeyProvider := newTestKeyProvider(t)
	legacyStore := NewSecretStore(factStore, legacyOnlyEnvelopeCodec{keyProvider: legacyKeyProvider})
	if putError := legacyStore.PutSecret(ctx, "db", []byte("old-value")); putError != nil {
		t.Fatal(putError)
	}
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), legacyKeyProvider)
	if codecError != nil {
		t.Fatalf("codec: %v", codecError)
	}
	if migrationState := readTestMigrationState(t, factStore); migrationState != KeyringMigrationNotStarted {
		t.Errorf("upgrade migration state = %s, want not_started", migrationState)
	}
	upgradedStore := NewSecretStore(factStore, keyringCodec)
	if value, getError := upgradedStore.GetSecret(ctx, "db"); getError != nil || string(value) != "old-value" {
		t.Fatalf("v1 secret after upgrade = %q, %v", value, getError)
	}
	beforeGate, _ := keyringCodec.SealEnvelopeForKey(ctx, "secrets/new", []byte("v"))
	if isEnvelopeV2(beforeGate) {
		t.Error("v2 written before the migration gate")
	}
	writeTestMigrationState(t, factStore, KeyringMigrationInProgress)
	if refreshError := keyringCodec.Refresh(ctx); refreshError != nil {
		t.Fatal(refreshError)
	}
	afterGate, _ := keyringCodec.SealEnvelopeForKey(ctx, "secrets/new", []byte("v"))
	if !isEnvelopeV2(afterGate) {
		t.Error("v1 written after the migration started")
	}
}

// TestKeyringRefusesMissingOrWrongKeys verifies the startup refusals: secrets
// without a legacy key, a keyring that still needs the legacy key, and a
// different root key (root_check).
func TestKeyringRefusesMissingOrWrongKeys(t *testing.T) {
	ctx := context.Background()
	withSecrets := store.NewMemoryStore()
	withSecrets.Put(ctx, SecretStorePrefix+"db", []byte("v1-envelope"))
	if _, codecError := NewKeyringEnvelopeCodec(ctx, withSecrets, newTestKeyProvider(t), nil); codecError == nil {
		t.Error("secrets without the legacy key accepted")
	}

	upgradedStore := store.NewMemoryStore()
	rootKeyProvider := newTestKeyProvider(t)
	if _, codecError := NewKeyringEnvelopeCodec(ctx, upgradedStore, rootKeyProvider, newTestKeyProvider(t)); codecError != nil {
		t.Fatal(codecError)
	}
	if _, codecError := NewKeyringEnvelopeCodec(ctx, upgradedStore, rootKeyProvider, nil); codecError == nil {
		t.Error("replica without the legacy key accepted while the keyring still needs it")
	}
	if _, codecError := NewKeyringEnvelopeCodec(ctx, upgradedStore, newTestKeyProvider(t), newTestKeyProvider(t)); !errors.Is(codecError, ErrKeyringRootCheckFailed) {
		t.Errorf("different root key error = %v, want ErrKeyringRootCheckFailed", codecError)
	}
}

// TestLegacyKeyCannotOpenKeyringMaterial verifies decision R1: the legacy key
// cannot unwrap a KEK, open root_check or open a v2 envelope.
func TestLegacyKeyCannotOpenKeyringMaterial(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	legacyKeyProvider := newTestKeyProvider(t)
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), legacyKeyProvider)
	if codecError != nil {
		t.Fatal(codecError)
	}
	kekFact, _ := factStore.Get(ctx, types.KeyKeyringKEK(keyringFirstKEKVersion))
	if _, unwrapError := legacyKeyProvider.UnwrapKey(ctx, kekFact.Value); unwrapError == nil {
		t.Error("legacy key unwrapped a KEK")
	}
	rootCheckFact, _ := factStore.Get(ctx, types.KeyKeyringRootCheck)
	if _, unwrapError := legacyKeyProvider.UnwrapKey(ctx, rootCheckFact.Value); unwrapError == nil {
		t.Error("legacy key opened root_check")
	}
	writeTestMigrationState(t, factStore, KeyringMigrationInProgress)
	_ = keyringCodec.Refresh(ctx)
	v2Envelope, _ := keyringCodec.SealEnvelopeForKey(ctx, "secrets/db", []byte("value"))
	if _, openError := OpenEnvelope(ctx, legacyKeyProvider, v2Envelope); openError == nil {
		t.Error("legacy key opened a v2 envelope")
	}
}

// TestKeyringRetiredLegacyRefusesV1 verifies v1 envelopes are refused once
// the legacy key is retired.
func TestKeyringRetiredLegacyRefusesV1(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	legacyKeyProvider := newTestKeyProvider(t)
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), legacyKeyProvider)
	if codecError != nil {
		t.Fatal(codecError)
	}
	v1Envelope, _ := keyringCodec.SealEnvelopeForKey(ctx, "secrets/db", []byte("value"))
	factStore.Put(ctx, types.KeyKeyringLegacy, []byte(LegacyKeyRetired))
	_ = keyringCodec.Refresh(ctx)
	if _, openError := keyringCodec.OpenEnvelopeForKey(ctx, "secrets/db", v1Envelope); openError == nil || !strings.Contains(openError.Error(), "retired") {
		t.Errorf("v1 after retirement: %v, want refusal", openError)
	}
}

// TestKeyringConcurrentBootstrapAgrees verifies replicas bootstrapping at the
// same time end with one keyring: each opens the other's envelopes.
func TestKeyringConcurrentBootstrapAgrees(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	rootKeyProvider := newTestKeyProvider(t)
	codecs := make([]*KeyringEnvelopeCodec, 4)
	var waitGroup sync.WaitGroup
	for replicaIndex := range codecs {
		waitGroup.Add(1)
		go func(replicaIndex int) {
			defer waitGroup.Done()
			keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, nil)
			if codecError != nil {
				t.Errorf("replica %d: %v", replicaIndex, codecError)
				return
			}
			codecs[replicaIndex] = keyringCodec
		}(replicaIndex)
	}
	waitGroup.Wait()
	if t.Failed() {
		return
	}
	envelope, _ := codecs[0].SealEnvelopeForKey(ctx, "secrets/db", []byte("shared"))
	for replicaIndex, keyringCodec := range codecs {
		if opened, openError := keyringCodec.OpenEnvelopeForKey(ctx, "secrets/db", envelope); openError != nil || string(opened) != "shared" {
			t.Errorf("replica %d cannot open replica 0's envelope: %v", replicaIndex, openError)
		}
	}
}

// TestEnvelopeFormatDetection verifies v1 envelopes (and base64 text) are
// never mistaken for v2.
func TestEnvelopeFormatDetection(t *testing.T) {
	ctx := context.Background()
	v1Envelope, _ := SealEnvelope(ctx, newTestKeyProvider(t), []byte("value"))
	if isEnvelopeV2(v1Envelope) {
		t.Error("v1 envelope detected as v2")
	}
	if isEnvelopeV2([]byte(base64.StdEncoding.EncodeToString(v1Envelope))) {
		t.Error("base64 text detected as v2")
	}
	if _, _, _, parseError := parseEnvelopeV2(append(append([]byte{}, envelopeV2Magic...), envelopeV2Format)); parseError == nil {
		t.Error("truncated v2 header accepted")
	}
}

// TestKeyringReplicaReportPublished verifies a replica publishes its version
// and active KEK at observed/keyring/replica/{node}.
func TestKeyringReplicaReportPublished(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, newTestKeyProvider(t), nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	keyringCodec.publishKeyringReplicaReport(ctx, "cp-1", "v-test", 0)
	reportFact, getError := factStore.Get(ctx, types.KeyObservedKeyringReplica("cp-1"))
	if getError != nil {
		t.Fatal(getError)
	}
	var report keyringReplicaReport
	if unmarshalError := json.Unmarshal(reportFact.Value, &report); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	if report.Version != "v-test" || report.ActiveKEKVersion != keyringFirstKEKVersion || report.RootCheck != "ok" {
		t.Errorf("report = %+v", report)
	}
}
