// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// RootKeyNextSuffix names the new root key file while a rotation runs.
const RootKeyNextSuffix = ".next"

// RootKeyOldSuffix names the replaced root key file, kept for a rollback
// until the operator deletes or archives it.
const RootKeyOldSuffix = ".old"

// rootKeyRotationPollInterval is how often the live replica reports are re-read
// while waiting for every API replica to stop.
const rootKeyRotationPollInterval = time.Second

// rootKeyFingerprintLength is the number of hex characters of the key's
// SHA-256 printed to identify it (never the key itself).
const rootKeyFingerprintLength = 16

// RootKeyFileState says whether a root key file exists and opens the keyring.
type RootKeyFileState string

const (
	// RootKeyFileAbsent: no file at the path.
	RootKeyFileAbsent RootKeyFileState = "absent"
	// RootKeyFileOpens: the file opens keyring/root_check.
	RootKeyFileOpens RootKeyFileState = "opens"
	// RootKeyFileDoesNotOpen: the file is valid but does not open keyring/root_check.
	RootKeyFileDoesNotOpen RootKeyFileState = "no"
)

// RootKeyRotationStage is where a host is in a root key rotation, derived
// from its key files and the keyring (cca secret root-key-status).
type RootKeyRotationStage string

const (
	// RootKeyStageIdle: the key file opens the keyring and there is no .next.
	RootKeyStageIdle RootKeyRotationStage = "idle"
	// RootKeyStageStaged: the key file opens the keyring and a .next waits; nothing committed.
	RootKeyStageStaged RootKeyRotationStage = "staged"
	// RootKeyStageCommitted: the keyring is under .next; the file swap is pending.
	RootKeyStageCommitted RootKeyRotationStage = "committed"
	// RootKeyStageInvalid: no file opens the keyring (wrong files or KMS mode).
	RootKeyStageInvalid RootKeyRotationStage = "invalid"
)

// RootKeyFilesStatus is the state of one host's root key files.
type RootKeyFilesStatus struct {
	Current   RootKeyFileState     // Current is the key file at the configured path.
	Next      RootKeyFileState     // Next is {path}.next.
	OldExists bool                 // OldExists reports whether {path}.old exists.
	Stage     RootKeyRotationStage // Stage is derived from Current and Next.
}

// WaitForStoppedKeyringReplicas waits up to maxWait until no API replica
// report is left under observed/keyring/replica/ (their leases expire after a
// server stops). Still present at the deadline is an error naming them.
func WaitForStoppedKeyringReplicas(ctx context.Context, factStore store.StateStore, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for {
		liveReplicas, scanError := liveKeyringReplicas(ctx, factStore)
		if scanError != nil {
			return scanError
		}
		if len(liveReplicas) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("API replicas still running (stop cca-server on every control-plane host first): %s", strings.Join(liveReplicas, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rootKeyRotationPollInterval):
		}
	}
}

// liveKeyringReplicas lists the node IDs with a live keyring report.
func liveKeyringReplicas(ctx context.Context, factStore store.StateStore) ([]string, error) {
	reportFacts, scanError := factStore.Scan(ctx, types.ScanObservedKeyringReplicas)
	if scanError != nil {
		return nil, fmt.Errorf("scan %s: %w", types.ScanObservedKeyringReplicas, scanError)
	}
	liveReplicas := make([]string, 0, len(reportFacts))
	for _, reportFact := range reportFacts {
		liveReplicas = append(liveReplicas, strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas))
	}
	return liveReplicas, nil
}

// InspectRootKeyFiles reports which of {path} and {path}.next opens the
// keyring, whether {path}.old exists, and the resulting stage. Read-only.
func InspectRootKeyFiles(ctx context.Context, factStore store.StateStore, rootKeyPath string) (RootKeyFilesStatus, error) {
	var filesStatus RootKeyFilesStatus
	var inspectError error
	if _, filesStatus.Current, inspectError = loadRootKeyFileState(ctx, factStore, rootKeyPath); inspectError != nil {
		return filesStatus, inspectError
	}
	if _, filesStatus.Next, inspectError = loadRootKeyFileState(ctx, factStore, rootKeyPath+RootKeyNextSuffix); inspectError != nil {
		return filesStatus, inspectError
	}
	_, oldStatError := os.Lstat(rootKeyPath + RootKeyOldSuffix)
	filesStatus.OldExists = oldStatError == nil
	switch {
	case filesStatus.Current == RootKeyFileOpens && filesStatus.Next == RootKeyFileAbsent:
		filesStatus.Stage = RootKeyStageIdle
	case filesStatus.Current == RootKeyFileOpens:
		filesStatus.Stage = RootKeyStageStaged
	case filesStatus.Next == RootKeyFileOpens:
		filesStatus.Stage = RootKeyStageCommitted
	default:
		filesStatus.Stage = RootKeyStageInvalid
	}
	return filesStatus, nil
}

