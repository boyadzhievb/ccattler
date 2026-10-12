// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// startTestSecretAPIServerWithServer creates a SecretAPIServer on a random
// port, starts it, and returns the server instance, base URL, and cancel func.
func startTestSecretAPIServerWithServer(testContext *testing.T, secretProvider SecretProvider, factStore store.StateStore) (*SecretAPIServer, string, context.CancelFunc) {
	testContext.Helper()

	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatal(listenError)
	}
	listenAddress := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	secretAPIServer := NewSecretAPIServer("test-node", listenAddress, secretProvider, factStore)

	go func() {
		_ = secretAPIServer.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	baseURL := "http://" + listenAddress
	return secretAPIServer, baseURL, cancel
}

func TestSecretAPIServerRegistersAndRevokesTokens(testContext *testing.T) {
	secretAPIServer := NewSecretAPIServer("test-node", "127.0.0.1:0", nil, nil)

	tokenHex, registerError := secretAPIServer.RegisterInstanceToken("instance-1", "web")
	if registerError != nil {
		testContext.Fatal(registerError)
	}
	if len(tokenHex) != secretTokenLength*2 {
		testContext.Fatalf("expected %d hex chars, got %d", secretTokenLength*2, len(tokenHex))
	}

	secretAPIServer.tokenMutex.RLock()
	metadata, exists := secretAPIServer.instanceTokens[tokenHex]
	secretAPIServer.tokenMutex.RUnlock()
	if !exists {
		testContext.Fatal("token not found after registration")
	}
	if metadata.instanceID != "instance-1" || metadata.serviceName != "web" {
		testContext.Fatalf("unexpected metadata: %+v", metadata)
	}

	secretAPIServer.RevokeInstanceToken("instance-1")

	secretAPIServer.tokenMutex.RLock()
	_, existsAfterRevoke := secretAPIServer.instanceTokens[tokenHex]
	secretAPIServer.tokenMutex.RUnlock()
	if existsAfterRevoke {
		testContext.Fatal("token still present after revocation")
	}
}

func TestSecretAPIServerGetSecretAuthorized(testContext *testing.T) {
	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{
			"db-password": []byte("s3cret123"),
		},
		grants: map[string]string{ //nolint:gosec // test grant map, not credentials
			"web/db-password": "/run/secrets/db-password",
		},
	}
	memoryStore := store.NewMemoryStore()

	secretServer, baseURL, cancel := startTestSecretAPIServerWithServer(testContext, mockProvider, memoryStore)
	defer cancel()

	tokenHex, _ := secretServer.RegisterInstanceToken("instance-1", "web")

	request, _ := http.NewRequest("GET", baseURL+"/v1/secrets/db-password", nil)
	request.Header.Set("Authorization", "Bearer "+tokenHex)

	response, httpError := http.DefaultClient.Do(request)
	if httpError != nil {
		testContext.Fatal(httpError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		testContext.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var secretResponse secretAPIResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&secretResponse); decodeError != nil {
		testContext.Fatal(decodeError)
	}
	if secretResponse.Name != "db-password" {
		testContext.Errorf("expected name db-password, got %s", secretResponse.Name)
	}
	if secretResponse.Value != "s3cret123" {
		testContext.Errorf("expected value s3cret123, got %s", secretResponse.Value)
	}
}

func TestSecretAPIServerGetSecretUnauthorized(testContext *testing.T) {
	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{},
		grants:  map[string]string{},
	}
	memoryStore := store.NewMemoryStore()

	_, baseURL, cancel := startTestSecretAPIServerWithServer(testContext, mockProvider, memoryStore)
	defer cancel()

	request, _ := http.NewRequest("GET", baseURL+"/v1/secrets/db-pass", nil)

	response, httpError := http.DefaultClient.Do(request)
	if httpError != nil {
		testContext.Fatal(httpError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		testContext.Fatalf("expected 401 without token, got %d", response.StatusCode)
	}

	request2, _ := http.NewRequest("GET", baseURL+"/v1/secrets/db-pass", nil)
	request2.Header.Set("Authorization", "Bearer invalidtoken")

	response2, httpError2 := http.DefaultClient.Do(request2)
	if httpError2 != nil {
		testContext.Fatal(httpError2)
	}
	defer response2.Body.Close()

	if response2.StatusCode != http.StatusUnauthorized {
		testContext.Fatalf("expected 401 with invalid token, got %d", response2.StatusCode)
	}
}

func TestSecretAPIServerGetSecretForbidden(testContext *testing.T) {
	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{
			"db-password": []byte("s3cret123"),
		},
		grants: map[string]string{ //nolint:gosec // test grant map, not credentials
			"web/db-password": "/run/secrets/db-password",
		},
	}
	memoryStore := store.NewMemoryStore()

	secretServer, baseURL, cancel := startTestSecretAPIServerWithServer(testContext, mockProvider, memoryStore)
	defer cancel()

	tokenHex, _ := secretServer.RegisterInstanceToken("instance-1", "scheduler")

	request, _ := http.NewRequest("GET", baseURL+"/v1/secrets/db-password", nil)
	request.Header.Set("Authorization", "Bearer "+tokenHex)

	response, httpError := http.DefaultClient.Do(request)
	if httpError != nil {
		testContext.Fatal(httpError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		testContext.Fatalf("expected 403 for ungranted secret, got %d", response.StatusCode)
	}
}

