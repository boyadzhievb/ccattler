// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rootKeyTestCluster is a fresh keyring with two KEK versions and two
// control-plane hosts (temp directories) holding the same root key file.
type rootKeyTestCluster struct {
	factStore    store.StateStore // factStore holds the keyring and secrets.
	hostKeyPaths []string         // hostKeyPaths are the root key paths of host 1 and host 2.
	originalKey  []byte           // originalKey is the root key before any rotation.
}

// newRootKeyTestCluster creates the root key on two hosts, the keyring, a
// second KEK version (as after a KEK rotation) and secrets under both versions.
func newRootKeyTestCluster(t *testing.T) *rootKeyTestCluster {
	t.Helper()
	ctx := context.Background()
	firstKeyPath := filepath.Join(t.TempDir(), "secret-root.key")
	if generateError := GenerateSecretRootKeyFile(firstKeyPath); generateError != nil {
		t.Fatal(generateError)
	}
	originalKey, _ := LoadSecretRootKeyFile(firstKeyPath)
	secondKeyPath := filepath.Join(t.TempDir(), "secret-root.key")
	writeTestKeyFile(t, secondKeyPath, originalKey)
	rootKeyProvider, _ := NewLocalKeyProvider(originalKey)
	factStore := store.NewMemoryStore()
	keyringCodec, codecError := NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	secretStore := NewSecretStore(factStore, keyringCodec)
	_ = secretStore.PutSecret(ctx, "under-kek-1", []byte("value-1"))
	wrappedKEK, _ := keyringCodec.CreateWrappedKEK(ctx)
	factStore.Put(ctx, types.KeyKeyringKEK(2), wrappedKEK)
	factStore.Put(ctx, types.KeyKeyringKEKState(2), []byte(KEKVersionActive))
	factStore.Put(ctx, types.KeyKeyringActive, []byte("2"))
	_ = keyringCodec.Refresh(ctx)
	_ = secretStore.PutSecret(ctx, "under-kek-2", []byte("value-2"))
	return &rootKeyTestCluster{factStore: factStore, hostKeyPaths: []string{firstKeyPath, secondKeyPath}, originalKey: originalKey}
}

// writeTestKeyFile writes keyBytes as a 0400 key file, as Ansible's copy does.
func writeTestKeyFile(t *testing.T, keyPath string, keyBytes []byte) {
	t.Helper()
	if writeError := os.WriteFile(keyPath, keyBytes, 0o400); writeError != nil {
		t.Fatal(writeError)
	}
}

// stageNextKey generates {path}.next on host 1 and copies it to host 2, as
// the playbook does before the commit; returns the staged key.
func (cluster *rootKeyTestCluster) stageNextKey(t *testing.T) []byte {
	t.Helper()
	firstNextPath := cluster.hostKeyPaths[0] + RootKeyNextSuffix
	if generateError := GenerateSecretRootKeyFile(firstNextPath); generateError != nil {
		t.Fatal(generateError)
	}
	nextKey, _ := LoadSecretRootKeyFile(firstNextPath)
	writeTestKeyFile(t, cluster.hostKeyPaths[1]+RootKeyNextSuffix, nextKey)
	return nextKey
}

// stage returns a host's rotation stage.
func (cluster *rootKeyTestCluster) stage(t *testing.T, keyPath string) RootKeyRotationStage {
	t.Helper()
	filesStatus, inspectError := InspectRootKeyFiles(context.Background(), cluster.factStore, keyPath)
	if inspectError != nil {
		t.Fatal(inspectError)
	}
	return filesStatus.Stage
}

// assertSecretsOpenWithKeyFile opens both secrets through a new replica that
// loads the root key from keyPath.
func (cluster *rootKeyTestCluster) assertSecretsOpenWithKeyFile(t *testing.T, keyPath string) {
	t.Helper()
	ctx := context.Background()
	rootKey, loadError := LoadSecretRootKeyFile(keyPath)
	if loadError != nil {
		t.Fatal(loadError)
	}
	rootKeyProvider, _ := NewLocalKeyProvider(rootKey)
	replicaCodec, codecError := NewKeyringEnvelopeCodec(ctx, cluster.factStore, rootKeyProvider, nil)
	if codecError != nil {
		t.Fatalf("replica with %s: %v", keyPath, codecError)
	}
	secretStore := NewSecretStore(cluster.factStore, replicaCodec)
	for secretName, wantValue := range map[string]string{"under-kek-1": "value-1", "under-kek-2": "value-2"} {
		if value, getError := secretStore.GetSecret(ctx, secretName); getError != nil || string(value) != wantValue {
			t.Fatalf("%s = %q, %v", secretName, value, getError)
		}
	}
}

