// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// specRecordingRuntime wraps the simulator and records every started spec so
// tests can inspect env vars and mounts.
type specRecordingRuntime struct {
	*runtime.SimulatorRuntime
	mutex        sync.Mutex              // mutex guards startedSpecs.
	startedSpecs map[string]runtime.Spec // startedSpecs maps instance ID to its last started spec.
}

// newSpecRecordingRuntime returns a recording wrapper around a new simulator.
func newSpecRecordingRuntime() *specRecordingRuntime {
	return &specRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime(), startedSpecs: make(map[string]runtime.Spec)}
}

// Start records the spec, then delegates to the simulator.
func (recordingRuntime *specRecordingRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	recordingRuntime.mutex.Lock()
	recordingRuntime.startedSpecs[spec.ID] = spec
	recordingRuntime.mutex.Unlock()
	return recordingRuntime.SimulatorRuntime.Start(ctx, spec)
}

// startedSpec returns the last spec started for the instance.
func (recordingRuntime *specRecordingRuntime) startedSpec(instanceID string) (runtime.Spec, bool) {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	spec, started := recordingRuntime.startedSpecs[instanceID]
	return spec, started
}

// startTestSecretAPIServer creates a SecretAPIServer on a random port backed
// by materialStore and returns it with its base URL and a cancel function.
func startTestSecretAPIServer(testContext *testing.T, materialStore *SecretMaterialStore, grantStore store.StateStore) (*SecretAPIServer, string, context.CancelFunc) {
	testContext.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatal(listenError)
	}
	listenAddress := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	secretAPIServer := NewSecretAPIServer("test-node", listenAddress, materialStore, grantStore)
	go func() {
		_ = secretAPIServer.Start(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	return secretAPIServer, "http://" + listenAddress, cancel
}

// getWithBearerToken performs GET url with the bearer token and returns status and body.
func getWithBearerToken(testContext *testing.T, requestURL string, bearerToken string) (int, []byte) {
	testContext.Helper()
	request, _ := http.NewRequest(http.MethodGet, requestURL, nil)
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		testContext.Fatal(requestError)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, _ := io.ReadAll(response.Body)
	return response.StatusCode, responseBody
}

func TestSecretAPIServerRegistersAndRevokesTokens(testContext *testing.T) {
	secretAPIServer := NewSecretAPIServer("test-node", "127.0.0.1:0", NewSecretMaterialStore(testContext.TempDir()), nil)
	tokenHex, registerError := secretAPIServer.RegisterInstanceToken("instance-1", "web")
	if registerError != nil {
		testContext.Fatal(registerError)
	}
	if len(tokenHex) != secretTokenLength*2 {
		testContext.Fatalf("expected %d hex chars, got %d", secretTokenLength*2, len(tokenHex))
	}
	secretAPIServer.RevokeInstanceToken("instance-1")
	secretAPIServer.tokenMutex.RLock()
	_, existsAfterRevoke := secretAPIServer.instanceTokens[tokenHex]
	secretAPIServer.tokenMutex.RUnlock()
	if existsAfterRevoke {
		testContext.Fatal("token still present after revocation")
	}
}

func TestSecretAPIServerServesOnlyDeliveredMaterial(testContext *testing.T) {
	materialStore := NewSecretMaterialStore(testContext.TempDir())
	if writeError := materialStore.WriteInstanceSecrets("inst-1", map[string][]byte{"db-password": []byte("s3cret"), "api-key": []byte("k")}); writeError != nil {
		testContext.Fatal(writeError)
	}
	grantStore := store.NewMemoryStore()
	for _, grantedName := range []string{"db-password", "api-key"} {
		_, _ = grantStore.Put(context.Background(), types.KeyDesiredServiceSecret("web", grantedName), []byte(""))
	}
	secretAPIServer, baseURL, cancel := startTestSecretAPIServer(testContext, materialStore, grantStore)
	defer cancel()
	instanceToken, _ := secretAPIServer.RegisterInstanceToken("inst-1", "web")
	otherToken, _ := secretAPIServer.RegisterInstanceToken("inst-2", "web")

	if statusCode, responseBody := getWithBearerToken(testContext, baseURL+"/v1/secrets/db-password?raw=true", instanceToken); statusCode != http.StatusOK || string(responseBody) != "s3cret" {
		testContext.Fatalf("delivered secret: status %d body %q", statusCode, responseBody)
	}
	if statusCode, _ := getWithBearerToken(testContext, baseURL+"/v1/secrets/not-delivered", instanceToken); statusCode != http.StatusForbidden {
		testContext.Fatalf("undelivered secret: status %d, want 403", statusCode)
	}
	if statusCode, _ := getWithBearerToken(testContext, baseURL+"/v1/secrets/db-password", otherToken); statusCode != http.StatusForbidden {
		testContext.Fatalf("other instance's secret: status %d, want 403", statusCode)
	}
	_ = grantStore.Delete(context.Background(), types.KeyDesiredServiceSecret("web", "api-key"))
	if statusCode, _ := getWithBearerToken(testContext, baseURL+"/v1/secrets/api-key", instanceToken); statusCode != http.StatusForbidden {
		testContext.Fatalf("revoked grant: status %d, want 403", statusCode)
	}
	if statusCode, _ := getWithBearerToken(testContext, baseURL+"/v1/secrets/db-password", "invalid"); statusCode != http.StatusUnauthorized {
		testContext.Fatalf("invalid token: status %d, want 401", statusCode)
	}
	statusCode, responseBody := getWithBearerToken(testContext, baseURL+"/v1/secrets", instanceToken)
	var listResponse secretListResponse
	_ = json.Unmarshal(responseBody, &listResponse)
	if statusCode != http.StatusOK || len(listResponse.Secrets) != 1 || listResponse.Secrets[0] != "db-password" {
		testContext.Fatalf("list: status %d, %+v", statusCode, listResponse)
	}
}

// newLegacySecretAPITestAgent returns an agent with fake delivery, a secret API
// server and a service "web" with one granted secret.
func newLegacySecretAPITestAgent(testContext *testing.T) (*Agent, *SecretAPIServer, *specRecordingRuntime, store.StateStore) {
	memoryStore := store.NewMemoryStore()
	recordingRuntime := newSpecRecordingRuntime()
	ctx := context.Background()
	materialStore := NewSecretMaterialStore(testContext.TempDir())
	nodeAgent := New("test-node", memoryStore, recordingRuntime)
	nodeAgent.SetSecretDelivery(&fakeSecretDeliveryClient{secretValues: map[string][]byte{"db-password": []byte("s3cret")}}, materialStore)
	secretAPIServer := NewSecretAPIServer("test-node", "127.0.0.1:0", materialStore, memoryStore)
	nodeAgent.SetSecretAPIServer(secretAPIServer)
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte("/run/secrets/db-password"))
	return nodeAgent, secretAPIServer, recordingRuntime, memoryStore
}