// CommitRootKeyRotation re-wraps the keyring under the staged {path}.next and
// swaps the files (Flow 30 part B, first host); it never generates a key. If
// the keyring is already under .next (crash after the commit) it only swaps.
// Returns the new key's fingerprint.
func CommitRootKeyRotation(ctx context.Context, factStore store.StateStore, rootKeyPath string, legacyKey []byte) (string, error) {
	filesStatus, inspectError := InspectRootKeyFiles(ctx, factStore, rootKeyPath)
	if inspectError != nil {
		return "", inspectError
	}
	if filesStatus.Stage == RootKeyStageCommitted {
		return finishStagedRootKey(ctx, factStore, rootKeyPath, filesStatus)
	}
	if filesStatus.Stage != RootKeyStageStaged {
		return "", fmt.Errorf("nothing to commit: stage is %s (generate %s%s and copy it to every replica first)", filesStatus.Stage, rootKeyPath, RootKeyNextSuffix)
	}
	if filesStatus.OldExists {
		return "", fmt.Errorf("%s%s exists from an earlier rotation: archive it with the snapshots it opens, or delete it, before rotating again", rootKeyPath, RootKeyOldSuffix)
	}
	currentKey, nextKey, loadError := loadValidatedRootKeyPair(rootKeyPath, legacyKey)
	if loadError != nil {
		return "", loadError
	}
	liveReplicas, scanError := liveKeyringReplicas(ctx, factStore)
	if scanError != nil {
		return "", scanError
	}
	if len(liveReplicas) > 0 {
		return "", fmt.Errorf("API replicas running, refusing to commit: %s", strings.Join(liveReplicas, ", "))
	}
	if rewrapError := rewrapKeyringUnderRootKey(ctx, factStore, currentKey, nextKey); rewrapError != nil {
		return "", rewrapError
	}
	if swapError := swapRootKeyFiles(rootKeyPath); swapError != nil {
		return "", fmt.Errorf("the keyring is under the new key, but the file swap failed (run the command again): %w", swapError)
	}
	installedKey, loadInstalledError := LoadSecretRootKeyFile(rootKeyPath)
	if loadInstalledError != nil {
		return "", loadInstalledError
	}
	return rootKeyFingerprint(installedKey), nil
}

// FinishRootKeyRotation installs {path}.next on a host whose keyring was
// already re-wrapped by CommitRootKeyRotation on the first host (Flow 30
// part B, other hosts). It never writes to etcd. A host already finished
// (its key opens, no .next) is a no-op. Returns the installed key's fingerprint.
func FinishRootKeyRotation(ctx context.Context, factStore store.StateStore, rootKeyPath string) (string, error) {
	filesStatus, inspectError := InspectRootKeyFiles(ctx, factStore, rootKeyPath)
	if inspectError != nil {
		return "", inspectError
	}
	switch filesStatus.Stage {
	case RootKeyStageCommitted:
		return finishStagedRootKey(ctx, factStore, rootKeyPath, filesStatus)
	case RootKeyStageIdle:
		currentKey, loadError := LoadSecretRootKeyFile(rootKeyPath)
		if loadError != nil {
			return "", loadError
		}
		return rootKeyFingerprint(currentKey), nil
	case RootKeyStageStaged:
		return "", errors.New("the keyring is not re-wrapped yet: run cca secret rotate-root-key --commit on the first host")
	default:
		return "", fmt.Errorf("neither %s nor %s%s opens keyring/root_check: wrong key files (or the cluster uses --kms-provider)", rootKeyPath, rootKeyPath, RootKeyNextSuffix)
	}
}

// finishStagedRootKey swaps {path}.next into place on a committed host,
// refusing to overwrite an existing .old with a key file that is still present.
func finishStagedRootKey(ctx context.Context, factStore store.StateStore, rootKeyPath string, filesStatus RootKeyFilesStatus) (string, error) {
	if filesStatus.OldExists && filesStatus.Current != RootKeyFileAbsent {
		return "", fmt.Errorf("%s%s exists from an earlier rotation: archive or delete it, then run the command again", rootKeyPath, RootKeyOldSuffix)
	}
	nextKey, loadError := LoadSecretRootKeyFile(rootKeyPath + RootKeyNextSuffix)
	if loadError != nil {
		return "", loadError
	}
	if swapError := swapRootKeyFiles(rootKeyPath); swapError != nil {
		return "", swapError
	}
	if _, installedState, verifyError := loadRootKeyFileState(ctx, factStore, rootKeyPath); verifyError != nil || installedState != RootKeyFileOpens {
		return "", fmt.Errorf("installed key does not open the keyring (%s): %v", installedState, verifyError)
	}
	return rootKeyFingerprint(nextKey), nil
}

