// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// KeyringMigrationState is the state of the legacy key migration (Flow 29).
type KeyringMigrationState string

const (
	// KeyringMigrationNotStarted: upgraded cluster before the migration gate; writes are v1.
	KeyringMigrationNotStarted KeyringMigrationState = "not_started"
	// KeyringMigrationInProgress: after the gate; writes are v2, v1 envelopes being re-encrypted.
	KeyringMigrationInProgress KeyringMigrationState = "in_progress"
	// KeyringMigrationCompleted: no v1 envelope left (or a fresh cluster that never had one).
	KeyringMigrationCompleted KeyringMigrationState = "completed"
)

// LegacyKeyState records whether the legacy master key still matters.
type LegacyKeyState string

const (
	// LegacyKeyNone: fresh cluster, no legacy key and no v1 envelope ever existed.
	LegacyKeyNone LegacyKeyState = "none"
	// LegacyKeyPresent: v1 envelopes may exist; the legacy key opens them.
	LegacyKeyPresent LegacyKeyState = "present"
	// LegacyKeyRetired: no replica loads the legacy key or opens v1 envelopes.
	LegacyKeyRetired LegacyKeyState = "retired"
)

// KeyringMigrationStatus is the JSON stored at observed/keyring/migration.
type KeyringMigrationStatus struct {
	State                 KeyringMigrationState `json:"state"`                    // State is the migration state.
	LegacyV1EnvelopeCount int                   `json:"legacy_v1_envelope_count"` // LegacyV1EnvelopeCount counts envelopes still in v1.
	UnreadableCount       int                   `json:"unreadable_count"`         // UnreadableCount counts v1 envelopes the legacy key cannot open.
	Migrated              int                   `json:"migrated"`                 // Migrated counts envelopes re-encrypted so far.
	Started               string                `json:"started,omitempty"`        // Started is the RFC 3339 start time.
	Completed             string                `json:"completed,omitempty"`      // Completed is the RFC 3339 completion time.
}

// writesV2 reports whether new envelopes must be v2 in this migration state.
func (migrationState KeyringMigrationState) writesV2() bool {
	return migrationState == KeyringMigrationInProgress || migrationState == KeyringMigrationCompleted
}

// keyringFirstKEKVersion is the KEK version created at bootstrap.
const keyringFirstKEKVersion = 1

// keyringKEKStateActive marks the KEK version in use.
const keyringKEKStateActive = "active"

// keyringRootCheckPlaintext is the fixed value sealed in keyring/root_check.
var keyringRootCheckPlaintext = []byte("ccattler keyring root check v1")

// encryptedStorePrefixes are the store areas holding envelopes; a cluster
// with any key here is not fresh.
var encryptedStorePrefixes = []string{SecretStorePrefix, CredentialStorePrefix}

// ErrKeyringRootCheckFailed means this replica's root key differs from the
// one that created the keyring; the replica must not serve.
var ErrKeyringRootCheckFailed = errors.New("keyring/root_check cannot be opened with this root key (different root key than the other replicas)")

// keyringSnapshot is the in-memory view of the keyring a replica seals and
// opens with: unwrapped KEKs by version, the active version, the legacy key
// state and the migration state.
type keyringSnapshot struct {
	keyEncryptionKeys map[int][]byte        // keyEncryptionKeys are the unwrapped KEKs by version.
	activeKEKVersion  int                   // activeKEKVersion seals new v2 envelopes.
	legacyKeyState    LegacyKeyState        // legacyKeyState says whether v1 envelopes may be opened.
	migrationState    KeyringMigrationState // migrationState decides v1 vs v2 writes.
}

