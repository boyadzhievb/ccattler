// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestGateKeyMigrationController returns the controller and its provider.
func newTestGateKeyMigrationController(t *testing.T) (*GateKeyMigrationController, security.KeyProvider) {
	t.Helper()
	_, secretKeyProvider := newTestRouteTLSController(t)
	return NewGateKeyMigrationController(newTestKeyProviderSource(secretKeyProvider)), secretKeyProvider
}

// sealedGateKeyPath returns secrets/route/{gate}/tls/key.
func sealedGateKeyPath(gateName string) string {
	return security.SecretStorePrefix + types.SecretNameRouteTLSKey(gateName)
}

// decodeMigrationStatus finds the status put among changes.
func decodeMigrationStatus(t *testing.T, changes []Change) *gateKeyMigrationStatus {
	t.Helper()
	for _, change := range changes {
		if change.Key == types.KeyObservedGateKeyMigration {
			var status gateKeyMigrationStatus
			if decodeError := json.Unmarshal(change.Value, &status); decodeError != nil {
				t.Fatal(decodeError)
			}
			return &status
		}
	}
	return nil
}

// legacyGateFacts are two TLS gates with plaintext keys (auto derived, manual
// desired), an old node and a disabled old node.
func legacyGateFacts() []struct{ k, v string } {
	return []struct{ k, v string }{
		kv(types.KeyDesiredRouteTLSMode("auto-gate"), "auto"),
		kv(types.KeyDerivedRouteTLSKey("auto-gate"), "AUTO-KEY-PEM"),
		kv(types.KeyDesiredRouteTLSMode("manual-gate"), "manual"),
		kv(types.KeyDesiredRouteTLSKey("manual-gate"), "MANUAL-KEY-PEM"),
		kv(types.KeyObservedNodeState("node-old"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeState("node-off"), string(types.NodeDisabled)),
	}
}

func TestGateKeyMigrationSealsPlaintextKeysAndKeepsThemForOldAgents(t *testing.T) {
	migrationController, secretKeyProvider := newTestGateKeyMigrationController(t)
	changes, reconcileError := migrationController.Reconcile(context.Background(), buildFacts(legacyGateFacts()...))
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	sealedValues := map[string]string{}
	for _, change := range changes {
		if change.Type == store.OpDelete {
			t.Errorf("deleted %s while an old agent is present", change.Key)
		}
		if strings.HasPrefix(change.Key, security.SecretStorePrefix) {
			sealedValues[change.Key] = string(change.Value)
		}
	}
	for gateName, expectedPEM := range map[string]string{"auto-gate": "AUTO-KEY-PEM", "manual-gate": "MANUAL-KEY-PEM"} {
		sealedValue, sealed := sealedValues[sealedGateKeyPath(gateName)]
		if !sealed || strings.Contains(sealedValue, expectedPEM) {
			t.Fatalf("gate %s not sealed (or sealed in plaintext)", gateName)
		}
		if opened := openSealedTestSecret(t, secretKeyProvider, sealedValue); string(opened) != expectedPEM {
			t.Errorf("gate %s sealed %q", gateName, opened)
		}
	}
	if status := decodeMigrationStatus(t, changes); status == nil || status.State != GateKeyMigrationSealing || status.PlaintextCount != 2 {
		t.Errorf("status = %+v", status)
	}
}

func TestGateKeyMigrationWaitsForEveryEnabledNodeThenDeletesPlaintext(t *testing.T) {
	migrationController, _ := newTestGateKeyMigrationController(t)
	ctx := context.Background()
	sealedFacts := append(legacyGateFacts(),
		kv(sealedGateKeyPath("auto-gate"), "sealed"),
		kv(sealedGateKeyPath("manual-gate"), "sealed"),
		kv(security.EnrolledNodePrefix+"node-enrolled-only", "{}"),
		kv(types.KeyDesiredRouteTLSKey("plain-gate"), "ORPHAN-KEY"), // gate without tls
	)

	changes, _ := migrationController.Reconcile(ctx, buildFacts(sealedFacts...))
	status := decodeMigrationStatus(t, changes)
	if len(changes) != 1 || status == nil || status.State != GateKeyMigrationWaitingForAgents ||
		strings.Join(status.PendingNodes, ",") != "node-enrolled-only,node-old" || status.PlaintextCount != 3 {
		t.Fatalf("changes = %d, status = %+v", len(changes), status)
	}
	// Same status stored: nothing to write.
	storedStatus, _ := json.Marshal(status)
	steadyChanges, _ := migrationController.Reconcile(ctx, buildFacts(append(append([]struct{ k, v string }{}, sealedFacts...), kv(types.KeyObservedGateKeyMigration, string(storedStatus)))...))
	if len(steadyChanges) != 0 {
		t.Errorf("steady state wrote %d changes", len(steadyChanges))
	}

	upgradedFacts := append(append([]struct{ k, v string }{}, sealedFacts...),
		kv(types.KeyObservedNodeGateKeyDelivery("node-old"), types.GateKeyDeliveryVersion),
		kv(types.KeyObservedNodeGateKeyDelivery("node-enrolled-only"), types.GateKeyDeliveryVersion),
	)
	changes, _ = migrationController.Reconcile(ctx, buildFacts(upgradedFacts...))
	var deletedKeys []string
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deletedKeys = append(deletedKeys, change.Key)
		}
	}
	expectedDeletions := []string{types.KeyDerivedRouteTLSKey("auto-gate"), types.KeyDesiredRouteTLSKey("manual-gate"), types.KeyDesiredRouteTLSKey("plain-gate")}
	if strings.Join(deletedKeys, ",") != strings.Join(expectedDeletions, ",") {
		t.Errorf("deleted %v, want %v", deletedKeys, expectedDeletions)
	}
	if status := decodeMigrationStatus(t, changes); status == nil || status.State != GateKeyMigrationCompleted || status.PlaintextCount != 0 {
		t.Errorf("status = %+v", status)
	}
}

