// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// KeyringRotationState is the state of a KEK rotation (Flow 30).
type KeyringRotationState string

const (
	// KeyringRotationInProgress: the gate passed; the keyring-rotation
	// controller creates the new KEK, switches to it and re-wraps envelopes.
	KeyringRotationInProgress KeyringRotationState = "in_progress"
	// KeyringRotationCompleted: every envelope names the new KEK and every
	// replaced version is retiring.
	KeyringRotationCompleted KeyringRotationState = "completed"
)

// KEKVersionState is the lifecycle state of one KEK version, stored at
// keyring/kek/{version}/state (Flow 30).
type KEKVersionState string

const (
	// KEKVersionPending: created by a rotation, not active yet.
	KEKVersionPending KEKVersionState = "pending"
	// KEKVersionActive: new envelopes are sealed with it.
	KEKVersionActive KEKVersionState = "active"
	// KEKVersionDraining: replaced; envelopes naming it are being re-wrapped.
	KEKVersionDraining KEKVersionState = "draining"
	// KEKVersionRetiring: no envelope names it; deleted after the retire grace.
	KEKVersionRetiring KEKVersionState = "retiring"
)

// KeyringRotationStatus is the JSON stored at observed/keyring/rotation.
type KeyringRotationStatus struct {
	State           KeyringRotationState `json:"state"`               // State is the rotation state.
	TargetVersion   int                  `json:"target_version"`      // TargetVersion is the KEK version being introduced.
	Rewrapped       int                  `json:"rewrapped"`           // Rewrapped counts envelopes moved to the target KEK so far.
	Remaining       int                  `json:"remaining"`           // Remaining counts envelopes still naming another KEK version.
	UnreadableCount int                  `json:"unreadable_count"`    // UnreadableCount counts envelopes that cannot be re-wrapped (malformed, unknown KEK).
	Started         string               `json:"started"`             // Started is the RFC 3339 time the gate passed.
	Completed       string               `json:"completed,omitempty"` // Completed is the RFC 3339 completion time.
}

// KeyringRotationIntent is the JSON stored at intent/keyring/rotation.
type KeyringRotationIntent struct {
	TargetVersion int    `json:"target_version"` // TargetVersion is the KEK version to create and activate.
	RequestedBy   string `json:"requested_by"`   // RequestedBy is the principal that passed the gate.
	RequestedAt   string `json:"requested_at"`   // RequestedAt is the RFC 3339 time the gate passed.
}

// allowsKEKRotation reports whether a KEK rotation may start in this keyring
// state: never while v1 envelopes may exist or be written (M178 decision).
func (legacyKeyState LegacyKeyState) allowsKEKRotation() bool {
	return legacyKeyState == LegacyKeyNone || legacyKeyState == LegacyKeyRetired
}

// CheckKeyringRotationGate returns every unmet KEK rotation condition (empty
// when the gate passes): keyring/legacy none or retired, no rotation running,
// and every live API replica on this build with the active KEK and root_check ok.
func (keyringCodec *KeyringEnvelopeCodec) CheckKeyringRotationGate(ctx context.Context, buildVersion string) ([]string, error) {
	var unmetConditions []string
	if legacyKeyState := keyringCodec.currentSnapshot().legacyKeyState; !legacyKeyState.allowsKEKRotation() {
		unmetConditions = append(unmetConditions, fmt.Sprintf(
			"keyring is %s: finish the legacy key migration and retire the legacy key first (cca secret migrate-keys, cca secret retire-legacy-key)", legacyKeyState))
	}
	if _, getError := keyringCodec.factStore.Get(ctx, types.KeyIntentKeyringRotation); getError == nil {
		unmetConditions = append(unmetConditions, "a KEK rotation is already running (see "+types.KeyObservedKeyringRotation+")")
	} else if !errors.Is(getError, store.ErrKeyNotFound) {
		return nil, fmt.Errorf("read %s: %w", types.KeyIntentKeyringRotation, getError)
	}
	replicaConditions, replicaError := keyringCodec.checkReplicaReports(ctx, buildVersion, "")
	if replicaError != nil {
		return nil, replicaError
	}
	return append(unmetConditions, replicaConditions...), nil
}