func TestLegacySecretAPITokenOnlyWithOptIn(testContext *testing.T) {
	nodeAgent, secretAPIServer, recordingRuntime, memoryStore := newLegacySecretAPITestAgent(testContext)
	ctx := context.Background()

	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-001", service: "web"})
	defaultSpec, started := recordingRuntime.startedSpec("inst-001")
	if !started || defaultSpec.Env["CCA_SECRET_TOKEN"] != "" || len(defaultSpec.SecretMounts) == 0 {
		testContext.Fatalf("without opt-in: started=%v env=%v mounts=%v", started, defaultSpec.Env, defaultSpec.SecretMounts)
	}

	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceLegacySecretAPI("web"), []byte("true"))
	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-002", service: "web"})
	legacySpec, _ := recordingRuntime.startedSpec("inst-002")
	if legacySpec.Env["CCA_SECRET_TOKEN"] == "" || legacySpec.Env["CCA_SECRET_URL"] == "" {
		testContext.Fatalf("with opt-in: expected secret API env, got %v", legacySpec.Env)
	}

	nodeAgent.cleanupUndesiredInstance(ctx, "inst-002")
	secretAPIServer.tokenMutex.RLock()
	tokensAfter := len(secretAPIServer.instanceTokens)
	secretAPIServer.tokenMutex.RUnlock()
	if tokensAfter != 0 {
		testContext.Fatalf("expected 0 tokens after cleanup, got %d", tokensAfter)
	}
}
