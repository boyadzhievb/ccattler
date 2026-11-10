// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// LegacyMigrationGateRequest carries what the operator asserts when asking
// to start the legacy key migration (Flow 29 step 5).
type LegacyMigrationGateRequest struct {
	ConfirmBackup bool `json:"confirm_backup"` // ConfirmBackup states a verified etcd snapshot exists and restore was tested.
}

// legacyMigrationIntent is the JSON stored at intent/keyring/legacy_migration.
type legacyMigrationIntent struct {
	RequestedBy string `json:"requested_by"` // RequestedBy is the principal that passed the gate.
	RequestedAt string `json:"requested_at"` // RequestedAt is the RFC 3339 time the gate passed.
}

// CheckLegacyMigrationGate returns every unmet migration gate condition
// (empty when the gate passes): backup confirmed, migration not started yet,
// every live API replica on this build with the active KEK and a passed root
// check, and every enrolled, non-disabled node on secret delivery v2.
func (keyringCodec *KeyringEnvelopeCodec) CheckLegacyMigrationGate(ctx context.Context, gateRequest LegacyMigrationGateRequest, buildVersion string) ([]string, error) {
	var unmetConditions []string
	if !gateRequest.ConfirmBackup {
		unmetConditions = append(unmetConditions, "confirm_backup not set: take and verify an etcd snapshot first (runbook in design/TESTING.md)")
	}
	migrationStatus, readError := readKeyringMigrationStatus(ctx, keyringCodec.factStore)
	if readError != nil {
		return nil, readError
	}
	if migrationStatus.State != KeyringMigrationNotStarted {
		unmetConditions = append(unmetConditions, fmt.Sprintf("migration is already %s", migrationStatus.State))
	}
	replicaConditions, replicaError := keyringCodec.checkReplicaReports(ctx, buildVersion)
	if replicaError != nil {
		return nil, replicaError
	}
	unmetConditions = append(unmetConditions, replicaConditions...)
	nodeFacts, nodeError := scanStorePrefixes(ctx, keyringCodec.factStore, EnrolledNodePrefix, types.ScanObservedNodes)
	if nodeError != nil {
		return nil, nodeError
	}
	if pendingNodes := NodesNotReporting(nodeFacts, "secret_delivery", types.SecretDeliveryVersion); len(pendingNodes) > 0 {
		unmetConditions = append(unmetConditions, "nodes not on secret delivery "+types.SecretDeliveryVersion+" (upgrade their agents): "+strings.Join(pendingNodes, ", "))
	}
	return unmetConditions, nil
}

// checkReplicaReports returns the unmet replica conditions: at least one live
// report, and every report on buildVersion with the active KEK and root_check ok.
func (keyringCodec *KeyringEnvelopeCodec) checkReplicaReports(ctx context.Context, buildVersion string) ([]string, error) {
	reportFacts, scanError := keyringCodec.factStore.Scan(ctx, types.ScanObservedKeyringReplicas)
	if scanError != nil {
		return nil, fmt.Errorf("scan %s: %w", types.ScanObservedKeyringReplicas, scanError)
	}
	if len(reportFacts) == 0 {
		return []string{"no API replica has reported its keyring yet"}, nil
	}
	activeKEKVersion := keyringCodec.currentSnapshot().activeKEKVersion
	var unmetConditions []string
	for _, reportFact := range reportFacts {
		replicaID := strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas)
		var report keyringReplicaReport
		if unmarshalError := json.Unmarshal(reportFact.Value, &report); unmarshalError != nil {
			unmetConditions = append(unmetConditions, fmt.Sprintf("replica %s: unreadable keyring report", replicaID))
			continue
		}
		if report.Version != buildVersion || report.ActiveKEKVersion != activeKEKVersion || report.RootCheck != "ok" {
			unmetConditions = append(unmetConditions, fmt.Sprintf("replica %s: version %s, KEK %d, root_check %s (want %s, KEK %d, ok)",
				replicaID, report.Version, report.ActiveKEKVersion, report.RootCheck, buildVersion, activeKEKVersion))
		}
	}
	return unmetConditions, nil
}

