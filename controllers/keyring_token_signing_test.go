// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// tokenSigningControllerTestIssuer is the issuer the test clusters store.
const tokenSigningControllerTestIssuer = "https://ctrl-1:9770" //nolint:gosec // G101: an issuer URL, not a credential

// tokenSigningControllerTestCycles bounds the reconcile cycles a test runs
// before it gives up waiting for a state.
const tokenSigningControllerTestCycles = 20

// loadTestTokenSigningIdentity loads (or creates) the signing identity with
// keyringCodec, as cca server does at start.
func loadTestTokenSigningIdentity(t *testing.T, factStore store.StateStore, keyringCodec security.SecretEnvelopeCodec) *security.TokenSigningIdentity {
	t.Helper()
	identity, loadError := security.LoadOrCreateTokenSigningIdentity(context.Background(), factStore, keyringCodec, "", tokenSigningControllerTestIssuer)
	if loadError != nil {
		t.Fatal(loadError)
	}
	return identity
}

// storedTokenSigningEnvelope returns the raw envelope of the signing key kid.
func storedTokenSigningEnvelope(t *testing.T, factStore store.StateStore, keyID string) []byte {
	t.Helper()
	keyFact, getError := factStore.Get(context.Background(), types.KeyTokenSigningKey(keyID))
	if getError != nil {
		t.Fatal(getError)
	}
	return keyFact.Value
}

