// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// GateKeyMigrationState is the state field of observed/gate-keys/migration.
type GateKeyMigrationState string

const (
	// GateKeyMigrationSealing means plaintext keys exist without a sealed copy.
	GateKeyMigrationSealing GateKeyMigrationState = "sealing"
	// GateKeyMigrationWaitingForAgents means every key is sealed but some node
	// does not report gate_key_delivery = v1, so plaintext keys stay.
	GateKeyMigrationWaitingForAgents GateKeyMigrationState = "waiting-for-agents"
	// GateKeyMigrationCompleted means no plaintext gate key fact remains.
	GateKeyMigrationCompleted GateKeyMigrationState = "completed"
)

// gateKeyMigrationStatus is the JSON value of observed/gate-keys/migration.
type gateKeyMigrationStatus struct {
	State          GateKeyMigrationState `json:"state"`                   // State is sealing, waiting-for-agents or completed.
	PlaintextCount int                   `json:"plaintext_count"`         // PlaintextCount is the number of plaintext key facts left.
	PendingNodes   []string              `json:"pending_nodes,omitempty"` // PendingNodes lists nodes not reporting gate_key_delivery = v1.
}

// GateKeyMigrationController performs the one-time cutover of plaintext gate
// TLS keys (Flow 28 step 7): it seals every desired|derived/route/{gate}/tls/key
// fact that has no sealed copy, and deletes the plaintext facts only when every
// non-disabled node reports gate_key_delivery = v1, so old agents keep TLS.
type GateKeyMigrationController struct {
	secretKeyProvider security.KeyProvider // secretKeyProvider seals plaintext keys.
}

// NewGateKeyMigrationController creates the migration controller with the
// server's secret key provider.
func NewGateKeyMigrationController(secretKeyProvider security.KeyProvider) *GateKeyMigrationController {
	return &GateKeyMigrationController{secretKeyProvider: secretKeyProvider}
}

// Name returns "gate-key-migration", identifying this controller in logs.
func (gateKeyMigrationController *GateKeyMigrationController) Name() string {
	return "gate-key-migration"
}

// Watch returns the gate facts, sealed keys, node facts, enrollment records
// and the migration status the controller reads.
func (gateKeyMigrationController *GateKeyMigrationController) Watch() []string {
	return []string{
		types.ScanDesiredRoutes,
		types.ScanDerivedRoutes,
		security.SecretStorePrefix + types.SecretNameRouteTLSKeyPrefix,
		types.ScanObservedNodes,
		security.EnrolledNodePrefix,
		types.KeyObservedGateKeyMigration,
	}
}

// Reconcile seals plaintext keys that have no sealed copy, deletes plaintext
// keys once every node is upgraded and the key is sealed (or its gate has no
// tls), and publishes the migration status when it changed.
func (gateKeyMigrationController *GateKeyMigrationController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	plaintextKeys := collectPlaintextGateKeys(facts)
	tlsGates := collectTLSGateNames(facts)
	sealedGates := collectSealedGateKeyNames(facts)
	pendingNodes := collectNodesWithoutGateKeyDelivery(facts)

	var changes []Change
	sealedNow := make(map[string]bool)
	for _, gateName := range sortedPlaintextGateNames(plaintextKeys) {
		if sealedGates[gateName] || !tlsGates[gateName] {
			continue
		}
		sealedKey, sealError := security.SealSecretValue(ctx, gateKeyMigrationController.secretKeyProvider, []byte(plaintextKeys[gateName].privateKeyPEM))
		if sealError != nil {
			return nil, fmt.Errorf("seal plaintext key of gate %s: %w", gateName, sealError)
		}
		logging.Default().Info("gate-key-migration: sealed plaintext gate key", "gate", gateName)
		changes = append(changes, Change{Type: store.OpPut, Key: security.SecretStorePrefix + types.SecretNameRouteTLSKey(gateName), Value: sealedKey})
		sealedNow[gateName] = true
	}

	remainingPlaintextCount := 0
	for _, gateName := range sortedPlaintextGateNames(plaintextKeys) {
		deletable := len(pendingNodes) == 0 && !sealedNow[gateName] && (sealedGates[gateName] || !tlsGates[gateName])
		if !deletable {
			remainingPlaintextCount += len(plaintextKeys[gateName].factKeys)
			continue
		}
		for _, plaintextFactKey := range plaintextKeys[gateName].factKeys {
			logging.Default().Info("gate-key-migration: deleting plaintext gate key", "key", plaintextFactKey)
			changes = append(changes, Change{Type: store.OpDelete, Key: plaintextFactKey})
		}
	}

	status := gateKeyMigrationStatus{State: GateKeyMigrationCompleted, PlaintextCount: remainingPlaintextCount}
	switch {
	case len(sealedNow) > 0:
		status.State = GateKeyMigrationSealing
	case remainingPlaintextCount > 0:
		status.State, status.PendingNodes = GateKeyMigrationWaitingForAgents, pendingNodes
	}
	return append(changes, migrationStatusChange(facts, status)...), nil
}

