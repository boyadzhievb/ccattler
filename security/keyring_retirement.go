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
// (Flow 29 step 7, R7): the legacy key is still present, the migration is
// completed with zero v1 envelopes (recounted by a full scan), no plaintext
// gate key fact exists, and every node other than the API replicas reports
// legacy_key_file = absent. Replicas keep the file until retirement.
func (keyringCodec *KeyringEnvelopeCodec) CheckLegacyRetirementGate(ctx context.Context) ([]string, error) {
	var unmetConditions []string
	if legacyKeyState := keyringCodec.currentSnapshot().legacyKeyState; legacyKeyState != LegacyKeyPresent {
		unmetConditions = append(unmetConditions, fmt.Sprintf("legacy key state is %s; there is nothing to retire", legacyKeyState))
	}
	migrationStatus, readError := readKeyringMigrationStatus(ctx, keyringCodec.factStore)
	if readError != nil {
		return nil, readError
	}
	if migrationStatus.State != KeyringMigrationCompleted || migrationStatus.LegacyV1EnvelopeCount != 0 {
		unmetConditions = append(unmetConditions, fmt.Sprintf("migration is %s with %d v1 envelopes (run cca secret migrate-keys and wait for completed)",
			migrationStatus.State, migrationStatus.LegacyV1EnvelopeCount))
	}
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
	nodeFacts, nodeError := scanStorePrefixes(ctx, keyringCodec.factStore, EnrolledNodePrefix, types.ScanObservedNodes, types.ScanObservedKeyringReplicas)
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
// with the same node ID): their server still opens v1 until retirement.
func nodesHoldingLegacyKeyFile(nodeFacts []store.Fact) []string {
	replicaNodes := make(map[string]bool)
	for _, reportFact := range store.FactsWithPrefix(nodeFacts, types.ScanObservedKeyringReplicas) {
		replicaNodes[strings.TrimPrefix(reportFact.Key, types.ScanObservedKeyringReplicas)] = true
	}
	var pendingNodes []string
	for _, nodeID := range NodesNotReporting(nodeFacts, "legacy_key_file", "absent") {
		if !replicaNodes[nodeID] {
			pendingNodes = append(pendingNodes, nodeID)
		}
	}
	return pendingNodes
}

// RetireLegacyKey sets keyring/legacy = retired in a transaction guarded by
// its revision. Every replica then drops the legacy key and refuses v1
// envelopes; the step cannot be undone.
func (keyringCodec *KeyringEnvelopeCodec) RetireLegacyKey(ctx context.Context) error {
	legacyFact, getError := keyringCodec.factStore.Get(ctx, types.KeyKeyringLegacy)
	if getError != nil {
		return fmt.Errorf("read %s: %w", types.KeyKeyringLegacy, getError)
	}
	transactionSucceeded, transactionError := keyringCodec.factStore.Transaction(ctx,
		[]store.Compare{{Key: types.KeyKeyringLegacy, Revision: legacyFact.Revision}},
		[]store.Op{{Type: store.OpPut, Key: types.KeyKeyringLegacy, Value: []byte(LegacyKeyRetired)}}, nil)
	if transactionError != nil {
		return fmt.Errorf("retire legacy key: %w", transactionError)
	}
	if !transactionSucceeded {
		return fmt.Errorf("retire legacy key: %s changed concurrently; check it and retry", types.KeyKeyringLegacy)
	}
	return keyringCodec.Refresh(ctx)
}