// StartKeyringRotation records intent/keyring/rotation for KEK version
// active + 1 and the in_progress status in one transaction guarded by the
// revisions of keyring/active and keyring/legacy (intent create-only), so two
// concurrent requests cannot both start one. Returns the target version.
func (keyringCodec *KeyringEnvelopeCodec) StartKeyringRotation(ctx context.Context, requestedBy string) (int, error) {
	activeFact, activeError := keyringCodec.factStore.Get(ctx, types.KeyKeyringActive)
	if activeError != nil {
		return 0, fmt.Errorf("read %s: %w", types.KeyKeyringActive, activeError)
	}
	activeKEKVersion, parseError := strconv.Atoi(string(activeFact.Value))
	if parseError != nil || activeKEKVersion < keyringFirstKEKVersion {
		return 0, fmt.Errorf("%s holds an invalid version %q", types.KeyKeyringActive, activeFact.Value)
	}
	legacyFact, legacyError := keyringCodec.factStore.Get(ctx, types.KeyKeyringLegacy)
	if legacyError != nil {
		return 0, fmt.Errorf("read %s: %w", types.KeyKeyringLegacy, legacyError)
	}
	if !LegacyKeyState(legacyFact.Value).allowsKEKRotation() {
		return 0, fmt.Errorf("keyring is %s: KEK rotation needs none or retired", legacyFact.Value)
	}
	rotationOperations, buildError := buildKeyringRotationStartOperations(activeKEKVersion+1, requestedBy)
	if buildError != nil {
		return 0, buildError
	}
	transactionSucceeded, transactionError := keyringCodec.factStore.Transaction(ctx, []store.Compare{
		{Key: types.KeyKeyringActive, Revision: activeFact.Revision},
		{Key: types.KeyKeyringLegacy, Revision: legacyFact.Revision},
		{Key: types.KeyIntentKeyringRotation, Revision: 0},
	}, rotationOperations, nil)
	if transactionError != nil {
		return 0, fmt.Errorf("start KEK rotation: %w", transactionError)
	}
	if !transactionSucceeded {
		return 0, errors.New("start KEK rotation: a rotation started or the keyring changed concurrently; check it and retry")
	}
	return activeKEKVersion + 1, nil
}

// buildKeyringRotationStartOperations returns the puts of the rotation intent
// and its in_progress status for targetVersion.
func buildKeyringRotationStartOperations(targetVersion int, requestedBy string) ([]store.Op, error) {
	startedAt := time.Now().UTC().Format(time.RFC3339)
	intentJSON, intentError := json.Marshal(KeyringRotationIntent{TargetVersion: targetVersion, RequestedBy: requestedBy, RequestedAt: startedAt})
	if intentError != nil {
		return nil, intentError
	}
	statusJSON, statusError := json.Marshal(KeyringRotationStatus{State: KeyringRotationInProgress, TargetVersion: targetVersion, Started: startedAt})
	if statusError != nil {
		return nil, statusError
	}
	return []store.Op{
		{Type: store.OpPut, Key: types.KeyIntentKeyringRotation, Value: intentJSON},
		{Type: store.OpPut, Key: types.KeyObservedKeyringRotation, Value: statusJSON},
	}, nil
}

// KEKRewrapBatch is one bounded round of a KEK rotation's re-wrap.
type KEKRewrapBatch struct {
	Rewrites              []store.Fact // Rewrites are re-wrapped envelopes (Key, Value, Revision of the scanned fact for the CAS guard).
	EnvelopesByKEKVersion map[int]int  // EnvelopesByKEKVersion counts envelopes naming each KEK version after this batch, unreadable ones included.
	UnreadableCount       int          // UnreadableCount counts envelopes that cannot be re-wrapped (malformed, v1, unknown KEK).
}

// CreateWrappedKEK generates a fresh KEK and returns it wrapped by the root
// key, the value stored at keyring/kek/{version} (Flow 30 step a).
func (keyringCodec *KeyringEnvelopeCodec) CreateWrappedKEK(ctx context.Context) ([]byte, error) {
	keyEncryptionKey, generateError := GenerateDEK()
	if generateError != nil {
		return nil, generateError
	}
	wrappedKEK, wrapError := keyringCodec.rootKeyProvider.WrapKey(ctx, keyEncryptionKey)
	if wrapError != nil {
		return nil, fmt.Errorf("wrap new KEK with the root key: %w", wrapError)
	}
	return wrappedKEK, nil
}