// StartLegacyMigration records the intent and switches the migration state to
// in_progress in one transaction guarded by the migration fact's revision, so
// two concurrent requests cannot both start it. Writes are v2 from here.
func (keyringCodec *KeyringEnvelopeCodec) StartLegacyMigration(ctx context.Context, requestedBy string) error {
	migrationFact, getError := keyringCodec.factStore.Get(ctx, types.KeyObservedKeyringMigration)
	if getError != nil {
		return fmt.Errorf("read %s: %w", types.KeyObservedKeyringMigration, getError)
	}
	startedAt := time.Now().UTC().Format(time.RFC3339)
	intentJSON, intentError := json.Marshal(legacyMigrationIntent{RequestedBy: requestedBy, RequestedAt: startedAt})
	if intentError != nil {
		return intentError
	}
	statusJSON, statusError := json.Marshal(KeyringMigrationStatus{State: KeyringMigrationInProgress, Started: startedAt})
	if statusError != nil {
		return statusError
	}
	transactionSucceeded, transactionError := keyringCodec.factStore.Transaction(ctx,
		[]store.Compare{{Key: types.KeyObservedKeyringMigration, Revision: migrationFact.Revision}},
		[]store.Op{
			{Type: store.OpPut, Key: types.KeyIntentKeyringLegacyMigration, Value: intentJSON},
			{Type: store.OpPut, Key: types.KeyObservedKeyringMigration, Value: statusJSON},
		}, nil)
	if transactionError != nil {
		return fmt.Errorf("start migration: %w", transactionError)
	}
	if !transactionSucceeded {
		return fmt.Errorf("start migration: the migration state changed concurrently; check it and retry")
	}
	return keyringCodec.Refresh(ctx)
}

// LegacyMigrationBatch is one bounded round of the legacy key migration.
type LegacyMigrationBatch struct {
	Rewrites          []store.Fact // Rewrites are v2 re-encryptions (Key, Value, Revision of the v1 fact for the CAS guard).
	RemainingV1Count  int          // RemainingV1Count counts v1 envelopes left after this batch, unreadable ones included.
	UnreadableV1Count int          // UnreadableV1Count counts v1 envelopes the legacy key cannot open.
}

// PlanLegacyMigrationBatch re-encrypts up to batchLimit v1 envelopes found in
// facts (secrets/ base64-encoded, credentials/ raw) as v2 under the active KEK,
// bound to their own store key (R8). Unreadable v1 envelopes are counted, never
// skipped silently or deleted.
func (keyringCodec *KeyringEnvelopeCodec) PlanLegacyMigrationBatch(ctx context.Context, facts []store.Fact, batchLimit int) (LegacyMigrationBatch, error) {
	var migrationBatch LegacyMigrationBatch
	snapshot := keyringCodec.currentSnapshot()
	for _, fact := range facts {
		isSecretValue := strings.HasPrefix(fact.Key, SecretStorePrefix)
		if !isSecretValue && !strings.HasPrefix(fact.Key, CredentialStorePrefix) {
			continue
		}
		envelope, isV1 := decodeStoredEnvelope(fact.Value, isSecretValue)
		if !isV1 {
			continue
		}
		migrationBatch.RemainingV1Count++
		plaintext, openError := keyringCodec.openLegacyEnvelope(ctx, envelope)
		if openError != nil {
			migrationBatch.UnreadableV1Count++
			continue
		}
		if len(migrationBatch.Rewrites) >= batchLimit {
			continue
		}
		v2Envelope, sealError := sealEnvelopeV2(snapshot.keyEncryptionKeys[snapshot.activeKEKVersion], snapshot.activeKEKVersion, fact.Key, plaintext)
		if sealError != nil {
			return LegacyMigrationBatch{}, fmt.Errorf("re-encrypt %s: %w", fact.Key, sealError)
		}
		if isSecretValue {
			v2Envelope = []byte(base64.StdEncoding.EncodeToString(v2Envelope))
		}
		migrationBatch.Rewrites = append(migrationBatch.Rewrites, store.Fact{Key: fact.Key, Value: v2Envelope, Revision: fact.Revision})
		migrationBatch.RemainingV1Count--
	}
	return migrationBatch, nil
}

// decodeStoredEnvelope returns the raw envelope of a stored value and whether
// it is v1. Secret values are base64; a value that does not decode is treated
// as an (unreadable) v1 envelope so it is counted, not ignored.
func decodeStoredEnvelope(storedValue []byte, isBase64 bool) ([]byte, bool) {
	envelope := storedValue
	if isBase64 {
		decodedEnvelope, decodeError := base64.StdEncoding.DecodeString(string(storedValue))
		if decodeError != nil {
			return storedValue, true
		}
		envelope = decodedEnvelope
	}
	return envelope, !isEnvelopeV2(envelope)
}

// scanStorePrefixes scans several prefixes into one key-sorted fact list.
func scanStorePrefixes(ctx context.Context, factStore store.StateStore, prefixes ...string) ([]store.Fact, error) {
	var allFacts []store.Fact
	for _, prefix := range prefixes {
		facts, scanError := factStore.Scan(ctx, prefix)
		if scanError != nil {
			return nil, fmt.Errorf("scan %s: %w", prefix, scanError)
		}
		allFacts = append(allFacts, facts...)
	}
	store.SortFacts(allFacts)
	return allFacts, nil
}
