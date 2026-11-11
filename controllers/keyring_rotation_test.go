// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rotationTestCluster is a fresh keyring cluster with v2 secrets under KEK 1.
type rotationTestCluster struct {
	factStore       *store.MemoryStore             // factStore holds keyring and secrets.
	keyringCodec    *security.KeyringEnvelopeCodec // keyringCodec is the leader replica's codec.
	rootKeyProvider security.KeyProvider           // rootKeyProvider builds further replicas.
	secretCount     int                            // secretCount is the number of secrets app-000…
}

// newRotationTestCluster writes secretCount secrets and one cloud credential
// on a fresh cluster and publishes the replica report of cp-1.
func newRotationTestCluster(t *testing.T, secretCount int) *rotationTestCluster {
	t.Helper()
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	rootKey, _ := security.GenerateMasterKey()
	rootKeyProvider, _ := security.NewLocalKeyProvider(rootKey)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	secretStore := security.NewSecretStore(factStore, keyringCodec)
	for secretIndex := 0; secretIndex < secretCount; secretIndex++ {
		if putError := secretStore.PutSecret(ctx, fmt.Sprintf("app-%03d", secretIndex), []byte(fmt.Sprintf("value-%03d", secretIndex))); putError != nil {
			t.Fatal(putError)
		}
	}
	credentialStore := security.NewCredentialStore(factStore, keyringCodec)
	if putError := credentialStore.PutCredential(ctx, "i-1", "s3-reader", &security.CloudCredential{Provider: "aws", SecretKey: "aws-secret"}); putError != nil {
		t.Fatal(putError)
	}
	cluster := &rotationTestCluster{factStore: factStore, keyringCodec: keyringCodec, rootKeyProvider: rootKeyProvider, secretCount: secretCount}
	cluster.publishReplicaReport(t)
	return cluster
}

// publishReplicaReport refreshes the codec and rewrites cp-1's keyring report,
// as the reporter does every few seconds in production.
func (cluster *rotationTestCluster) publishReplicaReport(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if refreshError := cluster.keyringCodec.Refresh(ctx); refreshError != nil {
		t.Fatal(refreshError)
	}
	reportContext, stopReport := context.WithTimeout(ctx, 20*time.Millisecond)
	cluster.keyringCodec.RunKeyringReplicaReporter(reportContext, "cp-1", "v-test")
	stopReport()
}

// runRotationCycle runs one reconciliation cycle of controller through the
// real runner (CAS guards, write domain).
func (cluster *rotationTestCluster) runRotationCycle(t *testing.T, controller *KeyringRotationController) {
	t.Helper()
	if cycleError := NewRunner(cluster.factStore, controller).executeReconciliationCycle(context.Background(), controller, nil); cycleError != nil {
		t.Fatal(cycleError)
	}
}

// storedEnvelope returns the raw envelope stored for a secret.
func (cluster *rotationTestCluster) storedEnvelope(t *testing.T, secretName string) []byte {
	t.Helper()
	storedFact, getError := cluster.factStore.Get(context.Background(), security.SecretStorePrefix+secretName)
	if getError != nil {
		t.Fatal(getError)
	}
	envelope, decodeError := base64.StdEncoding.DecodeString(string(storedFact.Value))
	if decodeError != nil {
		t.Fatal(decodeError)
	}
	return envelope
}

// envelopeKEKVersion reads the KEK version from a v2 envelope header
// (magic 4 bytes, format 1 byte, version 4 bytes).
func envelopeKEKVersion(envelope []byte) int {
	return int(binary.BigEndian.Uint32(envelope[5:9]))
}

// envelopeSealedValue returns nonce and ciphertext after the wrapped DEK.
func envelopeSealedValue(envelope []byte) []byte {
	wrappedDEKLength := int(binary.BigEndian.Uint16(envelope[9:11]))
	return envelope[11+wrappedDEKLength:]
}

