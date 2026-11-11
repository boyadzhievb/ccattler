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

// rootKeyTestCluster is a fresh keyring created from a root key file, with
// two KEK versions and a few secrets.
type rootKeyTestCluster struct {
	factStore   store.StateStore // factStore holds the keyring and secrets.
	rootKeyPath string           // rootKeyPath is the root key file in a temp directory.
	originalKey []byte           // originalKey is the root key before any rotation.
}

// newRootKeyTestCluster creates the root key file, the keyring, a second KEK
// version (as after a KEK rotation) and secrets under both versions.
func newRootKeyTestCluster(t *testing.T) *rootKeyTestCluster {
	t.Helper()
	ctx := context.Background()
	rootKeyPath := filepath.Join(t.TempDir(), "secret-root.key")
	if generateError := GenerateSecretRootKeyFile(rootKeyPath); generateError != nil {
		t.Fatal(generateError)
	}
	originalKey, _ := LoadSecretRootKeyFile(rootKeyPath)
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
	return &rootKeyTestCluster{factStore: factStore, rootKeyPath: rootKeyPath, originalKey: originalKey}
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

// TestRotateSecretRootKeyFile verifies the keyring moves to a new root key:
// new file at the path, the old one kept as .old, no .next left, every
// secret opens with the new key, and the old key opens neither root_check
// nor any KEK.
func TestRotateSecretRootKeyFile(t *testing.T) {
	ctx := context.Background()
	cluster := newRootKeyTestCluster(t)
	resumed, rotateError := RotateSecretRootKeyFile(ctx, cluster.factStore, cluster.rootKeyPath)
	if rotateError != nil || resumed {
		t.Fatalf("rotate: resumed %t, %v", resumed, rotateError)
	}
	newKey, _ := LoadSecretRootKeyFile(cluster.rootKeyPath)
	if bytes.Equal(newKey, cluster.originalKey) {
		t.Fatal("root key file unchanged")
	}
	if oldKey, _ := LoadSecretRootKeyFile(cluster.rootKeyPath + rootKeyOldSuffix); !bytes.Equal(oldKey, cluster.originalKey) {
		t.Fatal(".old does not hold the previous key")
	}
	if _, statError := os.Stat(cluster.rootKeyPath + rootKeyNextSuffix); !errors.Is(statError, os.ErrNotExist) {
		t.Fatalf(".next left behind: %v", statError)
	}
	cluster.assertSecretsOpenWithKeyFile(t, cluster.rootKeyPath)

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
}

// TestRotateSecretRootKeyFileResumesAfterCrash verifies a rotation that
// committed its transaction but crashed before (or between) the file renames
// is finished by running the command again, without re-wrapping twice.
func TestRotateSecretRootKeyFileResumesAfterCrash(t *testing.T) {
	ctx := context.Background()
	for _, crashPoint := range []string{"before renames", "between renames"} {
		t.Run(crashPoint, func(t *testing.T) {
			cluster := newRootKeyTestCluster(t)
			nextKeyPath := cluster.rootKeyPath + rootKeyNextSuffix
			_ = GenerateSecretRootKeyFile(nextKeyPath)
			nextKey, _ := LoadSecretRootKeyFile(nextKeyPath)
			currentKeyProvider, _ := NewLocalKeyProvider(cluster.originalKey)
			nextKeyProvider, _ := NewLocalKeyProvider(nextKey)
			if rewrapError := rewrapKeyringUnderRootKey(ctx, cluster.factStore, currentKeyProvider, nextKeyProvider); rewrapError != nil {
				t.Fatal(rewrapError)
			}
			if crashPoint == "between renames" {
				_ = os.Rename(cluster.rootKeyPath, cluster.rootKeyPath+rootKeyOldSuffix)
			}
			resumed, rotateError := RotateSecretRootKeyFile(ctx, cluster.factStore, cluster.rootKeyPath)
			if rotateError != nil || !resumed {
				t.Fatalf("resume: resumed %t, %v", resumed, rotateError)
			}
			if installedKey, _ := LoadSecretRootKeyFile(cluster.rootKeyPath); !bytes.Equal(installedKey, nextKey) {
				t.Fatal("the committed key is not installed")
			}
			cluster.assertSecretsOpenWithKeyFile(t, cluster.rootKeyPath)
		})
	}
}

// TestRotateSecretRootKeyFileReplacesStaleNext verifies a .next left by a
// crash before the transaction is discarded and the rotation completes.
func TestRotateSecretRootKeyFileReplacesStaleNext(t *testing.T) {
	cluster := newRootKeyTestCluster(t)
	_ = GenerateSecretRootKeyFile(cluster.rootKeyPath + rootKeyNextSuffix)
	staleKey, _ := LoadSecretRootKeyFile(cluster.rootKeyPath + rootKeyNextSuffix)
	if _, rotateError := RotateSecretRootKeyFile(context.Background(), cluster.factStore, cluster.rootKeyPath); rotateError != nil {
		t.Fatal(rotateError)
	}
	if installedKey, _ := LoadSecretRootKeyFile(cluster.rootKeyPath); bytes.Equal(installedKey, staleKey) {
		t.Fatal("the stale .next key was installed")
	}
	cluster.assertSecretsOpenWithKeyFile(t, cluster.rootKeyPath)
}

// TestRotateSecretRootKeyFileRefusesWrongKey verifies a key file that does
// not open root_check is refused and the keyring is left untouched.
func TestRotateSecretRootKeyFileRefusesWrongKey(t *testing.T) {
	ctx := context.Background()
	cluster := newRootKeyTestCluster(t)
	wrongKeyPath := filepath.Join(filepath.Dir(cluster.rootKeyPath), "wrong.key")
	_ = GenerateSecretRootKeyFile(wrongKeyPath)
	rootCheckBefore, _ := cluster.factStore.Get(ctx, types.KeyKeyringRootCheck)
	if _, rotateError := RotateSecretRootKeyFile(ctx, cluster.factStore, wrongKeyPath); rotateError == nil || !strings.Contains(rotateError.Error(), "wrong key files") {
		t.Fatalf("wrong key: %v", rotateError)
	}
	if rootCheckAfter, _ := cluster.factStore.Get(ctx, types.KeyKeyringRootCheck); rootCheckAfter.Revision != rootCheckBefore.Revision {
		t.Fatal("keyring changed after a refused rotation")
	}
	cluster.assertSecretsOpenWithKeyFile(t, cluster.rootKeyPath)
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
	time.Sleep(50 * time.Millisecond)
	if rewrapError := rewrapKeyringUnderRootKey(ctx, cluster.factStore, runningKeyProvider, newTestKeyProvider(t)); rewrapError != nil {
		t.Fatal(rewrapError)
	}
	select {
	case watchError := <-watchResult:
		if !errors.Is(watchError, ErrKeyringRootCheckFailed) {
			t.Fatalf("watch ended with %v", watchError)
		}
	case <-ctx.Done():
		t.Fatalf("watch still running after the root key changed: %v", ctx.Err())
	}
}