// rootCheckRevision returns the current revision of keyring/root_check.
func (cluster *rootKeyTestCluster) rootCheckRevision(t *testing.T) int64 {
	t.Helper()
	rootCheckFact, getError := cluster.factStore.Get(context.Background(), types.KeyKeyringRootCheck)
	if getError != nil {
		t.Fatal(getError)
	}
	return rootCheckFact.Revision
}

// TestCommitAndFinishRootKeyRotation verifies the two-host flow: staged on
// both hosts, commit on host 1, finish on host 2, same key everywhere, the
// old key opens nothing, and a repeated commit or finish never rotates again.
func TestCommitAndFinishRootKeyRotation(t *testing.T) {
	ctx := context.Background()
	cluster := newRootKeyTestCluster(t)
	firstKeyPath, secondKeyPath := cluster.hostKeyPaths[0], cluster.hostKeyPaths[1]
	nextKey := cluster.stageNextKey(t)
	if stage := cluster.stage(t, secondKeyPath); stage != RootKeyStageStaged {
		t.Fatalf("host 2 before commit: %s", stage)
	}
	if _, finishError := FinishRootKeyRotation(ctx, cluster.factStore, secondKeyPath); finishError == nil || !strings.Contains(finishError.Error(), "not re-wrapped yet") {
		t.Fatalf("finish before commit: %v", finishError)
	}

	commitFingerprint, commitError := CommitRootKeyRotation(ctx, cluster.factStore, firstKeyPath, nil)
	if commitError != nil {
		t.Fatal(commitError)
	}
	if stage := cluster.stage(t, secondKeyPath); stage != RootKeyStageCommitted {
		t.Fatalf("host 2 after commit: %s", stage)
	}
	finishFingerprint, finishError := FinishRootKeyRotation(ctx, cluster.factStore, secondKeyPath)
	if finishError != nil || finishFingerprint != commitFingerprint || commitFingerprint != rootKeyFingerprint(nextKey) {
		t.Fatalf("finish: %s vs %s, %v", finishFingerprint, commitFingerprint, finishError)
	}
	for _, keyPath := range cluster.hostKeyPaths {
		if installedKey, _ := LoadSecretRootKeyFile(keyPath); !bytes.Equal(installedKey, nextKey) {
			t.Fatalf("%s does not hold the new key", keyPath)
		}
		if oldKey, _ := LoadSecretRootKeyFile(keyPath + RootKeyOldSuffix); !bytes.Equal(oldKey, cluster.originalKey) {
			t.Fatalf("%s.old does not hold the previous key", keyPath)
		}
		cluster.assertSecretsOpenWithKeyFile(t, keyPath)
	}

	oldKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
	if verifyError := verifyKeyringRootCheck(ctx, cluster.factStore, oldKeyProvider); !errors.Is(verifyError, ErrKeyringRootCheckFailed) {
		t.Fatalf("old key opens root_check: %v", verifyError)
	}
	for _, kekVersion := range []int{1, 2} {
		wrappedKEK, _ := cluster.factStore.Get(ctx, types.KeyKeyringKEK(kekVersion))
		if _, unwrapError := oldKeyProvider.UnwrapKey(ctx, wrappedKEK.Value); unwrapError == nil {
			t.Fatalf("old key unwraps KEK %d", kekVersion)
		}
	}

	revisionAfterRotation := cluster.rootCheckRevision(t)
	if _, recommitError := CommitRootKeyRotation(ctx, cluster.factStore, firstKeyPath, nil); recommitError == nil || !strings.Contains(recommitError.Error(), "nothing to commit") {
		t.Fatalf("second commit: %v", recommitError)
	}
	if refinishFingerprint, refinishError := FinishRootKeyRotation(ctx, cluster.factStore, secondKeyPath); refinishError != nil || refinishFingerprint != commitFingerprint {
		t.Fatalf("second finish: %s, %v", refinishFingerprint, refinishError)
	}
	if cluster.rootCheckRevision(t) != revisionAfterRotation {
		t.Fatal("a repeated command changed the keyring")
	}
}