// assertSecretsOnKEKAndReadable checks every secret names kekVersion and opens
// with a new replica's codec (only what is in the store), and so does the credential.
func (cluster *rotationTestCluster) assertSecretsOnKEKAndReadable(t *testing.T, kekVersion int) {
	t.Helper()
	ctx := context.Background()
	freshCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, cluster.factStore, cluster.rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	secretStore := security.NewSecretStore(cluster.factStore, freshCodec)
	for secretIndex := 0; secretIndex < cluster.secretCount; secretIndex++ {
		secretName := fmt.Sprintf("app-%03d", secretIndex)
		if storedVersion := envelopeKEKVersion(cluster.storedEnvelope(t, secretName)); storedVersion != kekVersion {
			t.Fatalf("%s names KEK %d, want %d", secretName, storedVersion, kekVersion)
		}
		if value, getError := secretStore.GetSecret(ctx, secretName); getError != nil || string(value) != fmt.Sprintf("value-%03d", secretIndex) {
			t.Fatalf("%s = %q, %v", secretName, value, getError)
		}
	}
	credential, credentialError := security.NewCredentialStore(cluster.factStore, freshCodec).GetCredential(ctx, "i-1", "s3-reader")
	if credentialError != nil || credential.SecretKey != "aws-secret" {
		t.Fatalf("credential = %+v, %v", credential, credentialError)
	}
}

// readRotationStatus parses observed/keyring/rotation.
func (cluster *rotationTestCluster) readRotationStatus(t *testing.T) security.KeyringRotationStatus {
	t.Helper()
	var rotationStatus security.KeyringRotationStatus
	statusFact, getError := cluster.factStore.Get(context.Background(), types.KeyObservedKeyringRotation)
	if getError != nil {
		t.Fatal(getError)
	}
	if unmarshalError := json.Unmarshal(statusFact.Value, &rotationStatus); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	return rotationStatus
}

// storeValue returns a key's value, "" when absent.
func (cluster *rotationTestCluster) storeValue(key string) string {
	fact, getError := cluster.factStore.Get(context.Background(), key)
	if getError != nil {
		return ""
	}
	return string(fact.Value)
}

// completeTestRotation starts a rotation and runs cycles until it completes,
// publishing the replica report once the switch happened.
func (cluster *rotationTestCluster) completeTestRotation(t *testing.T, controller *KeyringRotationController) {
	t.Helper()
	if _, startError := cluster.keyringCodec.StartKeyringRotation(context.Background(), "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	for cycle := 0; cycle < 20 && cluster.readRotationStatus(t).State != security.KeyringRotationCompleted; cycle++ {
		cluster.runRotationCycle(t, controller)
		cluster.publishReplicaReport(t)
	}
	if rotationStatus := cluster.readRotationStatus(t); rotationStatus.State != security.KeyringRotationCompleted {
		t.Fatalf("rotation did not complete: %+v", rotationStatus)
	}
}

// TestKeyringRotationIdleWithoutIntent verifies the controller writes nothing
// on a cluster with no rotation and every envelope on the active KEK.
func TestKeyringRotationIdleWithoutIntent(t *testing.T) {
	cluster := newRotationTestCluster(t, 3)
	facts, _ := cluster.factStore.Scan(context.Background(), "")
	store.SortFacts(facts)
	changes, reconcileError := NewKeyringRotationController(cluster.keyringCodec, DefaultKeyringRetireGrace).Reconcile(context.Background(), facts)
	if reconcileError != nil || len(changes) != 0 {
		t.Fatalf("changes without a rotation = %v, %v", changes, reconcileError)
	}
}