// PlanKEKRewrapBatch re-wraps up to batchLimit v2 envelopes found in facts
// (secrets/ base64-encoded, credentials/ raw) whose KEK version is not
// activeKEKVersion, and counts the envelopes naming each version afterwards.
// Envelopes it cannot re-wrap are counted, never rewritten or deleted.
func (keyringCodec *KeyringEnvelopeCodec) PlanKEKRewrapBatch(ctx context.Context, facts []store.Fact, activeKEKVersion int, batchLimit int) (KEKRewrapBatch, error) {
	keyEncryptionKeys, keyringError := keyringCodec.keyEncryptionKeysIncluding(ctx, activeKEKVersion)
	if keyringError != nil {
		return KEKRewrapBatch{}, keyringError
	}
	rewrapBatch := KEKRewrapBatch{EnvelopesByKEKVersion: make(map[int]int)}
	for _, fact := range facts {
		isSecretValue := strings.HasPrefix(fact.Key, SecretStorePrefix)
		if !isSecretValue && !strings.HasPrefix(fact.Key, CredentialStorePrefix) {
			continue
		}
		envelope, isV1 := decodeStoredEnvelope(fact.Value, isSecretValue)
		envelopeKEKVersion, _, _, parseError := parseEnvelopeV2(envelope)
		if isV1 || parseError != nil {
			rewrapBatch.UnreadableCount++
			continue
		}
		rewrapBatch.EnvelopesByKEKVersion[envelopeKEKVersion]++
		currentKEK := keyEncryptionKeys[envelopeKEKVersion]
		if envelopeKEKVersion == activeKEKVersion {
			continue
		}
		if currentKEK == nil {
			rewrapBatch.UnreadableCount++
			continue
		}
		if len(rewrapBatch.Rewrites) >= batchLimit {
			continue
		}
		rewrapped, rewrapError := rewrapEnvelopeV2(envelope, currentKEK, keyEncryptionKeys[activeKEKVersion], activeKEKVersion)
		if rewrapError != nil {
			rewrapBatch.UnreadableCount++
			continue
		}
		if isSecretValue {
			rewrapped = []byte(base64.StdEncoding.EncodeToString(rewrapped))
		}
		rewrapBatch.Rewrites = append(rewrapBatch.Rewrites, store.Fact{Key: fact.Key, Value: rewrapped, Revision: fact.Revision})
		rewrapBatch.EnvelopesByKEKVersion[envelopeKEKVersion]--
		rewrapBatch.EnvelopesByKEKVersion[activeKEKVersion]++
	}
	return rewrapBatch, nil
}

// keyEncryptionKeysIncluding returns the unwrapped KEKs, re-reading the
// keyring once when requiredVersion is missing (the watch may lag behind the
// controller's scan); still missing is an error, retried next cycle.
func (keyringCodec *KeyringEnvelopeCodec) keyEncryptionKeysIncluding(ctx context.Context, requiredVersion int) (map[int][]byte, error) {
	if keyEncryptionKeys := keyringCodec.currentSnapshot().keyEncryptionKeys; keyEncryptionKeys[requiredVersion] != nil {
		return keyEncryptionKeys, nil
	}
	if refreshError := keyringCodec.Refresh(ctx); refreshError != nil {
		return nil, refreshError
	}
	keyEncryptionKeys := keyringCodec.currentSnapshot().keyEncryptionKeys
	if keyEncryptionKeys[requiredVersion] == nil {
		return nil, fmt.Errorf("KEK version %d is not in the keyring", requiredVersion)
	}
	return keyEncryptionKeys, nil
}

// ReplicasNotOnKEK returns the API replicas whose keyring report in facts
// (observed/keyring/replica/*) does not seal with kekVersion, and how many
// reports there are; an unreadable report counts as not on it.
func ReplicasNotOnKEK(facts []store.Fact, kekVersion int) ([]string, int) {
	var replicasNotOnKEK []string
	reportCount := 0
	for _, reportFact := range store.FactsWithPrefix(facts, types.ScanObservedKeyringReplicas) {
		reportCount++
		var report keyringReplicaReport
		if json.Unmarshal(reportFact.Value, &report) != nil || report.ActiveKEKVersion != kekVersion {
			replicasNotOnKEK = append(replicasNotOnKEK, strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas))
		}
	}
	return replicasNotOnKEK, reportCount
}