func TestGateKeyMigrationThroughRunnerCompletesWithUpgradedNodes(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	migrationController, _ := newTestGateKeyMigrationController(t)
	runner := NewRunner(factStore, migrationController)
	runner.SetDebounce(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("auto-gate"), []byte("auto"))
	factStore.Put(ctx, types.KeyDerivedRouteTLSKey("auto-gate"), []byte("AUTO-KEY-PEM"))
	factStore.Put(ctx, types.KeyObservedNodeState("node-1"), []byte(types.NodeAlive))
	factStore.Put(ctx, types.KeyObservedNodeGateKeyDelivery("node-1"), []byte(types.GateKeyDeliveryVersion))
	go runner.Run(ctx)

	waitForStoreValue(t, factStore, types.KeyObservedGateKeyMigration, func(value string) bool {
		return strings.Contains(value, string(GateKeyMigrationCompleted))
	})
	if _, getError := factStore.Get(ctx, types.KeyDerivedRouteTLSKey("auto-gate")); getError == nil {
		t.Error("plaintext key still present after completion")
	}
	if _, getError := factStore.Get(ctx, sealedGateKeyPath("auto-gate")); getError != nil {
		t.Errorf("sealed key missing: %v", getError)
	}
}

func TestGateKeyMigrationNeverSealsKeysOfKeySecretGates(t *testing.T) {
	migrationController, _ := newTestGateKeyMigrationController(t)
	facts := buildFacts(
		kv(types.KeyDesiredRouteTLSMode("secret-gate"), "manual"),
		kv(types.KeyDesiredRouteTLSKeySecret("secret-gate"), "platform/key"),
		kv(types.KeyDesiredRouteTLSKey("secret-gate"), "LEFTOVER-PEM"),
		kv(types.KeyObservedNodeState("node-1"), string(types.NodeAlive)),
		kv(types.KeyObservedNodeGateKeyDelivery("node-1"), types.GateKeyDeliveryVersion),
	)
	changes, _ := migrationController.Reconcile(context.Background(), facts)
	for _, change := range changes {
		if change.Key == sealedGateKeyPath("secret-gate") {
			t.Fatal("sealed a system key for a gate that uses a key secret (route-tls would delete it again)")
		}
	}
	deleted := false
	for _, change := range changes {
		deleted = deleted || (change.Type == store.OpDelete && change.Key == types.KeyDesiredRouteTLSKey("secret-gate"))
	}
	if !deleted {
		t.Error("leftover plaintext key of a key-secret gate not deleted once all nodes are upgraded")
	}
}