// TestKeyringRotationStepByStep verifies each step: create (pending), switch
// (active/draining), re-wrap in batches keeping nonce and ciphertext, no
// retirement while a replica reports the old KEK, then retiring and completed.
func TestKeyringRotationStepByStep(t *testing.T) {
	const secretCount = 90
	cluster := newRotationTestCluster(t, secretCount)
	controller := NewKeyringRotationController(cluster.keyringCodec, DefaultKeyringRetireGrace)
	sealedBefore := envelopeSealedValue(cluster.storedEnvelope(t, "app-007"))
	if _, startError := cluster.keyringCodec.StartKeyringRotation(context.Background(), "user:admin"); startError != nil {
		t.Fatal(startError)
	}

	cluster.runRotationCycle(t, controller)
	if cluster.storeValue(types.KeyKeyringKEKState(2)) != string(security.KEKVersionPending) || cluster.storeValue(types.KeyKeyringActive) != "1" {
		t.Fatalf("after create: state %q, active %q", cluster.storeValue(types.KeyKeyringKEKState(2)), cluster.storeValue(types.KeyKeyringActive))
	}
	cluster.runRotationCycle(t, controller)
	if cluster.storeValue(types.KeyKeyringActive) != "2" || cluster.storeValue(types.KeyKeyringKEKState(1)) != string(security.KEKVersionDraining) {
		t.Fatalf("after switch: active %q, KEK 1 %q", cluster.storeValue(types.KeyKeyringActive), cluster.storeValue(types.KeyKeyringKEKState(1)))
	}
	cluster.runRotationCycle(t, controller)
	if rewrapped := cluster.readRotationStatus(t).Rewrapped; rewrapped != keyringRewrapBatchSize {
		t.Fatalf("first batch re-wrapped %d, want %d", rewrapped, keyringRewrapBatchSize)
	}
	for cycle := 0; cycle < 5; cycle++ {
		cluster.runRotationCycle(t, controller)
	}
	if rotationStatus := cluster.readRotationStatus(t); rotationStatus.State != security.KeyringRotationInProgress || rotationStatus.Remaining != 0 {
		t.Fatalf("before the replica reports KEK 2: %+v (want in_progress, remaining 0)", rotationStatus)
	}
	if cluster.storeValue(types.KeyKeyringKEKState(1)) != string(security.KEKVersionDraining) {
		t.Fatal("KEK 1 retired while cp-1 still reported KEK 1")
	}

	cluster.publishReplicaReport(t)
	cluster.runRotationCycle(t, controller)
	rotationStatus := cluster.readRotationStatus(t)
	if rotationStatus.State != security.KeyringRotationCompleted || rotationStatus.Rewrapped != secretCount+1 || rotationStatus.UnreadableCount != 0 {
		t.Fatalf("final status = %+v", rotationStatus)
	}
	if cluster.storeValue(types.KeyIntentKeyringRotation) != "" || cluster.storeValue(types.KeyKeyringKEKState(1)) != string(security.KEKVersionRetiring) || cluster.storeValue(types.KeyKeyringKEK(1)) == "" {
		t.Fatal("want intent deleted, KEK 1 retiring and still stored")
	}
	if !bytes.Equal(sealedBefore, envelopeSealedValue(cluster.storedEnvelope(t, "app-007"))) {
		t.Fatal("re-wrap changed the nonce or ciphertext")
	}
	cluster.assertSecretsOnKEKAndReadable(t, 2)
}

// TestKeyringRotationDeletesOnlyAfterGrace verifies a retiring KEK is kept
// until the retire grace has passed, then deleted, and every secret still opens.
func TestKeyringRotationDeletesOnlyAfterGrace(t *testing.T) {
	cluster := newRotationTestCluster(t, 5)
	controller := NewKeyringRotationController(cluster.keyringCodec, time.Hour)
	cluster.completeTestRotation(t, controller)
	retiringSince, _ := time.Parse(time.RFC3339, cluster.storeValue(types.KeyKeyringKEKRetiringSince(1)))

	controller.timeNow = func() time.Time { return retiringSince.Add(59 * time.Minute) }
	cluster.runRotationCycle(t, controller)
	if cluster.storeValue(types.KeyKeyringKEK(1)) == "" {
		t.Fatal("KEK 1 deleted before the grace")
	}
	controller.timeNow = func() time.Time { return retiringSince.Add(61 * time.Minute) }
	cluster.runRotationCycle(t, controller)
	for _, kekKey := range []string{types.KeyKeyringKEK(1), types.KeyKeyringKEKState(1), types.KeyKeyringKEKRetiringSince(1)} {
		if cluster.storeValue(kekKey) != "" {
			t.Fatalf("%s still present after the grace", kekKey)
		}
	}
	cluster.assertSecretsOnKEKAndReadable(t, 2)
}

// TestKeyringRotationResumesOnEveryNewLeader verifies a rotation completes
// when every cycle runs on a new replica (new codec, new controller) that
// only knows the store, as after a crash or leader change at each step.
func TestKeyringRotationResumesOnEveryNewLeader(t *testing.T) {
	const secretCount = 60
	ctx := context.Background()
	cluster := newRotationTestCluster(t, secretCount)
	if _, startError := cluster.keyringCodec.StartKeyringRotation(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	for cycle := 0; cycle < 20 && cluster.readRotationStatus(t).State != security.KeyringRotationCompleted; cycle++ {
		leaderCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, cluster.factStore, cluster.rootKeyProvider, nil)
		if codecError != nil {
			t.Fatal(codecError)
		}
		cluster.runRotationCycle(t, NewKeyringRotationController(leaderCodec, DefaultKeyringRetireGrace))
		cluster.publishReplicaReport(t)
	}
	if rotationStatus := cluster.readRotationStatus(t); rotationStatus.State != security.KeyringRotationCompleted || rotationStatus.Rewrapped != secretCount+1 {
		t.Fatalf("rotation across leaders = %+v", rotationStatus)
	}
	cluster.assertSecretsOnKEKAndReadable(t, 2)
}