// loadValidatedRootKeyPair loads the current and staged keys and refuses a
// staged key equal to the current key or to the legacy master key (R1).
func loadValidatedRootKeyPair(rootKeyPath string, legacyKey []byte) (KeyProvider, KeyProvider, error) {
	currentKey, currentError := LoadSecretRootKeyFile(rootKeyPath)
	if currentError != nil {
		return nil, nil, currentError
	}
	nextKey, nextError := LoadSecretRootKeyFile(rootKeyPath + RootKeyNextSuffix)
	if nextError != nil {
		return nil, nil, nextError
	}
	if subtle.ConstantTimeCompare(currentKey, nextKey) == 1 {
		return nil, nil, fmt.Errorf("%s%s is the current key; generate a fresh one", rootKeyPath, RootKeyNextSuffix)
	}
	if distinctError := ValidateSecretRootKeyDistinctFromLegacy(nextKey, legacyKey); distinctError != nil {
		return nil, nil, distinctError
	}
	currentKeyProvider, currentProviderError := NewLocalKeyProvider(currentKey)
	if currentProviderError != nil {
		return nil, nil, currentProviderError
	}
	nextKeyProvider, nextProviderError := NewLocalKeyProvider(nextKey)
	if nextProviderError != nil {
		return nil, nil, nextProviderError
	}
	return currentKeyProvider, nextKeyProvider, nil
}

// loadRootKeyFileState loads the root key file at keyPath and reports whether
// it opens keyring/root_check. A missing file is absent; an invalid file
// (mode, size) or a store error is an error.
func loadRootKeyFileState(ctx context.Context, factStore store.StateStore, keyPath string) (KeyProvider, RootKeyFileState, error) {
	rootKey, loadError := LoadSecretRootKeyFile(keyPath)
	if errors.Is(loadError, ErrSecretRootKeyMissing) {
		return nil, RootKeyFileAbsent, nil
	}
	if loadError != nil {
		return nil, "", loadError
	}
	rootKeyProvider, providerError := NewLocalKeyProvider(rootKey)
	if providerError != nil {
		return nil, "", providerError
	}
	verifyError := verifyKeyringRootCheck(ctx, factStore, rootKeyProvider)
	if errors.Is(verifyError, ErrKeyringRootCheckFailed) {
		return rootKeyProvider, RootKeyFileDoesNotOpen, nil
	}
	if verifyError != nil {
		return nil, "", verifyError
	}
	return rootKeyProvider, RootKeyFileOpens, nil
}

// rewrapKeyringUnderRootKey re-wraps every keyring/kek/{v} and root_check under
// the next root key in ONE transaction (built and dry-run by
// buildRootKeyRewrapTransaction, whose guards keep a stale replica from adding
// a KEK under the old root key meanwhile, review F1).
func rewrapKeyringUnderRootKey(ctx context.Context, factStore store.StateStore, currentKeyProvider KeyProvider, nextKeyProvider KeyProvider) error {
	compares, operations, buildError := buildRootKeyRewrapTransaction(ctx, factStore, currentKeyProvider, nextKeyProvider)
	if buildError != nil {
		return buildError
	}
	transactionSucceeded, transactionError := factStore.Transaction(ctx, compares, operations, nil)
	if transactionError != nil {
		return fmt.Errorf("re-wrap keyring under the new root key (run root-key-status: the commit may have landed): %w", transactionError)
	}
	if !transactionSucceeded {
		return errors.New("re-wrap keyring under the new root key: the keyring changed concurrently (is an API replica running?); nothing was changed")
	}
	return nil
}

