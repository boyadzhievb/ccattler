// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// plaintextGateKeySuffix ends the pre-M176 plaintext gate key facts
// (desired|derived/route/{gate}/tls/key) that must be gone before retirement.
const plaintextGateKeySuffix = "/tls/key"

// CheckLegacyRetirementGate returns every unmet retirement gate condition
// (Flow 29 step 7, R7): keyring/legacy is migrating, every live replica acts
// on it (seals v2), the migration is completed with zero v1 envelopes
// (recounted by a full scan), no plaintext gate key fact exists, and every
// node other than the API replicas reports legacy_key_file = absent.
func (keyringCodec *KeyringEnvelopeCodec) CheckLegacyRetirementGate(ctx context.Context) ([]string, error) {
	var unmetConditions []string
	if legacyKeyState := keyringCodec.currentSnapshot().legacyKeyState; legacyKeyState != LegacyKeyMigrating {
		unmetConditions = append(unmetConditions, fmt.Sprintf("keyring is %s; retirement needs a completed migration (state migrating)", legacyKeyState))
	}
	migrationStatus, readError := readKeyringMigrationStatus(ctx, keyringCodec.factStore)
	if readError != nil {
		return nil, readError
	}
	if migrationStatus.State != KeyringMigrationCompleted || migrationStatus.LegacyV1EnvelopeCount != 0 {
		unmetConditions = append(unmetConditions, fmt.Sprintf("migration is %s with %d v1 envelopes (%d unreadable); the keyring controller re-encrypts v1 envelopes, also ones written late — wait for completed",
			migrationStatus.State, migrationStatus.LegacyV1EnvelopeCount, migrationStatus.UnreadableCount))
	}
	replicaConditions, replicaError := keyringCodec.checkReplicaReports(ctx, "", LegacyKeyMigrating)
	if replicaError != nil {
		return nil, replicaError
	}
	unmetConditions = append(unmetConditions, replicaConditions...)
	storeConditions, storeError := keyringCodec.checkRetirementStoreConditions(ctx)
	if storeError != nil {
		return nil, storeError
	}
	return append(unmetConditions, storeConditions...), nil
}

// checkRetirementStoreConditions recounts v1 envelopes by a full scan, looks
// for plaintext gate key facts and for nodes that still hold the legacy file.
func (keyringCodec *KeyringEnvelopeCodec) checkRetirementStoreConditions(ctx context.Context) ([]string, error) {
	var unmetConditions []string
	envelopeFacts, scanError := scanStorePrefixes(ctx, keyringCodec.factStore, SecretStorePrefix, CredentialStorePrefix)
	if scanError != nil {
		return nil, scanError
	}
	if v1EnvelopeCount := countV1Envelopes(envelopeFacts); v1EnvelopeCount > 0 {
		unmetConditions = append(unmetConditions, fmt.Sprintf("full scan found %d v1 envelopes", v1EnvelopeCount))
	}
	routeFacts, routeError := scanStorePrefixes(ctx, keyringCodec.factStore, types.ScanDesiredRoutes, types.ScanDerivedRoutes)
	if routeError != nil {
		return nil, routeError
	}
	for _, routeFact := range routeFacts {
		if strings.HasSuffix(routeFact.Key, plaintextGateKeySuffix) {
			unmetConditions = append(unmetConditions, "plaintext gate key fact still exists: "+routeFact.Key)
		}
	}
	nodeFacts, nodeError := scanStorePrefixes(ctx, keyringCodec.factStore, types.ScanObservedNodes, types.ScanObservedKeyringReplicas)
	if nodeError != nil {
		return nil, nodeError
	}
	if pendingNodes := nodesHoldingLegacyKeyFile(nodeFacts); len(pendingNodes) > 0 {
		unmetConditions = append(unmetConditions, "nodes still report legacy_key_file present (remove /etc/ccattler/secret-master.key, e.g. with the worker Ansible role): "+strings.Join(pendingNodes, ", "))
	}
	return unmetConditions, nil
}

// countV1Envelopes counts v1 envelopes among secret and credential facts.
func countV1Envelopes(envelopeFacts []store.Fact) int {
	v1EnvelopeCount := 0
	for _, envelopeFact := range envelopeFacts {
		if _, isV1 := decodeStoredEnvelope(envelopeFact.Value, strings.HasPrefix(envelopeFact.Key, SecretStorePrefix)); isV1 {
			v1EnvelopeCount++
		}
	}
	return v1EnvelopeCount
}

// nodesHoldingLegacyKeyFile returns the nodes that do not report
// legacy_key_file = absent, excluding API replicas (a live keyring report
// with the same node ID — server --node-id must equal the agent's node ID):
// their server still opens v1 until retirement.
func nodesHoldingLegacyKeyFile(nodeFacts []store.Fact) []string {
	replicaNodes := make(map[string]bool)
	for _, reportFact := range store.FactsWithPrefix(nodeFacts, types.ScanObservedKeyringReplicas) {
		replicaNodes[strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas)] = true
	}
	var pendingNodes []string
	for _, nodeID := range NodesNotReporting(nodeFacts, types.NodeReportLegacyKeyFile, types.LegacyKeyFileAbsent) {
		if !replicaNodes[nodeID] {
			pendingNodes = append(pendingNodes, nodeID)
		}
	}
	return pendingNodes
}

// RetireLegacyKey moves keyring/legacy from migrating to retired, guarded by
// the revisions of keyring/legacy and observed/keyring/migration read now
// (a v1 write that reopened the migration fails it). Every replica then drops
// the legacy key and refuses v1 envelopes; the step cannot be undone.
func (keyringCodec *KeyringEnvelopeCodec) RetireLegacyKey(ctx context.Context) error {
	return keyringCodec.advanceLegacyKeyState(ctx, LegacyKeyMigrating, LegacyKeyRetired)
}
