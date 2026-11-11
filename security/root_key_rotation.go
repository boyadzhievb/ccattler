// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// rootKeyNextSuffix names the new root key file while a rotation runs.
const rootKeyNextSuffix = ".next"

// rootKeyOldSuffix names the replaced root key file, kept for a rollback
// until the playbook deletes it.
const rootKeyOldSuffix = ".old"

// rootKeyRotationPollInterval is how often the live replica reports are re-read
// while waiting for every API replica to stop.
const rootKeyRotationPollInterval = time.Second

// WaitForStoppedKeyringReplicas waits up to maxWait until no API replica
// report is left under observed/keyring/replica/ (their leases expire after a
// server stops). Still present at the deadline is an error naming them.
func WaitForStoppedKeyringReplicas(ctx context.Context, factStore store.StateStore, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for {
		reportFacts, scanError := factStore.Scan(ctx, types.ScanObservedKeyringReplicas)
		if scanError != nil {
			return fmt.Errorf("scan %s: %w", types.ScanObservedKeyringReplicas, scanError)
		}
		if len(reportFacts) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			liveReplicas := make([]string, 0, len(reportFacts))
			for _, reportFact := range reportFacts {
				liveReplicas = append(liveReplicas, strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas))
			}
			return fmt.Errorf("API replicas still running (stop cca-server on every control-plane host first): %s", strings.Join(liveReplicas, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rootKeyRotationPollInterval):
		}
	}
}

// RotateSecretRootKeyFile replaces the root key at rootKeyPath (Flow 30 part
// B): a new key is written to {path}.next, every KEK and root_check are
// re-wrapped under it in one transaction, then {path} → {path}.old and
// {path}.next → {path}. Returns true when it only finished a rotation whose
// transaction had already committed (a crash before the file swap).
func RotateSecretRootKeyFile(ctx context.Context, factStore store.StateStore, rootKeyPath string) (bool, error) {
	nextKeyPath, oldKeyPath := rootKeyPath+rootKeyNextSuffix, rootKeyPath+rootKeyOldSuffix
	currentKeyProvider, currentOpens, currentError := loadRootKeyOpeningKeyring(ctx, factStore, rootKeyPath)
	if currentError != nil {
		return false, currentError
	}
	if !currentOpens {
		_, nextOpens, nextError := loadRootKeyOpeningKeyring(ctx, factStore, nextKeyPath)
		if nextError != nil {
			return false, nextError
		}
		if !nextOpens {
			return false, fmt.Errorf("neither %s nor %s opens keyring/root_check: wrong key files (or the cluster uses --kms-provider)", rootKeyPath, nextKeyPath)
		}
		return true, swapRootKeyFiles(rootKeyPath, nextKeyPath, oldKeyPath)
	}
	if removeError := os.Remove(nextKeyPath); removeError != nil && !errors.Is(removeError, fs.ErrNotExist) {
		return false, fmt.Errorf("remove the unused %s of an interrupted rotation: %w", nextKeyPath, removeError)
	}
	if generateError := GenerateSecretRootKeyFile(nextKeyPath); generateError != nil {
		return false, generateError
	}
	nextKey, loadError := LoadSecretRootKeyFile(nextKeyPath)
	if loadError != nil {
		return false, loadError
	}
	nextKeyProvider, providerError := NewLocalKeyProvider(nextKey)
	if providerError != nil {
		return false, providerError
	}
	if rewrapError := rewrapKeyringUnderRootKey(ctx, factStore, currentKeyProvider, nextKeyProvider); rewrapError != nil {
		return false, rewrapError
	}
	return false, swapRootKeyFiles(rootKeyPath, nextKeyPath, oldKeyPath)
}

// loadRootKeyOpeningKeyring loads the root key file at keyPath and reports
// whether it opens keyring/root_check. A missing file is (nil, false, nil);
// an invalid file (mode, size) or a store error is an error.
func loadRootKeyOpeningKeyring(ctx context.Context, factStore store.StateStore, keyPath string) (KeyProvider, bool, error) {
	rootKey, loadError := LoadSecretRootKeyFile(keyPath)
	if errors.Is(loadError, ErrSecretRootKeyMissing) {
		return nil, false, nil
	}
	if loadError != nil {
		return nil, false, loadError
	}
	rootKeyProvider, providerError := NewLocalKeyProvider(rootKey)
	if providerError != nil {
		return nil, false, providerError
	}
	verifyError := verifyKeyringRootCheck(ctx, factStore, rootKeyProvider)
	if errors.Is(verifyError, ErrKeyringRootCheckFailed) {
		return rootKeyProvider, false, nil
	}
	return rootKeyProvider, verifyError == nil, verifyError
}