// TestCommitRootKeyRotationResumesAfterCrash verifies a commit whose
// transaction landed but whose file swap did not (or only half) is finished
// by running --commit again, without a second re-wrap.
func TestCommitRootKeyRotationResumesAfterCrash(t *testing.T) {
	ctx := context.Background()
	for _, crashPoint := range []string{"before renames", "between renames"} {
		t.Run(crashPoint, func(t *testing.T) {
			cluster := newRootKeyTestCluster(t)
			firstKeyPath := cluster.hostKeyPaths[0]
			nextKey := cluster.stageNextKey(t)
			currentKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
			nextKeyProvider, _ := NewLocalKeyProvider(nextKey)
			if rewrapError := rewrapKeyringUnderRootKey(ctx, cluster.factStore, currentKeyProvider, nextKeyProvider); rewrapError != nil {
				t.Fatal(rewrapError)
			}
			if crashPoint == "between renames" {
				_ = os.Rename(firstKeyPath, firstKeyPath+RootKeyOldSuffix)
			}
			revisionAfterCommit := cluster.rootCheckRevision(t)
			if _, commitError := CommitRootKeyRotation(ctx, cluster.factStore, firstKeyPath, nil); commitError != nil {
				t.Fatal(commitError)
			}
			if cluster.rootCheckRevision(t) != revisionAfterCommit {
				t.Fatal("resume re-wrapped the keyring again")
			}
			cluster.assertSecretsOpenWithKeyFile(t, firstKeyPath)
		})
	}
}

// TestCommitRootKeyRotationRefusals verifies each refusal leaves the keyring
// untouched: no staged key, an existing .old, a staged key equal to the
// current or the legacy key, a live replica, a running KEK rotation.
func TestCommitRootKeyRotationRefusals(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name        string                                                 // name describes the refused situation.
		arrange     func(t *testing.T, cluster *rootKeyTestCluster) []byte // arrange prepares it and returns the legacy key to pass.
		wantMessage string                                                 // wantMessage is part of the expected error.
	}{
		{"no staged key", func(t *testing.T, cluster *rootKeyTestCluster) []byte { return nil }, "nothing to commit"},
		{"old key file present", func(t *testing.T, cluster *rootKeyTestCluster) []byte {
			cluster.stageNextKey(t)
			writeTestKeyFile(t, cluster.hostKeyPaths[0]+RootKeyOldSuffix, cluster.originalKey)
			return nil
		}, "earlier rotation"},
		{"staged key is the current key", func(t *testing.T, cluster *rootKeyTestCluster) []byte {
			writeTestKeyFile(t, cluster.hostKeyPaths[0]+RootKeyNextSuffix, cluster.originalKey)
			return nil
		}, "is the current key"},
		{"staged key is the legacy key", func(t *testing.T, cluster *rootKeyTestCluster) []byte {
			return cluster.stageNextKey(t)
		}, "legacy"},
		{"replica running", func(t *testing.T, cluster *rootKeyTestCluster) []byte {
			cluster.stageNextKey(t)
			cluster.factStore.Put(ctx, types.KeyObservedKeyringReplica("cp-3"), []byte("{}"))
			return nil
		}, "cp-3"},
		{"KEK rotation running", func(t *testing.T, cluster *rootKeyTestCluster) []byte {
			cluster.stageNextKey(t)
			cluster.factStore.Put(ctx, types.KeyIntentKeyringRotation, []byte("{}"))
			return nil
		}, "KEK rotation is running"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cluster := newRootKeyTestCluster(t)
			legacyKey := testCase.arrange(t, cluster)
			revisionBefore := cluster.rootCheckRevision(t)
			if _, commitError := CommitRootKeyRotation(ctx, cluster.factStore, cluster.hostKeyPaths[0], legacyKey); commitError == nil || !strings.Contains(commitError.Error(), testCase.wantMessage) {
				t.Fatalf("commit: %v (want %q)", commitError, testCase.wantMessage)
			}
			if cluster.rootCheckRevision(t) != revisionBefore {
				t.Fatal("keyring changed after a refused commit")
			}
			cluster.assertSecretsOpenWithKeyFile(t, cluster.hostKeyPaths[0])
		})
	}
}

// TestRootKeyRewrapFailsWhenStaleReplicaAddsKEK verifies the re-wrap
// transaction fails if a KEK version appears between its read and its
// commit (a replica still running with the old root key, review finding 1).
func TestRootKeyRewrapFailsWhenStaleReplicaAddsKEK(t *testing.T) {
	ctx := context.Background()
	cluster := newRootKeyTestCluster(t)
	nextKey := cluster.stageNextKey(t)
	currentKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
	nextKeyProvider, _ := NewLocalKeyProvider(nextKey)
	compares, operations, buildError := buildRootKeyRewrapTransaction(ctx, cluster.factStore, currentKeyProvider, nextKeyProvider)
	if buildError != nil {
		t.Fatal(buildError)
	}
	staleWrappedKEK, _ := currentKeyProvider.WrapKey(ctx, make([]byte, SecretRootKeySize))
	cluster.factStore.Put(ctx, types.KeyKeyringKEK(3), staleWrappedKEK)
	if committed, _ := cluster.factStore.Transaction(ctx, compares, operations, nil); committed {
		t.Fatal("re-wrap committed although a KEK under the old root key appeared")
	}
}