// buildRootKeyRewrapTransaction reads the keyring, re-wraps it in memory,
// proves the next key unwraps each value, and returns the guarded transaction.
func buildRootKeyRewrapTransaction(ctx context.Context, factStore store.StateStore, currentKeyProvider KeyProvider, nextKeyProvider KeyProvider) ([]store.Compare, []store.Op, error) {
	if _, intentError := factStore.Get(ctx, types.KeyIntentKeyringRotation); !errors.Is(intentError, store.ErrKeyNotFound) {
		return nil, nil, fmt.Errorf("a KEK rotation is running or %s cannot be read (%v): wait until it completes", types.KeyIntentKeyringRotation, intentError)
	}
	activeFact, activeError := factStore.Get(ctx, types.KeyKeyringActive)
	if activeError != nil {
		return nil, nil, fmt.Errorf("read %s: %w", types.KeyKeyringActive, activeError)
	}
	rootCheckFact, rootCheckError := factStore.Get(ctx, types.KeyKeyringRootCheck)
	if rootCheckError != nil {
		return nil, nil, fmt.Errorf("read %s: %w", types.KeyKeyringRootCheck, rootCheckError)
	}
	kekFacts, scanError := factStore.Scan(ctx, types.ScanKeyringKEKs)
	if scanError != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", types.ScanKeyringKEKs, scanError)
	}
	sealedRootCheck, sealError := rewrapWithProof(ctx, nextKeyProvider, keyringRootCheckPlaintext)
	if sealError != nil {
		return nil, nil, fmt.Errorf("seal root check with the new root key: %w", sealError)
	}
	compares := []store.Compare{
		{Key: types.KeyKeyringActive, Revision: activeFact.Revision},
		{Key: types.KeyKeyringRootCheck, Revision: rootCheckFact.Revision},
		{Key: types.KeyIntentKeyringRotation, Revision: 0},
	}
	operations := []store.Op{{Type: store.OpPut, Key: types.KeyKeyringRootCheck, Value: sealedRootCheck}}
	highestKEKVersion := 0
	for _, kekFact := range kekFacts {
		versionText := strings.TrimPrefix(kekFact.Key, types.ScanKeyringKEKs)
		kekVersion, parseError := strconv.Atoi(versionText)
		if strings.Contains(versionText, "/") || parseError != nil {
			continue
		}
		highestKEKVersion = max(highestKEKVersion, kekVersion)
		keyEncryptionKey, unwrapError := currentKeyProvider.UnwrapKey(ctx, kekFact.Value)
		if unwrapError != nil {
			return nil, nil, fmt.Errorf("unwrap %s with the current root key: %w", kekFact.Key, unwrapError)
		}
		rewrappedKEK, wrapError := rewrapWithProof(ctx, nextKeyProvider, keyEncryptionKey)
		if wrapError != nil {
			return nil, nil, fmt.Errorf("wrap %s with the new root key: %w", kekFact.Key, wrapError)
		}
		compares = append(compares, store.Compare{Key: kekFact.Key, Revision: kekFact.Revision})
		operations = append(operations, store.Op{Type: store.OpPut, Key: kekFact.Key, Value: rewrappedKEK})
	}
	compares = append(compares,
		store.Compare{Key: types.KeyKeyringKEK(highestKEKVersion + 1), Revision: 0},
		store.Compare{Key: types.KeyKeyringKEKState(highestKEKVersion + 1), Revision: 0})
	return compares, operations, nil
}

// rewrapWithProof wraps plaintext with the next key and unwraps the result
// again, failing unless it returns the same bytes (a dry run before commit).
func rewrapWithProof(ctx context.Context, nextKeyProvider KeyProvider, plaintext []byte) ([]byte, error) {
	wrappedValue, wrapError := nextKeyProvider.WrapKey(ctx, plaintext)
	if wrapError != nil {
		return nil, wrapError
	}
	unwrappedValue, unwrapError := nextKeyProvider.UnwrapKey(ctx, wrappedValue)
	if unwrapError != nil || !bytes.Equal(unwrappedValue, plaintext) {
		return nil, fmt.Errorf("the new root key does not unwrap what it wrapped: %v", unwrapError)
	}
	return wrappedValue, nil
}

// swapRootKeyFiles moves {path} to {path}.old (when present) and {path}.next
// to {path}, then syncs the directory so the swap survives a crash.
func swapRootKeyFiles(rootKeyPath string) error {
	nextKeyPath, oldKeyPath := rootKeyPath+RootKeyNextSuffix, rootKeyPath+RootKeyOldSuffix
	if renameError := os.Rename(rootKeyPath, oldKeyPath); renameError != nil && !errors.Is(renameError, fs.ErrNotExist) {
		return fmt.Errorf("keep the old root key as %s: %w", oldKeyPath, renameError)
	}
	if renameError := os.Rename(nextKeyPath, rootKeyPath); renameError != nil {
		return fmt.Errorf("install the new root key at %s: %w", rootKeyPath, renameError)
	}
	return syncDirectory(filepath.Dir(rootKeyPath))
}

// syncDirectory fsyncs a directory so renames and new entries in it survive a crash.
func syncDirectory(directoryPath string) error {
	keyDirectory, openError := os.Open(directoryPath) //nolint:gosec // G304: operator-supplied key directory
	if openError != nil {
		return fmt.Errorf("sync %s: %w", directoryPath, openError)
	}
	defer func() { _ = keyDirectory.Close() }()
	if syncError := keyDirectory.Sync(); syncError != nil {
		return fmt.Errorf("sync %s: %w", directoryPath, syncError)
	}
	return nil
}

// rootKeyFingerprint identifies a key by the first hex characters of its
// SHA-256, so operators can compare copies without printing the key.
func rootKeyFingerprint(rootKey []byte) string {
	keyDigest := sha256.Sum256(rootKey)
	return hex.EncodeToString(keyDigest[:])[:rootKeyFingerprintLength]
}
