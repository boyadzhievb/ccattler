// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// KeyringRotationState is the state of a KEK rotation (Flow 30).
type KeyringRotationState string

// KeyringRotationInProgress: the gate passed; the keyring-rotation controller
// creates the new KEK, switches to it and re-wraps envelopes.
const KeyringRotationInProgress KeyringRotationState = "in_progress"

// KeyringRotationStatus is the JSON stored at observed/keyring/rotation.
type KeyringRotationStatus struct {
	State         KeyringRotationState `json:"state"`          // State is the rotation state.
	TargetVersion int                  `json:"target_version"` // TargetVersion is the KEK version being introduced.
	Started       string               `json:"started"`        // Started is the RFC 3339 time the gate passed.
}

// keyringRotationIntent is the JSON stored at intent/keyring/rotation.
type keyringRotationIntent struct {
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
	intentJSON, intentError := json.Marshal(keyringRotationIntent{TargetVersion: targetVersion, RequestedBy: requestedBy, RequestedAt: startedAt})
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
