// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// keyringRewrapBatchSize caps the envelopes re-wrapped per cycle; with the
// status put it stays well under maxTransactionChanges.
const keyringRewrapBatchSize = 40

// DefaultKeyringRetireGrace is how long a retiring KEK version is kept before
// it is deleted (cca server --keyring-retire-grace).
const DefaultKeyringRetireGrace = 7 * 24 * time.Hour

// keyringRotationSwitchGroup groups the keyring/active switch and the state
// changes that go with it, so the runner commits them together.
const keyringRotationSwitchGroup = "keyring-rotation-switch"

// KeyringRotationPlanner creates KEKs and plans re-wrap batches
// (implemented by security.KeyringEnvelopeCodec).
type KeyringRotationPlanner interface {
	// CreateWrappedKEK returns a fresh KEK wrapped by the root key.
	CreateWrappedKEK(ctx context.Context) ([]byte, error)
	// PlanKEKRewrapBatch re-wraps up to batchLimit envelopes onto activeKEKVersion.
	PlanKEKRewrapBatch(ctx context.Context, facts []store.Fact, activeKEKVersion int, batchLimit int) (security.KEKRewrapBatch, error)
	// ProveKEKVersion fails unless the KEK of kekVersion unwraps with the root key.
	ProveKEKVersion(ctx context.Context, kekVersion int) error
}

// KeyringRotationController carries out a KEK rotation (Flow 30): it creates
// the new KEK, switches keyring/active to it, re-wraps envelopes in bounded
// batches, retires replaced versions once nothing names them and deletes them
// after the retire grace. Every step is decided from stored facts only.
type KeyringRotationController struct {
	rotationPlanner KeyringRotationPlanner // rotationPlanner holds the root key and the unwrapped KEKs.
	retireGrace     time.Duration          // retireGrace is how long a retiring version is kept.
	timeNow         func() time.Time       // timeNow returns the current time (replaced in tests).
}

// keyringRotationView is the keyring state parsed from the scanned facts.
type keyringRotationView struct {
	activeKEKVersion  int                              // activeKEKVersion is keyring/active.
	wrappedKEKs       map[int]bool                     // wrappedKEKs marks versions whose keyring/kek/{v} exists.
	kekVersionStates  map[int]security.KEKVersionState // kekVersionStates holds keyring/kek/{v}/state.
	retiringSince     map[int]string                   // retiringSince holds keyring/kek/{v}/retiring_since.
	rotationIntent    *security.KeyringRotationIntent  // rotationIntent is intent/keyring/rotation, nil when absent.
	rotationStatus    security.KeyringRotationStatus   // rotationStatus is observed/keyring/rotation.
	rotationStatusRaw []byte                           // rotationStatusRaw is the stored status, to skip unchanged writes.
	legacyKeyState    security.LegacyKeyState          // legacyKeyState is keyring/legacy (rotation needs none or retired).
}

// NewKeyringRotationController creates the controller around the server's
// keyring codec; retireGrace is the retire grace (DefaultKeyringRetireGrace).
func NewKeyringRotationController(rotationPlanner KeyringRotationPlanner, retireGrace time.Duration) *KeyringRotationController {
	return &KeyringRotationController{rotationPlanner: rotationPlanner, retireGrace: retireGrace, timeNow: time.Now}
}

// Name returns "keyring-rotation", identifying this controller in logs.
func (keyringRotationController *KeyringRotationController) Name() string {
	return "keyring-rotation"
}

// Watch returns the keyring, the rotation intent and status, the replica
// reports (retirement waits for them) and the envelope areas.
func (keyringRotationController *KeyringRotationController) Watch() []string {
	return []string{
		types.PrefixKeyring + "/",
		types.KeyIntentKeyringRotation,
		types.KeyObservedKeyringRotation,
		types.ScanObservedKeyringReplicas,
		security.SecretStorePrefix,
		security.CredentialStorePrefix,
	}
}

// Reconcile does the first rotation step not done yet: create the target KEK,
// switch to it, re-wrap one batch, then retire, complete and delete versions.
func (keyringRotationController *KeyringRotationController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	keyringView, hasKeyring := parseKeyringRotationView(facts)
	if !hasKeyring || (keyringView.rotationIntent == nil && !hasNonActiveKEKVersion(keyringView)) {
		return nil, nil
	}
	if rotationIntent := keyringView.rotationIntent; rotationIntent != nil && keyringView.activeKEKVersion < rotationIntent.TargetVersion {
		return keyringRotationController.advanceToTargetKEK(ctx, keyringView, rotationIntent.TargetVersion)
	}
	rewrapBatch, planError := keyringRotationController.rotationPlanner.PlanKEKRewrapBatch(ctx, facts, keyringView.activeKEKVersion, keyringRewrapBatchSize)
	if planError != nil {
		return nil, planError
	}
	var changes []Change
	for _, rewrite := range rewrapBatch.Rewrites {
		changes = append(changes, Change{Type: store.OpPut, Key: rewrite.Key, Value: rewrite.Value})
	}
	nextStates := keyringView.kekVersionStates
	if len(rewrapBatch.Rewrites) == 0 {
		var transitionChanges []Change
		transitionChanges, nextStates = keyringRotationController.planVersionTransitions(keyringView, rewrapBatch, facts)
		changes = append(changes, transitionChanges...)
	}
	statusChanges, statusError := keyringRotationController.planRotationStatus(keyringView, rewrapBatch, nextStates)
	if statusError != nil {
		return nil, statusError
	}
	return append(changes, statusChanges...), nil
}