// plaintextGateKey holds a gate's plaintext key facts and the PEM to seal.
type plaintextGateKey struct {
	privateKeyPEM string   // privateKeyPEM is the desired key if present, else the derived one.
	factKeys      []string // factKeys are the plaintext fact keys (desired and/or derived), sorted.
}

// collectPlaintextGateKeys groups desired|derived/route/{gate}/tls/key facts
// by gate. A desired (manual) key wins over a derived one, as in the old proxy.
func collectPlaintextGateKeys(facts []store.Fact) map[string]*plaintextGateKey {
	plaintextKeys := make(map[string]*plaintextGateKey)
	for _, scanPrefix := range []string{types.ScanDerivedRoutes, types.ScanDesiredRoutes} {
		for _, fact := range store.FactsWithPrefix(facts, scanPrefix) {
			gateName, isKeyFact := strings.CutSuffix(strings.TrimPrefix(fact.Key, scanPrefix), routeTLSSealedKeySuffix)
			if !isKeyFact || gateName == "" || strings.Contains(gateName, "/") {
				continue
			}
			if plaintextKeys[gateName] == nil {
				plaintextKeys[gateName] = &plaintextGateKey{}
			}
			plaintextKeys[gateName].privateKeyPEM = string(fact.Value) // desired is scanned last and wins
			plaintextKeys[gateName].factKeys = append(plaintextKeys[gateName].factKeys, fact.Key)
		}
	}
	for _, plaintextKey := range plaintextKeys {
		sort.Strings(plaintextKey.factKeys)
	}
	return plaintextKeys
}

// collectTLSGateNames returns the gates that declare tls (desired tls/mode).
func collectTLSGateNames(facts []store.Fact) map[string]bool {
	tlsGates := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredRoutes) {
		if gateName, isModeFact := strings.CutSuffix(strings.TrimPrefix(fact.Key, types.ScanDesiredRoutes), "/tls/mode"); isModeFact && len(fact.Value) > 0 {
			tlsGates[gateName] = true
		}
	}
	return tlsGates
}

// collectSealedGateKeyNames returns the gates with a sealed system key.
func collectSealedGateKeyNames(facts []store.Fact) map[string]bool {
	sealedKeyPrefix := security.SecretStorePrefix + types.SecretNameRouteTLSKeyPrefix
	sealedGates := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, sealedKeyPrefix) {
		if gateName, isSealedKey := strings.CutSuffix(strings.TrimPrefix(fact.Key, sealedKeyPrefix), routeTLSSealedKeySuffix); isSealedKey {
			sealedGates[gateName] = true
		}
	}
	return sealedGates
}

// collectNodesWithoutGateKeyDelivery returns the sorted nodes (enrolled or
// registered, not disabled) that do not report gate_key_delivery = v1. Counting
// registered nodes too keeps clusters without enrollment from deleting early.
func collectNodesWithoutGateKeyDelivery(facts []store.Fact) []string {
	knownNodes := make(map[string]bool)
	disabledNodes := make(map[string]bool)
	upgradedNodes := make(map[string]bool)
	for _, fact := range store.FactsWithPrefix(facts, security.EnrolledNodePrefix) {
		if nodeID := strings.TrimPrefix(fact.Key, security.EnrolledNodePrefix); nodeID != "" && !strings.Contains(nodeID, "/") {
			knownNodes[nodeID] = true
		}
	}
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		nodeID, attributePath, hasAttribute := strings.Cut(strings.TrimPrefix(fact.Key, types.ScanObservedNodes), "/")
		switch {
		case !hasAttribute:
		case attributePath == "state":
			knownNodes[nodeID] = true
			disabledNodes[nodeID] = types.NodeState(fact.Value) == types.NodeDisabled
		case attributePath == "gate_key_delivery":
			upgradedNodes[nodeID] = string(fact.Value) == types.GateKeyDeliveryVersion
		}
	}
	pendingNodes := make([]string, 0)
	for nodeID := range knownNodes {
		if !disabledNodes[nodeID] && !upgradedNodes[nodeID] {
			pendingNodes = append(pendingNodes, nodeID)
		}
	}
	sort.Strings(pendingNodes)
	return pendingNodes
}

// migrationStatusChange returns a put of observed/gate-keys/migration when the
// status differs from the stored one, so a steady state writes nothing.
func migrationStatusChange(facts []store.Fact, status gateKeyMigrationStatus) []Change {
	encodedStatus, _ := json.Marshal(status)
	for _, fact := range store.FactsWithPrefix(facts, types.KeyObservedGateKeyMigration) {
		if fact.Key == types.KeyObservedGateKeyMigration && string(fact.Value) == string(encodedStatus) {
			return nil
		}
	}
	return []Change{{Type: store.OpPut, Key: types.KeyObservedGateKeyMigration, Value: encodedStatus}}
}

// sortedPlaintextGateNames returns the gate names of a plaintext key map, sorted.
func sortedPlaintextGateNames(plaintextKeys map[string]*plaintextGateKey) []string {
	gateNames := make([]string, 0, len(plaintextKeys))
	for gateName := range plaintextKeys {
		gateNames = append(gateNames, gateName)
	}
	sort.Strings(gateNames)
	return gateNames
}