// assertRestartedReplicaAcceptsToken checks a fresh replica (new codec,
// identity loaded from the store, no legacy key) signs with keyID and
// accepts alice's earlier token.
func assertRestartedReplicaAcceptsToken(t *testing.T, factStore store.StateStore, rootKeyProvider security.KeyProvider, keyID string, token string) {
	t.Helper()
	restartedCodec, codecError := security.NewKeyringEnvelopeCodec(context.Background(), factStore, rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	restartedIdentity := loadTestTokenSigningIdentity(t, factStore, restartedCodec)
	if restartedIdentity.ActiveKeyID() != keyID {
		t.Fatalf("after the restart the replica signs with %s, want %s", restartedIdentity.ActiveKeyID(), keyID)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	result, authError := restartedIdentity.BuildInternalTokenAuthenticator(factStore).Authenticate(request)
	if authError != nil || result == nil || result.Principal != "user:alice" {
		t.Fatalf("restarted replica on the earlier token: %+v, %v", result, authError)
	}
}

// mintTestAliceToken writes user alice and mints her a token.
func mintTestAliceToken(t *testing.T, factStore store.StateStore, identity *security.TokenSigningIdentity) string {
	t.Helper()
	userRevision, putError := factStore.Put(context.Background(), types.KeyAuthUser("alice"), []byte(`{}`))
	if putError != nil {
		t.Fatal(putError)
	}
	token, mintError := identity.BuildUserTokenIssuer().MintUserToken("alice", "", nil, time.Hour, userRevision)
	if mintError != nil {
		t.Fatal(mintError)
	}
	return token
}

// TestKEKRotationRewrapsTokenSigningKey: after cca secret rotate completes
// and KEK 1 is deleted past the grace, the signing key is sealed with KEK 2,
// a restarted replica loads the same kid and accepts an earlier token.
func TestKEKRotationRewrapsTokenSigningKey(t *testing.T) {
	cluster := newRotationTestCluster(t, 3)
	identity := loadTestTokenSigningIdentity(t, cluster.factStore, cluster.keyringCodec)
	token := mintTestAliceToken(t, cluster.factStore, identity)
	controller := NewKeyringRotationController(cluster.keyringCodec, time.Hour)

	cluster.completeTestRotation(t, controller)
	retiringSince, _ := time.Parse(time.RFC3339, cluster.storeValue(types.KeyKeyringKEKRetiringSince(1)))
	controller.timeNow = func() time.Time { return retiringSince.Add(2 * time.Hour) }
	cluster.runRotationCycle(t, controller)
	if cluster.storeValue(types.KeyKeyringKEK(1)) != "" {
		t.Fatal("KEK 1 still present after the grace")
	}
	if storedVersion := envelopeKEKVersion(storedTokenSigningEnvelope(t, cluster.factStore, identity.ActiveKeyID())); storedVersion != 2 {
		t.Fatalf("signing key sealed with KEK %d after the rotation, want 2", storedVersion)
	}
	assertRestartedReplicaAcceptsToken(t, cluster.factStore, cluster.rootKeyProvider, identity.ActiveKeyID(), token)
}

// TestKEKRetirementWaitsForTokenSigningKey: the rotation counts the signing
// key among the envelopes naming KEK 1, so KEK 1 never becomes retiring
// while the signing key is still sealed with it.
func TestKEKRetirementWaitsForTokenSigningKey(t *testing.T) {
	cluster := newRotationTestCluster(t, 0)
	identity := loadTestTokenSigningIdentity(t, cluster.factStore, cluster.keyringCodec)
	controller := NewKeyringRotationController(cluster.keyringCodec, DefaultKeyringRetireGrace)
	if _, startError := cluster.keyringCodec.StartKeyringRotation(context.Background(), "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	for cycle := 0; cycle < tokenSigningControllerTestCycles; cycle++ {
		cluster.runRotationCycle(t, controller)
		cluster.publishReplicaReport(t)
		signingKeyVersion := envelopeKEKVersion(storedTokenSigningEnvelope(t, cluster.factStore, identity.ActiveKeyID()))
		if kekOneState := cluster.storeValue(types.KeyKeyringKEKState(1)); kekOneState == string(security.KEKVersionRetiring) {
			if signingKeyVersion != 2 {
				t.Fatalf("KEK 1 retiring while the signing key is still sealed with KEK %d", signingKeyVersion)
			}
			return
		}
	}
	t.Fatalf("KEK 1 never became retiring: %+v", cluster.readRotationStatus(t))
}

// TestLegacyMigrationMovesTokenSigningKeyToV2: on an upgraded cluster the
// signing key is created as a v1 envelope; the migration controller, run by
// the real runner, moves it to v2, and after the legacy key is retired a
// restarted replica still loads it and accepts an earlier token.
func TestLegacyMigrationMovesTokenSigningKeyToV2(t *testing.T) {
	ctx := context.Background()
	factStore, keyringCodec, rootKeyProvider, _ := newUpgradedKeyringCluster(t, 2)
	identity := loadTestTokenSigningIdentity(t, factStore, keyringCodec)
	if envelope := storedTokenSigningEnvelope(t, factStore, identity.ActiveKeyID()); len(envelope) == 0 || envelope[0] != 0 {
		t.Fatal("signing key on a cluster with legacy present is not a v1 envelope")
	}
	token := mintTestAliceToken(t, factStore, identity)
	if startError := keyringCodec.StartLegacyMigration(ctx, "user:admin"); startError != nil {
		t.Fatal(startError)
	}
	migrationController := NewKeyringMigrationController(keyringCodec)
	for cycle := 0; cycle < tokenSigningControllerTestCycles && readMigrationStatus(t, factStore).State != security.KeyringMigrationCompleted; cycle++ {
		if cycleError := NewRunner(factStore, migrationController).executeReconciliationCycle(ctx, migrationController, nil); cycleError != nil {
			t.Fatal(cycleError)
		}
	}
	if envelope := storedTokenSigningEnvelope(t, factStore, identity.ActiveKeyID()); len(envelope) == 0 || envelope[0] == 0 {
		t.Fatalf("signing key still v1 after the migration: %+v", readMigrationStatus(t, factStore))
	}
	if retireError := keyringCodec.RetireLegacyKey(ctx); retireError != nil {
		t.Fatal(retireError)
	}
	assertRestartedReplicaAcceptsToken(t, factStore, rootKeyProvider, identity.ActiveKeyID(), token)
}

// TestKeyringOperationsCoverTokenSigningKey: the KEK-rotation and
// legacy-migration controllers both watch keyring/token-signing/key/ and may
// write it, and neither may write the marker or the active kid (D4).
func TestKeyringOperationsCoverTokenSigningKey(t *testing.T) {
	signingKeyStoreKey := types.KeyTokenSigningKey("test-kid")
	keyringOperations := map[string]Controller{
		"keyring-rotation":  NewKeyringRotationController(nil, DefaultKeyringRetireGrace),
		"keyring-migration": NewKeyringMigrationController(nil),
	}
	for controllerName, controller := range keyringOperations {
		if controller.Name() != controllerName {
			t.Fatalf("controller %s is named %s", controllerName, controller.Name())
		}
		watchesSigningKey := false
		for _, watchedPrefix := range controller.Watch() {
			watchesSigningKey = watchesSigningKey || strings.HasPrefix(signingKeyStoreKey, watchedPrefix)
		}
		if !watchesSigningKey {
			t.Errorf("%s does not watch %s: %v", controllerName, signingKeyStoreKey, controller.Watch())
		}
		proposedChanges := []Change{
			{Type: store.OpPut, Key: signingKeyStoreKey, Value: []byte("re-wrapped")},
			{Type: store.OpPut, Key: types.KeyTokenSigningInitialized, Value: []byte("{}")},
			{Type: store.OpPut, Key: types.KeyTokenSigningActive, Value: []byte("other-kid")},
		}
		allowedChanges, enforceError := enforceWriteDomain(controllerName, proposedChanges)
		if enforceError != nil {
			t.Fatal(enforceError)
		}
		if len(allowedChanges) != 1 || allowedChanges[0].Key != signingKeyStoreKey {
			t.Errorf("%s may write %v; want only %s", controllerName, allowedChanges, signingKeyStoreKey)
		}
	}
}