func TestSecretAPIServerListSecrets(testContext *testing.T) {
	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{
			"db-password":    []byte("s3cret"),
			"admin-password": []byte("adm1n"),
		},
		grants: map[string]string{ //nolint:gosec // test grant map, not credentials
			"web/db-password":    "/run/secrets/db-password",
			"web/admin-password": "/run/secrets/admin-password",
		},
	}
	memoryStore := store.NewMemoryStore()
	ctx := context.Background()
	memoryStore.Put(ctx, "desired/service/web/secret/db-password", []byte("/run/secrets/db-password"))
	memoryStore.Put(ctx, "desired/service/web/secret/admin-password", []byte("/run/secrets/admin-password"))

	secretServer, baseURL, cancel := startTestSecretAPIServerWithServer(testContext, mockProvider, memoryStore)
	defer cancel()

	tokenHex, _ := secretServer.RegisterInstanceToken("instance-1", "web")

	request, _ := http.NewRequest("GET", baseURL+"/v1/secrets", nil)
	request.Header.Set("Authorization", "Bearer "+tokenHex)

	response, httpError := http.DefaultClient.Do(request)
	if httpError != nil {
		testContext.Fatal(httpError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		testContext.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var listResponse secretListResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&listResponse); decodeError != nil {
		testContext.Fatal(decodeError)
	}
	if listResponse.Service != "web" {
		testContext.Errorf("expected service webserver, got %s", listResponse.Service)
	}
	if len(listResponse.Secrets) != 2 {
		testContext.Fatalf("expected 2 secrets, got %d: %v", len(listResponse.Secrets), listResponse.Secrets)
	}
}

// TestReconcileWithSecretAPIRegistersToken verifies that when a SecretAPIServer
// is configured, the agent registers a bearer token for the instance during
// reconciliation and does not materialize secrets to disk.
func TestReconcileWithSecretAPIRegistersToken(testContext *testing.T) {
	memoryStore := store.NewMemoryStore()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{"db-password": []byte("s3cret")},
		grants:  map[string]string{"web/db-password": "/run/secrets/db-password"}, //nolint:gosec // test grant map
	}

	nodeAgent := New("test-node", memoryStore, simulatorRuntime)
	nodeAgent.SetSecretProvider(mockProvider)

	secretAPIServer := NewSecretAPIServer("test-node", "127.0.0.1:0", mockProvider, memoryStore)
	nodeAgent.SetSecretAPIServer(secretAPIServer)

	memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))

	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-001", service: "web"})

	status, statusError := simulatorRuntime.Status(ctx, "inst-001")
	if statusError != nil {
		testContext.Fatal(statusError)
	}
	if !status.Running {
		testContext.Fatal("instance should be running")
	}

	secretAPIServer.tokenMutex.RLock()
	tokenCount := len(secretAPIServer.instanceTokens)
	var foundService string
	for _, metadata := range secretAPIServer.instanceTokens {
		if metadata.instanceID == "inst-001" {
			foundService = metadata.serviceName
		}
	}
	secretAPIServer.tokenMutex.RUnlock()

	if tokenCount != 1 {
		testContext.Fatalf("expected 1 registered token, got %d", tokenCount)
	}
	if foundService != "web" {
		testContext.Errorf("expected token for webserver, got %s", foundService)
	}

	if len(nodeAgent.materializedSecrets) != 0 {
		testContext.Errorf("expected no materialized secrets with API mode, got %d", len(nodeAgent.materializedSecrets))
	}
}

// TestSecretAPITokenRevokedOnCleanup verifies that the bearer token is revoked
// when an instance is cleaned up.
func TestSecretAPITokenRevokedOnCleanup(testContext *testing.T) {
	memoryStore := store.NewMemoryStore()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	mockProvider := &mockSecretProvider{
		secrets: map[string][]byte{"db-password": []byte("s3cret")},
		grants:  map[string]string{"web/db-password": "/run/secrets/db-password"}, //nolint:gosec // test grant map
	}

	nodeAgent := New("test-node", memoryStore, simulatorRuntime)
	nodeAgent.SetSecretProvider(mockProvider)

	secretAPIServer := NewSecretAPIServer("test-node", "127.0.0.1:0", mockProvider, memoryStore)
	nodeAgent.SetSecretAPIServer(secretAPIServer)

	memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))

	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-001", service: "web"})

	secretAPIServer.tokenMutex.RLock()
	tokensBefore := len(secretAPIServer.instanceTokens)
	secretAPIServer.tokenMutex.RUnlock()
	if tokensBefore != 1 {
		testContext.Fatalf("expected 1 token before cleanup, got %d", tokensBefore)
	}

	nodeAgent.cleanupUndesiredInstance(ctx, "inst-001")

	secretAPIServer.tokenMutex.RLock()
	tokensAfter := len(secretAPIServer.instanceTokens)
	secretAPIServer.tokenMutex.RUnlock()
	if tokensAfter != 0 {
		testContext.Fatalf("expected 0 tokens after cleanup, got %d", tokensAfter)
	}
}