// ensureKeyring creates the keyring once, in one create-only transaction
// (compare keyring/active absent). A fresh cluster (no legacy key, no
// envelope) starts with migration completed; otherwise not_started. Losing
// the race to another replica is not an error.
func ensureKeyring(ctx context.Context, factStore store.StateStore, rootKeyProvider KeyProvider, legacyKeyAvailable bool) error {
	if _, getError := factStore.Get(ctx, types.KeyKeyringActive); getError == nil {
		return nil
	} else if !errors.Is(getError, store.ErrKeyNotFound) {
		return fmt.Errorf("read %s: %w", types.KeyKeyringActive, getError)
	}
	hasEnvelopes, scanError := storeHasEnvelopes(ctx, factStore)
	if scanError != nil {
		return scanError
	}
	if hasEnvelopes && !legacyKeyAvailable {
		return errors.New("secrets exist but the legacy secret-master.key is missing; restore it from another control-plane replica before upgrading")
	}
	bootstrapOperations, buildError := buildKeyringBootstrapOperations(ctx, rootKeyProvider, legacyKeyAvailable)
	if buildError != nil {
		return buildError
	}
	if _, transactionError := factStore.Transaction(ctx,
		[]store.Compare{{Key: types.KeyKeyringActive, Revision: 0}}, bootstrapOperations, nil); transactionError != nil {
		return fmt.Errorf("create keyring: %w", transactionError)
	}
	return nil
}