// TestStaleReplicaRefusesKEKCreationAfterRootRotation verifies a replica
// still holding the old root key cannot create a KEK or start a KEK rotation
// once the keyring moved to the new root key.
func TestStaleReplicaRefusesKEKCreationAfterRootRotation(t *testing.T) {
	ctx := context.Background()
	cluster := newRootKeyTestCluster(t)
	staleKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
	staleCodec, codecError := NewKeyringEnvelopeCodec(ctx, cluster.factStore, staleKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	cluster.stageNextKey(t)
	if _, commitError := CommitRootKeyRotation(ctx, cluster.factStore, cluster.hostKeyPaths[0], nil); commitError != nil {
		t.Fatal(commitError)
	}
	if _, createError := staleCodec.CreateWrappedKEK(ctx); !errors.Is(createError, ErrKeyringRootCheckFailed) {
		t.Fatalf("stale CreateWrappedKEK: %v", createError)
	}
	if _, startError := staleCodec.StartKeyringRotation(ctx, "user:admin"); !errors.Is(startError, ErrKeyringRootCheckFailed) {
		t.Fatalf("stale StartKeyringRotation: %v", startError)
	}
}

// TestInspectRootKeyFilesInvalidWithWrongKey verifies a key file that opens
// nothing is reported as invalid (also the KMS case: no key file opens).
func TestInspectRootKeyFilesInvalidWithWrongKey(t *testing.T) {
	cluster := newRootKeyTestCluster(t)
	wrongKeyPath := filepath.Join(t.TempDir(), "wrong.key")
	_ = GenerateSecretRootKeyFile(wrongKeyPath)
	if stage := cluster.stage(t, wrongKeyPath); stage != RootKeyStageInvalid {
		t.Fatalf("wrong key stage = %s", stage)
	}
	if stage := cluster.stage(t, filepath.Join(t.TempDir(), "missing.key")); stage != RootKeyStageInvalid {
		t.Fatalf("missing key stage = %s", stage)
	}
}

// TestWaitForStoppedKeyringReplicas verifies a live replica report refuses
// the rotation, naming the replica, and no report lets it proceed.
func TestWaitForStoppedKeyringReplicas(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	factStore.Put(ctx, types.KeyObservedKeyringReplica("cp-2"), []byte("{}"))
	if waitError := WaitForStoppedKeyringReplicas(ctx, factStore, 0); waitError == nil || !strings.Contains(waitError.Error(), "cp-2") {
		t.Fatalf("live replica: %v", waitError)
	}
	_ = factStore.Delete(ctx, types.KeyObservedKeyringReplica("cp-2"))
	if waitError := WaitForStoppedKeyringReplicas(ctx, factStore, 0); waitError != nil {
		t.Fatalf("no replica: %v", waitError)
	}
}

// TestRunKeyringWatchExitsAfterRootKeyRotation verifies a replica still
// running when the keyring moves to another root key stops its watch with
// ErrKeyringRootCheckFailed (the server then exits) instead of serving.
func TestRunKeyringWatchExitsAfterRootKeyRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cluster := newRootKeyTestCluster(t)
	runningKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
	runningCodec, codecError := NewKeyringEnvelopeCodec(ctx, cluster.factStore, runningKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	watchResult := make(chan error, 1)
	go func() { watchResult <- runningCodec.RunKeyringWatch(ctx) }()
	if rewrapError := rewrapKeyringUnderRootKey(ctx, cluster.factStore, runningKeyProvider, newTestKeyProvider(t)); rewrapError != nil {
		t.Fatal(rewrapError)
	}
	// The watch may subscribe after the re-wrap; re-writing keyring/legacy with
	// its own value produces keyring/ events until the watch has seen one.
	eventTicker := time.NewTicker(20 * time.Millisecond)
	defer eventTicker.Stop()
	for {
		select {
		case watchError := <-watchResult:
			if !errors.Is(watchError, ErrKeyringRootCheckFailed) {
				t.Fatalf("watch ended with %v", watchError)
			}
			return
		case <-eventTicker.C:
			cluster.factStore.Put(ctx, types.KeyKeyringLegacy, []byte(LegacyKeyNone))
		case <-ctx.Done():
			t.Fatalf("watch still running after the root key changed: %v", ctx.Err())
		}
	}
}