// advanceToTargetKEK creates the target KEK or switches to it, after checking
// the intent from the store (target = active + 1, legacy key none or retired)
// and proving the existing target KEK unwraps (review: intent is forgeable).
func (keyringRotationController *KeyringRotationController) advanceToTargetKEK(ctx context.Context, keyringView keyringRotationView, targetVersion int) ([]Change, error) {
	if targetVersion != keyringView.activeKEKVersion+1 || !keyringView.legacyKeyState.AllowsKEKRotation() {
		logging.Default().Warn("keyring-rotation: ignoring a rotation intent the gate would refuse",
			"target_version", strconv.Itoa(targetVersion), "active", strconv.Itoa(keyringView.activeKEKVersion), "legacy", string(keyringView.legacyKeyState))
		return nil, nil
	}
	if !keyringView.wrappedKEKs[targetVersion] {
		return keyringRotationController.createTargetKEK(ctx, targetVersion)
	}
	if proveError := keyringRotationController.rotationPlanner.ProveKEKVersion(ctx, targetVersion); proveError != nil {
		return nil, fmt.Errorf("target KEK %d does not unwrap with the root key: %w", targetVersion, proveError)
	}
	return switchActiveKEK(keyringView, targetVersion), nil
}

// createTargetKEK writes keyring/kek/{target} (wrapped by the root key) and
// its pending state; the runner guards both keys as absent (create-only).
func (keyringRotationController *KeyringRotationController) createTargetKEK(ctx context.Context, targetVersion int) ([]Change, error) {
	wrappedKEK, createError := keyringRotationController.rotationPlanner.CreateWrappedKEK(ctx)
	if createError != nil {
		return nil, createError
	}
	logging.Default().Info("keyring-rotation: created KEK", "version", strconv.Itoa(targetVersion))
	return groupedChanges("keyring-rotation-create",
		Change{Type: store.OpPut, Key: types.KeyKeyringKEK(targetVersion), Value: wrappedKEK},
		Change{Type: store.OpPut, Key: types.KeyKeyringKEKState(targetVersion), Value: []byte(security.KEKVersionPending)},
	), nil
}

// switchActiveKEK moves keyring/active to the target version, marks it active
// and the replaced version draining, in one group.
func switchActiveKEK(keyringView keyringRotationView, targetVersion int) []Change {
	logging.Default().Info("keyring-rotation: switching active KEK",
		"from", strconv.Itoa(keyringView.activeKEKVersion), "to", strconv.Itoa(targetVersion))
	return groupedChanges(keyringRotationSwitchGroup,
		Change{Type: store.OpPut, Key: types.KeyKeyringActive, Value: []byte(strconv.Itoa(targetVersion))},
		Change{Type: store.OpPut, Key: types.KeyKeyringKEKState(targetVersion), Value: []byte(security.KEKVersionActive)},
		Change{Type: store.OpPut, Key: types.KeyKeyringKEKState(keyringView.activeKEKVersion), Value: []byte(security.KEKVersionDraining)},
	)
}