// rewrapKeyringUnderRootKey re-wraps every keyring/kek/{v} and root_check from
// the current to the next root key in ONE transaction guarded by the revision
// of each key it writes and of keyring/active: there is never a keyring with
// some KEKs under one root key and some under the other.
func rewrapKeyringUnderRootKey(ctx context.Context, factStore store.StateStore, currentKeyProvider KeyProvider, nextKeyProvider KeyProvider) error {
	kekFacts, scanError := factStore.Scan(ctx, types.ScanKeyringKEKs)
	if scanError != nil {
		return fmt.Errorf("scan %s: %w", types.ScanKeyringKEKs, scanError)
	}
	activeFact, activeError := factStore.Get(ctx, types.KeyKeyringActive)
	if activeError != nil {
		return fmt.Errorf("read %s: %w", types.KeyKeyringActive, activeError)
	}
	rootCheckFact, rootCheckError := factStore.Get(ctx, types.KeyKeyringRootCheck)
	if rootCheckError != nil {
		return fmt.Errorf("read %s: %w", types.KeyKeyringRootCheck, rootCheckError)
	}
	sealedRootCheck, sealError := nextKeyProvider.WrapKey(ctx, keyringRootCheckPlaintext)
	if sealError != nil {
		return fmt.Errorf("seal root check with the new root key: %w", sealError)
	}
	compares := []store.Compare{
		{Key: types.KeyKeyringActive, Revision: activeFact.Revision},
		{Key: types.KeyKeyringRootCheck, Revision: rootCheckFact.Revision},
	}
	operations := []store.Op{{Type: store.OpPut, Key: types.KeyKeyringRootCheck, Value: sealedRootCheck}}
	for _, kekFact := range kekFacts {
		if strings.Contains(strings.TrimPrefix(kekFact.Key, types.ScanKeyringKEKs), "/") {
			continue
		}
		keyEncryptionKey, unwrapError := currentKeyProvider.UnwrapKey(ctx, kekFact.Value)
		if unwrapError != nil {
			return fmt.Errorf("unwrap %s with the current root key: %w", kekFact.Key, unwrapError)
		}
		rewrappedKEK, wrapError := nextKeyProvider.WrapKey(ctx, keyEncryptionKey)
		if wrapError != nil {
			return fmt.Errorf("wrap %s with the new root key: %w", kekFact.Key, wrapError)
		}
		compares = append(compares, store.Compare{Key: kekFact.Key, Revision: kekFact.Revision})
		operations = append(operations, store.Op{Type: store.OpPut, Key: kekFact.Key, Value: rewrappedKEK})
	}
	transactionSucceeded, transactionError := factStore.Transaction(ctx, compares, operations, nil)
	if transactionError != nil {
		return fmt.Errorf("re-wrap keyring under the new root key: %w", transactionError)
	}
	if !transactionSucceeded {
		return errors.New("re-wrap keyring under the new root key: the keyring changed concurrently (is an API replica running?); nothing was changed")
	}
	return nil
}

// swapRootKeyFiles moves {path} to {path}.old (when present) and {path}.next
// to {path}, then syncs the directory so the swap survives a crash.
func swapRootKeyFiles(rootKeyPath string, nextKeyPath string, oldKeyPath string) error {
	if renameError := os.Rename(rootKeyPath, oldKeyPath); renameError != nil && !errors.Is(renameError, fs.ErrNotExist) {
		return fmt.Errorf("keep the old root key as %s: %w", oldKeyPath, renameError)
	}
	if renameError := os.Rename(nextKeyPath, rootKeyPath); renameError != nil {
		return fmt.Errorf("install the new root key at %s: %w", rootKeyPath, renameError)
	}
	keyDirectory, openError := os.Open(filepath.Dir(rootKeyPath))
	if openError != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(rootKeyPath), openError)
	}
	defer func() { _ = keyDirectory.Close() }()
	return keyDirectory.Sync()
}