// TestKeyringRotationNeverDeletesReferencedKEK verifies an envelope naming a
// retiring KEK that cannot be re-wrapped sends the version back to draining,
// keeps it past the grace and keeps it counted as unreadable.
func TestKeyringRotationNeverDeletesReferencedKEK(t *testing.T) {
	ctx := context.Background()
	cluster := newRotationTestCluster(t, 3)
	controller := NewKeyringRotationController(cluster.keyringCodec, time.Hour)
	corruptEnvelope := cluster.storedEnvelope(t, "app-001")
	cluster.completeTestRotation(t, controller)

	corruptEnvelope[12] ^= 0xFF // damage the wrapped DEK; the header still names KEK 1
	cluster.factStore.Put(ctx, security.SecretStorePrefix+"app-001", []byte(base64.StdEncoding.EncodeToString(corruptEnvelope)))
	controller.timeNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	cluster.runRotationCycle(t, controller)
	cluster.runRotationCycle(t, controller)
	if cluster.storeValue(types.KeyKeyringKEK(1)) == "" || cluster.storeValue(types.KeyKeyringKEKState(1)) != string(security.KEKVersionDraining) {
		t.Fatalf("KEK 1 named by an envelope: state %q, stored %t", cluster.storeValue(types.KeyKeyringKEKState(1)), cluster.storeValue(types.KeyKeyringKEK(1)) != "")
	}
}

// concurrentWriteController wraps the rotation controller and overwrites one
// secret after the first re-wrap batch was planned, before the runner commits.
type concurrentWriteController struct {
	*KeyringRotationController
	secretStore *security.SecretStore // secretStore performs the concurrent user write.
	written     bool                  // written is set once the concurrent write happened.
}

// Reconcile plans like the wrapped controller, then writes app-002 once.
func (concurrentController *concurrentWriteController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	changes, reconcileError := concurrentController.KeyringRotationController.Reconcile(ctx, facts)
	if !concurrentController.written && len(changes) > 1 {
		concurrentController.written = true
		if putError := concurrentController.secretStore.PutSecret(ctx, "app-002", []byte("written-during-rotation")); putError != nil {
			return nil, putError
		}
	}
	return changes, reconcileError
}

// TestKeyringRotationNeverOverwritesConcurrentWrite verifies a user write
// between planning and commit makes the guarded batch fail, the write is
// kept, and the rotation still completes.
func TestKeyringRotationNeverOverwritesConcurrentWrite(t *testing.T) {
	ctx := context.Background()
	cluster := newRotationTestCluster(t, 5)
	if _, startError := cluster.keyringCodec.StartKeyringRotation(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	rotationController := NewKeyringRotationController(cluster.keyringCodec, DefaultKeyringRetireGrace)
	cluster.runRotationCycle(t, rotationController)
	cluster.runRotationCycle(t, rotationController)
	cluster.publishReplicaReport(t)

	secretStore := security.NewSecretStore(cluster.factStore, cluster.keyringCodec)
	concurrentController := &concurrentWriteController{KeyringRotationController: rotationController, secretStore: secretStore}
	_ = NewRunner(cluster.factStore, concurrentController).executeReconciliationCycle(ctx, concurrentController, nil)
	if !concurrentController.written {
		t.Fatal("the concurrent write did not happen")
	}
	for cycle := 0; cycle < 5; cycle++ {
		cluster.runRotationCycle(t, rotationController)
	}
	if value, getError := secretStore.GetSecret(ctx, "app-002"); getError != nil || string(value) != "written-during-rotation" {
		t.Fatalf("concurrent write lost: %q, %v", value, getError)
	}
	if rotationStatus := cluster.readRotationStatus(t); rotationStatus.State != security.KeyringRotationCompleted {
		t.Fatalf("rotation after the conflict = %+v", rotationStatus)
	}
}