// planVersionTransitions retires replaced versions that no envelope names
// once every live replica seals with the active KEK, returns a retiring
// version that is named again to draining, and deletes retiring versions
// after the grace. Returns the changes and the resulting version states.
func (keyringRotationController *KeyringRotationController) planVersionTransitions(keyringView keyringRotationView, rewrapBatch security.KEKRewrapBatch, facts []store.Fact) ([]Change, map[int]security.KEKVersionState) {
	replicasNotOnKEK, replicaReportCount := security.ReplicasNotOnKEK(facts, keyringView.activeKEKVersion)
	replicasOnActiveKEK := replicaReportCount > 0 && len(replicasNotOnKEK) == 0
	currentTime := keyringRotationController.timeNow().UTC()
	nextStates := make(map[int]security.KEKVersionState, len(keyringView.kekVersionStates))
	var changes []Change
	for _, kekVersion := range sortedKEKVersions(keyringView.kekVersionStates) {
		versionState := keyringView.kekVersionStates[kekVersion]
		nextStates[kekVersion] = versionState
		if kekVersion == keyringView.activeKEKVersion {
			continue
		}
		envelopeCount := rewrapBatch.EnvelopesByKEKVersion[kekVersion]
		groupID := "keyring-rotation-version-" + strconv.Itoa(kekVersion)
		switch {
		case (versionState == security.KEKVersionDraining || versionState == security.KEKVersionActive) && envelopeCount == 0 && replicasOnActiveKEK:
			nextStates[kekVersion] = security.KEKVersionRetiring
			changes = append(changes, groupedChanges(groupID,
				Change{Type: store.OpPut, Key: types.KeyKeyringKEKState(kekVersion), Value: []byte(security.KEKVersionRetiring)},
				Change{Type: store.OpPut, Key: types.KeyKeyringKEKRetiringSince(kekVersion), Value: []byte(currentTime.Format(time.RFC3339))},
			)...)
		case versionState == security.KEKVersionRetiring && envelopeCount > 0:
			logging.Default().Warn("keyring-rotation: retiring KEK is named by envelopes again, draining it", "version", strconv.Itoa(kekVersion))
			nextStates[kekVersion] = security.KEKVersionDraining
			changes = append(changes, groupedChanges(groupID,
				Change{Type: store.OpPut, Key: types.KeyKeyringKEKState(kekVersion), Value: []byte(security.KEKVersionDraining)},
				Change{Type: store.OpDelete, Key: types.KeyKeyringKEKRetiringSince(kekVersion)},
			)...)
		case versionState == security.KEKVersionRetiring && replicasOnActiveKEK && keyringRotationController.retireGraceElapsed(keyringView.retiringSince[kekVersion], currentTime):
			logging.Default().Info("keyring-rotation: deleting retired KEK", "version", strconv.Itoa(kekVersion))
			delete(nextStates, kekVersion)
			changes = append(changes, groupedChanges(groupID,
				Change{Type: store.OpDelete, Key: types.KeyKeyringKEK(kekVersion)},
				Change{Type: store.OpDelete, Key: types.KeyKeyringKEKState(kekVersion)},
				Change{Type: store.OpDelete, Key: types.KeyKeyringKEKRetiringSince(kekVersion)},
			)...)
		}
	}
	return changes, nextStates
}

// retireGraceElapsed reports whether a retiring version became retiring at
// least the retire grace ago. An unreadable time never elapses.
func (keyringRotationController *KeyringRotationController) retireGraceElapsed(retiringSince string, currentTime time.Time) bool {
	retiringTime, parseError := time.Parse(time.RFC3339, retiringSince)
	return parseError == nil && currentTime.Sub(retiringTime) >= keyringRotationController.retireGrace
}

// planRotationStatus updates observed/keyring/rotation while a rotation runs
// and completes it (intent deleted) once nothing is left to re-wrap and no
// replaced version waits for replica reports; unreadable envelopes do not
// block completion (they are listed). Unchanged status is not written.
func (keyringRotationController *KeyringRotationController) planRotationStatus(keyringView keyringRotationView, rewrapBatch security.KEKRewrapBatch, nextStates map[int]security.KEKVersionState) ([]Change, error) {
	if keyringView.rotationIntent == nil {
		return nil, nil
	}
	nextStatus := keyringView.rotationStatus
	nextStatus.Rewrapped += len(rewrapBatch.Rewrites)
	nextStatus.Remaining = 0
	for kekVersion, envelopeCount := range rewrapBatch.EnvelopesByKEKVersion {
		if kekVersion != keyringView.activeKEKVersion {
			nextStatus.Remaining += envelopeCount
		}
	}
	nextStatus.UnreadableCount = rewrapBatch.UnreadableCount
	nextStatus.UnreadableKeys = rewrapBatch.UnreadableKeys
	var changes []Change
	if len(rewrapBatch.Rewrites) == 0 && !hasDrainingKEKVersionAwaitingReplicas(nextStates, keyringView.activeKEKVersion, rewrapBatch) {
		nextStatus.State = security.KeyringRotationCompleted
		nextStatus.Completed = keyringRotationController.timeNow().UTC().Format(time.RFC3339)
		changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyIntentKeyringRotation})
		logging.Default().Info("keyring-rotation: completed", "version", strconv.Itoa(keyringView.activeKEKVersion),
			"rewrapped", strconv.Itoa(nextStatus.Rewrapped))
		if nextStatus.Remaining > 0 {
			logging.Default().Warn("keyring-rotation: completed with envelopes that cannot be re-wrapped; their KEK versions are kept (re-set those secrets)",
				"remaining", strconv.Itoa(nextStatus.Remaining), "keys", strings.Join(nextStatus.UnreadableKeys, ","))
		}
	}
	statusJSON, marshalError := json.Marshal(nextStatus)
	if marshalError != nil {
		return nil, marshalError
	}
	if string(statusJSON) == string(keyringView.rotationStatusRaw) {
		return changes, nil
	}
	return append(changes, Change{Type: store.OpPut, Key: types.KeyObservedKeyringRotation, Value: statusJSON}), nil
}

