// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// keyringMigrationBatchSize bounds the v1 envelopes re-encrypted per cycle;
// with the status put it stays inside the runner's transaction budget.
const keyringMigrationBatchSize = 50

// LegacyEnvelopeMigrationPlanner plans one bounded batch of the legacy key
// migration (implemented by security.KeyringEnvelopeCodec).
type LegacyEnvelopeMigrationPlanner interface {
	// PlanLegacyMigrationBatch re-encrypts up to batchLimit v1 envelopes as v2.
	PlanLegacyMigrationBatch(ctx context.Context, facts []store.Fact, batchLimit int) (security.LegacyMigrationBatch, error)
}

// KeyringMigrationController re-encrypts v1 envelopes under secrets/ and
// credentials/ as v2 after the migration gate (Flow 29 step 6), in bounded
// batches guarded by each envelope's revision, and persists the progress.
// It resumes from the remaining v1 envelopes after a crash.
type KeyringMigrationController struct {
	migrationPlanner LegacyEnvelopeMigrationPlanner // migrationPlanner opens v1 and seals v2 envelopes.
}

// NewKeyringMigrationController creates the controller around the server's
// keyring envelope codec.
func NewKeyringMigrationController(migrationPlanner LegacyEnvelopeMigrationPlanner) *KeyringMigrationController {
	return &KeyringMigrationController{migrationPlanner: migrationPlanner}
}

// Name returns "keyring-migration", identifying this controller in logs.
func (keyringMigrationController *KeyringMigrationController) Name() string {
	return "keyring-migration"
}

// Watch returns the envelope areas, the authoritative keyring state and the
// migration status.
func (keyringMigrationController *KeyringMigrationController) Watch() []string {
	return []string{
		security.SecretStorePrefix,
		security.CredentialStorePrefix,
		types.KeyKeyringLegacy,
		types.KeyObservedKeyringMigration,
	}
}

// Reconcile does nothing unless keyring/legacy is migrating. Then it rewrites
// one batch of v1 envelopes (each put guarded by the runner with the
// envelope's scanned revision) and updates observed/keyring/migration: zero
// v1 left is completed; a v1 envelope written late re-opens it (in_progress).
func (keyringMigrationController *KeyringMigrationController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	migrationStatus, hasStatus := parseKeyringMigrationStatus(facts)
	if !hasStatus || !isKeyringMigrating(facts) {
		return nil, nil
	}
	migrationBatch, planError := keyringMigrationController.migrationPlanner.PlanLegacyMigrationBatch(ctx, facts, keyringMigrationBatchSize)
	if planError != nil {
		return nil, planError
	}
	changes := make([]Change, 0, len(migrationBatch.Rewrites)+1)
	for _, rewrite := range migrationBatch.Rewrites {
		changes = append(changes, Change{Type: store.OpPut, Key: rewrite.Key, Value: rewrite.Value})
	}
	nextStatus := migrationStatus
	nextStatus.Migrated += len(migrationBatch.Rewrites)
	nextStatus.LegacyV1EnvelopeCount = migrationBatch.RemainingV1Count
	nextStatus.UnreadableCount = migrationBatch.UnreadableV1Count
	switch {
	case migrationBatch.RemainingV1Count == 0 && migrationStatus.State != security.KeyringMigrationCompleted:
		nextStatus.State = security.KeyringMigrationCompleted
		nextStatus.Completed = time.Now().UTC().Format(time.RFC3339)
		logging.Default().Info("keyring-migration: completed", "migrated", fmt.Sprintf("%d", nextStatus.Migrated))
	case migrationBatch.RemainingV1Count > 0 && migrationStatus.State == security.KeyringMigrationCompleted:
		nextStatus.State = security.KeyringMigrationInProgress
		nextStatus.Completed = ""
		logging.Default().Warn("keyring-migration: v1 envelopes appeared after completion, re-encrypting them",
			"remaining", fmt.Sprintf("%d", migrationBatch.RemainingV1Count))
	}
	if nextStatus == migrationStatus {
		return changes, nil
	}
	statusJSON, marshalError := json.Marshal(nextStatus)
	if marshalError != nil {
		return nil, marshalError
	}
	return append(changes, Change{Type: store.OpPut, Key: types.KeyObservedKeyringMigration, Value: statusJSON}), nil
}

// isKeyringMigrating reports whether keyring/legacy is migrating in facts.
func isKeyringMigrating(facts []store.Fact) bool {
	for _, fact := range store.FactsWithPrefix(facts, types.KeyKeyringLegacy) {
		if fact.Key == types.KeyKeyringLegacy {
			return security.LegacyKeyState(fact.Value) == security.LegacyKeyMigrating
		}
	}
	return false
}

// parseKeyringMigrationStatus reads observed/keyring/migration from facts.
func parseKeyringMigrationStatus(facts []store.Fact) (security.KeyringMigrationStatus, bool) {
	var migrationStatus security.KeyringMigrationStatus
	for _, fact := range store.FactsWithPrefix(facts, types.KeyObservedKeyringMigration) {
		if fact.Key == types.KeyObservedKeyringMigration && json.Unmarshal(fact.Value, &migrationStatus) == nil {
			return migrationStatus, true
		}
	}
	return migrationStatus, false
}
