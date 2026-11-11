// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// TestSecretRotateGateAndStart verifies POST /api/secret/rotate needs
// secret.write at cluster, answers 409 with the unmet conditions while the
// legacy key is present, and starts a rotation on a fresh cluster (200,
// target version 2) exactly once.
func TestSecretRotateGateAndStart(testContext *testing.T) {
	ctx := context.Background()
	newRotateServer := func(legacyKey []byte) *Server {
		rawStore := store.NewMemoryStore()
		var legacyKeyProvider security.KeyProvider
		if legacyKey != nil {
			legacyStore, _ := security.NewSecretStoreWithMasterKey(rawStore, legacyKey)
			_ = legacyStore.PutSecret(ctx, "db.password", []byte("s3cret"))
			legacyKeyProvider, _ = security.NewLocalKeyProvider(legacyKey)
		}
		rootKey, _ := security.GenerateMasterKey()
		rootKeyProvider, _ := security.NewLocalKeyProvider(rootKey)
		keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, rawStore, rootKeyProvider, legacyKeyProvider)
		if codecError != nil {
			testContext.Fatal(codecError)
		}
		reportContext, stopReport := context.WithTimeout(ctx, 20*time.Millisecond)
		keyringCodec.RunKeyringReplicaReporter(reportContext, "cp-1", "v-test")
		stopReport()
		apiServer := NewServer(rawStore)
		apiServer.SetKeyringAdministration(keyringCodec, "v-test")
		apiAuthorizer := security.NewAPIAuthorizer()
		apiAuthorizer.Grant("user:secrets", security.CapabilitySecretWrite, security.ScopeCluster)
		apiServer.SetAPIAuthorizer(apiAuthorizer)
		return apiServer
	}
	sendRotate := func(apiServer *Server, principalName string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/secret/rotate", nil)
		principal := security.Principal{Kind: security.PrincipalKindUser, Name: principalName}
		request = request.WithContext(security.WithPrincipalStruct(request.Context(), principal))
		responseRecorder := httptest.NewRecorder()
		apiServer.handleSecretRotate(responseRecorder, request)
		return responseRecorder
	}

	legacyKey, _ := security.GenerateMasterKey()
	upgradedServer := newRotateServer(legacyKey)
	if refused := sendRotate(upgradedServer, "secrets"); refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "keyring is present") {
		testContext.Fatalf("upgraded cluster: %d %s", refused.Code, refused.Body.String())
	}

	freshServer := newRotateServer(nil)
	if forbidden := sendRotate(freshServer, "nobody"); forbidden.Code != http.StatusForbidden {
		testContext.Fatalf("without secret.write: %d %s", forbidden.Code, forbidden.Body.String())
	}
	started := sendRotate(freshServer, "secrets")
	if started.Code != http.StatusOK || !strings.Contains(started.Body.String(), `"target_version":2`) {
		testContext.Fatalf("fresh cluster: %d %s", started.Code, started.Body.String())
	}
	if again := sendRotate(freshServer, "secrets"); again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "already running") {
		testContext.Fatalf("second rotate: %d %s", again.Code, again.Body.String())
	}
}

// TestSecretMigrateKeysGateAndStart verifies POST /api/secret/migrate-keys
// answers 409 with the unmet conditions without backup confirmation, and
// starts the migration (200, in_progress) once the gate passes.
func TestSecretMigrateKeysGateAndStart(testContext *testing.T) {
	ctx := context.Background()
	rawStore := store.NewMemoryStore()
	legacyKey, _ := security.GenerateMasterKey()
	legacyStore, _ := security.NewSecretStoreWithMasterKey(rawStore, legacyKey)
	_ = legacyStore.PutSecret(ctx, "db.password", []byte("s3cret"))
	legacyKeyProvider, _ := security.NewLocalKeyProvider(legacyKey)
	rootKey, _ := security.GenerateMasterKey()
	rootKeyProvider, _ := security.NewLocalKeyProvider(rootKey)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, rawStore, rootKeyProvider, legacyKeyProvider)
	if codecError != nil {
		testContext.Fatal(codecError)
	}
	reportContext, stopReport := context.WithTimeout(ctx, 20*time.Millisecond)
	keyringCodec.RunKeyringReplicaReporter(reportContext, "cp-1", "v-test")
	stopReport()

	apiServer := NewServer(rawStore)
	apiServer.SetKeyringAdministration(keyringCodec, "v-test")
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	adminPrincipal := security.Principal{Kind: security.PrincipalKindUser, Name: "admin"}
	sendMigrateKeys := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/secret/migrate-keys", strings.NewReader(body))
		request = request.WithContext(security.WithPrincipalStruct(request.Context(), adminPrincipal))
		responseRecorder := httptest.NewRecorder()
		apiServer.handleSecretMigrateKeys(responseRecorder, request)
		return responseRecorder
	}

	refused := sendMigrateKeys(`{"confirm_backup": false}`)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "confirm_backup") {
		testContext.Fatalf("without backup: %d %s", refused.Code, refused.Body.String())
	}
	started := sendMigrateKeys(`{"confirm_backup": true}`)
	if started.Code != http.StatusOK || !strings.Contains(started.Body.String(), "in_progress") {
		testContext.Fatalf("with backup: %d %s", started.Code, started.Body.String())
	}
	again := sendMigrateKeys(`{"confirm_backup": true}`)
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "keyring is migrating") {
		testContext.Fatalf("second start: %d %s", again.Code, again.Body.String())
	}

	retireRequest := httptest.NewRequest(http.MethodPost, "/api/secret/retire-legacy-key", nil)
	retireRequest = retireRequest.WithContext(security.WithPrincipalStruct(retireRequest.Context(), adminPrincipal))
	retireRecorder := httptest.NewRecorder()
	apiServer.handleSecretRetireLegacyKey(retireRecorder, retireRequest)
	if retireRecorder.Code != http.StatusConflict || !strings.Contains(retireRecorder.Body.String(), "migration is in_progress") {
		testContext.Fatalf("retire before the migration completed: %d %s", retireRecorder.Code, retireRecorder.Body.String())
	}
}