// storeHasEnvelopes reports whether any secret or credential envelope exists.
func storeHasEnvelopes(ctx context.Context, factStore store.StateStore) (bool, error) {
	for _, encryptedPrefix := range encryptedStorePrefixes {
		facts, scanError := factStore.Scan(ctx, encryptedPrefix)
		if scanError != nil {
			return false, fmt.Errorf("scan %s: %w", encryptedPrefix, scanError)
		}
		if len(facts) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// buildKeyringBootstrapOperations generates KEK 1, wraps it and the root check
// with the root key, and returns the puts of the bootstrap transaction.
func buildKeyringBootstrapOperations(ctx context.Context, rootKeyProvider KeyProvider, legacyKeyAvailable bool) ([]store.Op, error) {
	firstKEK, generateError := GenerateDEK()
	if generateError != nil {
		return nil, generateError
	}
	wrappedKEK, wrapError := rootKeyProvider.WrapKey(ctx, firstKEK)
	if wrapError != nil {
		return nil, fmt.Errorf("wrap KEK with root key: %w", wrapError)
	}
	sealedRootCheck, rootCheckError := rootKeyProvider.WrapKey(ctx, keyringRootCheckPlaintext)
	if rootCheckError != nil {
		return nil, fmt.Errorf("seal root check: %w", rootCheckError)
	}
	legacyKeyState, migrationStatus := LegacyKeyNone, KeyringMigrationStatus{
		State: KeyringMigrationCompleted, Completed: time.Now().UTC().Format(time.RFC3339)}
	if legacyKeyAvailable {
		legacyKeyState, migrationStatus = LegacyKeyPresent, KeyringMigrationStatus{State: KeyringMigrationNotStarted}
	}
	migrationJSON, marshalError := json.Marshal(migrationStatus)
	if marshalError != nil {
		return nil, marshalError
	}
	return []store.Op{
		{Type: store.OpPut, Key: types.KeyKeyringKEK(keyringFirstKEKVersion), Value: wrappedKEK},
		{Type: store.OpPut, Key: types.KeyKeyringKEKState(keyringFirstKEKVersion), Value: []byte(keyringKEKStateActive)},
		{Type: store.OpPut, Key: types.KeyKeyringActive, Value: []byte(strconv.Itoa(keyringFirstKEKVersion))},
		{Type: store.OpPut, Key: types.KeyKeyringRootCheck, Value: sealedRootCheck},
		{Type: store.OpPut, Key: types.KeyKeyringLegacy, Value: []byte(legacyKeyState)},
		{Type: store.OpPut, Key: types.KeyObservedKeyringMigration, Value: migrationJSON},
	}, nil
}

// loadKeyringSnapshot reads the keyring, proves the root key with root_check
// and unwraps every KEK version. Any inconsistency is an error.
func loadKeyringSnapshot(ctx context.Context, factStore store.StateStore, rootKeyProvider KeyProvider) (*keyringSnapshot, error) {
	if rootCheckError := verifyKeyringRootCheck(ctx, factStore, rootKeyProvider); rootCheckError != nil {
		return nil, rootCheckError
	}
	keyEncryptionKeys, kekError := unwrapKeyringKEKs(ctx, factStore, rootKeyProvider)
	if kekError != nil {
		return nil, kekError
	}
	activeFact, activeError := factStore.Get(ctx, types.KeyKeyringActive)
	if activeError != nil {
		return nil, fmt.Errorf("read %s: %w", types.KeyKeyringActive, activeError)
	}
	activeKEKVersion, parseError := strconv.Atoi(string(activeFact.Value))
	if parseError != nil || keyEncryptionKeys[activeKEKVersion] == nil {
		return nil, fmt.Errorf("%s = %q names no KEK in the keyring", types.KeyKeyringActive, activeFact.Value)
	}
	legacyFact, legacyError := factStore.Get(ctx, types.KeyKeyringLegacy)
	if legacyError != nil {
		return nil, fmt.Errorf("read %s: %w", types.KeyKeyringLegacy, legacyError)
	}
	migrationStatus, migrationError := readKeyringMigrationStatus(ctx, factStore)
	if migrationError != nil {
		return nil, migrationError
	}
	return &keyringSnapshot{
		keyEncryptionKeys: keyEncryptionKeys,
		activeKEKVersion:  activeKEKVersion,
		legacyKeyState:    LegacyKeyState(legacyFact.Value),
		migrationState:    migrationStatus.State,
	}, nil
}

// verifyKeyringRootCheck opens keyring/root_check with the root key.
func verifyKeyringRootCheck(ctx context.Context, factStore store.StateStore, rootKeyProvider KeyProvider) error {
	rootCheckFact, getError := factStore.Get(ctx, types.KeyKeyringRootCheck)
	if getError != nil {
		return fmt.Errorf("read %s: %w", types.KeyKeyringRootCheck, getError)
	}
	openedRootCheck, unwrapError := rootKeyProvider.UnwrapKey(ctx, rootCheckFact.Value)
	if unwrapError != nil || subtle.ConstantTimeCompare(openedRootCheck, keyringRootCheckPlaintext) != 1 {
		return ErrKeyringRootCheckFailed
	}
	return nil
}

// unwrapKeyringKEKs unwraps every keyring/kek/{version} with the root key;
// the keyring/kek/{version}/state keys under the same prefix are skipped.
func unwrapKeyringKEKs(ctx context.Context, factStore store.StateStore, rootKeyProvider KeyProvider) (map[int][]byte, error) {
	kekFacts, scanError := factStore.Scan(ctx, types.ScanKeyringKEKs)
	if scanError != nil {
		return nil, fmt.Errorf("scan %s: %w", types.ScanKeyringKEKs, scanError)
	}
	keyEncryptionKeys := make(map[int][]byte, len(kekFacts))
	for _, kekFact := range kekFacts {
		versionText := strings.TrimPrefix(kekFact.Key, types.ScanKeyringKEKs)
		if strings.Contains(versionText, "/") {
			continue
		}
		kekVersion, parseError := strconv.Atoi(versionText)
		if parseError != nil {
			return nil, fmt.Errorf("keyring key %s has no numeric version", kekFact.Key)
		}
		unwrappedKEK, unwrapError := rootKeyProvider.UnwrapKey(ctx, kekFact.Value)
		if unwrapError != nil {
			return nil, fmt.Errorf("unwrap KEK %d with the root key: %w", kekVersion, unwrapError)
		}
		keyEncryptionKeys[kekVersion] = unwrappedKEK
	}
	return keyEncryptionKeys, nil
}

// readKeyringMigrationStatus parses observed/keyring/migration.
func readKeyringMigrationStatus(ctx context.Context, factStore store.StateStore) (KeyringMigrationStatus, error) {
	var migrationStatus KeyringMigrationStatus
	migrationFact, getError := factStore.Get(ctx, types.KeyObservedKeyringMigration)
	if getError != nil {
		return migrationStatus, fmt.Errorf("read %s: %w", types.KeyObservedKeyringMigration, getError)
	}
	if unmarshalError := json.Unmarshal(migrationFact.Value, &migrationStatus); unmarshalError != nil {
		return migrationStatus, fmt.Errorf("parse %s: %w", types.KeyObservedKeyringMigration, unmarshalError)
	}
	return migrationStatus, nil
}