// hasDrainingKEKVersionAwaitingReplicas reports whether a replaced version is
// still draining (or still marked active) although no envelope names it, i.e.
// it only waits for the replica reports. A version still named after a batch
// with no re-wraps is named only by envelopes that cannot be re-wrapped.
func hasDrainingKEKVersionAwaitingReplicas(kekVersionStates map[int]security.KEKVersionState, activeKEKVersion int, rewrapBatch security.KEKRewrapBatch) bool {
	for kekVersion, versionState := range kekVersionStates {
		isDraining := versionState == security.KEKVersionDraining || versionState == security.KEKVersionActive
		if kekVersion != activeKEKVersion && isDraining && rewrapBatch.EnvelopesByKEKVersion[kekVersion] == 0 {
			return true
		}
	}
	return false
}

// hasNonActiveKEKVersion reports whether the keyring holds any version other
// than the active one; without one and without an intent there is no work.
func hasNonActiveKEKVersion(keyringView keyringRotationView) bool {
	for kekVersion := range keyringView.kekVersionStates {
		if kekVersion != keyringView.activeKEKVersion {
			return true
		}
	}
	return false
}

// sortedKEKVersions returns the versions of a state map in ascending order.
func sortedKEKVersions(kekVersionStates map[int]security.KEKVersionState) []int {
	kekVersions := make([]int, 0, len(kekVersionStates))
	for kekVersion := range kekVersionStates {
		kekVersions = append(kekVersions, kekVersion)
	}
	sort.Ints(kekVersions)
	return kekVersions
}

// parseKeyringRotationView reads the keyring, intent and status from facts;
// false when there is no keyring (keyring/active absent or invalid).
func parseKeyringRotationView(facts []store.Fact) (keyringRotationView, bool) {
	keyringView := keyringRotationView{
		wrappedKEKs:      make(map[int]bool),
		kekVersionStates: make(map[int]security.KEKVersionState),
		retiringSince:    make(map[int]string),
	}
	hasActive := false
	for _, fact := range store.FactsWithPrefix(facts, types.PrefixKeyring+"/") {
		switch fact.Key {
		case types.KeyKeyringActive:
			activeVersion, parseError := strconv.Atoi(string(fact.Value))
			keyringView.activeKEKVersion, hasActive = activeVersion, parseError == nil
			if parseError != nil {
				logging.Default().Warn("keyring-rotation: keyring/active is not a version, doing nothing", "value", string(fact.Value))
			}
		case types.KeyKeyringLegacy:
			keyringView.legacyKeyState = security.LegacyKeyState(fact.Value)
		default:
			recordKEKFact(&keyringView, fact)
		}
	}
	for _, fact := range store.FactsWithPrefix(facts, types.KeyIntentKeyringRotation) {
		var rotationIntent security.KeyringRotationIntent
		if fact.Key == types.KeyIntentKeyringRotation && json.Unmarshal(fact.Value, &rotationIntent) == nil {
			keyringView.rotationIntent = &rotationIntent
		}
	}
	for _, fact := range store.FactsWithPrefix(facts, types.KeyObservedKeyringRotation) {
		if fact.Key == types.KeyObservedKeyringRotation && json.Unmarshal(fact.Value, &keyringView.rotationStatus) == nil {
			keyringView.rotationStatusRaw = fact.Value
		}
	}
	return keyringView, hasActive
}

// recordKEKFact records one keyring/kek/{v}[/state|/retiring_since] fact.
func recordKEKFact(keyringView *keyringRotationView, fact store.Fact) {
	if !strings.HasPrefix(fact.Key, types.ScanKeyringKEKs) {
		return
	}
	versionText, attribute, _ := strings.Cut(strings.TrimPrefix(fact.Key, types.ScanKeyringKEKs), "/")
	kekVersion, parseError := strconv.Atoi(versionText)
	if parseError != nil {
		logging.Default().Warn("keyring-rotation: ignoring keyring key without a numeric version", "key", fact.Key)
		return
	}
	switch attribute {
	case "":
		keyringView.wrappedKEKs[kekVersion] = true
	case "state":
		keyringView.kekVersionStates[kekVersion] = security.KEKVersionState(fact.Value)
	case "retiring_since":
		keyringView.retiringSince[kekVersion] = string(fact.Value)
	default:
		logging.Default().Warn("keyring-rotation: ignoring unknown keyring key", "key", fact.Key)
	}
}
