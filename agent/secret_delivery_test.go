// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// deliveryTestSecretName is the granted secret used by the delivery tests.
const deliveryTestSecretName = "db-password" //nolint:gosec // G101: test secret name, not a credential

// newDeliveryTestAgent returns an agent for service "web" with one granted
// secret, a fake delivery client and a tmpfs-like material store in TempDir.
func newDeliveryTestAgent(testContext *testing.T) (*Agent, *fakeSecretDeliveryClient, *specRecordingRuntime, store.StateStore, *SecretMaterialStore) {
	memoryStore := store.NewMemoryStore()
	recordingRuntime := newSpecRecordingRuntime()
	materialStore := NewSecretMaterialStore(filepath.Join(testContext.TempDir(), "secrets"))
	deliveryClient := &fakeSecretDeliveryClient{secretValues: map[string][]byte{deliveryTestSecretName: []byte("s3cret")}}
	nodeAgent := New("node-1", memoryStore, recordingRuntime)
	nodeAgent.SetSecretDelivery(deliveryClient, materialStore)
	nodeAgent.legacyKeyFilePath = filepath.Join(testContext.TempDir(), "secret-master.key")
	ctx := context.Background()
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceSecret("web", deliveryTestSecretName), []byte("/run/secrets/db-password"))
	return nodeAgent, deliveryClient, recordingRuntime, memoryStore, materialStore
}

// readInstanceState returns the observed state of an instance.
func readInstanceState(memoryStore store.StateStore, instanceID string) types.InstanceState {
	stateFact, getError := memoryStore.Get(context.Background(), types.KeyObservedInstanceState(instanceID))
	if getError != nil {
		return ""
	}
	return types.InstanceState(stateFact.Value)
}

// readSecretWaitStatus returns the decoded secret-wait fact of an instance.
func readSecretWaitStatus(testContext *testing.T, memoryStore store.StateStore, instanceID string) secretWaitStatus {
	testContext.Helper()
	waitFact, getError := memoryStore.Get(context.Background(), types.KeyObservedInstanceSecretWait(instanceID))
	if getError != nil {
		testContext.Fatalf("secret-wait fact missing: %v", getError)
	}
	var waitStatus secretWaitStatus
	_ = json.Unmarshal(waitFact.Value, &waitStatus)
	return waitStatus
}

func TestDeliveredSecretsAreMountedReadOnlyFromMaterial(testContext *testing.T) {
	nodeAgent, _, recordingRuntime, memoryStore, materialStore := newDeliveryTestAgent(testContext)
	nodeAgent.reconcileDesiredInstance(context.Background(), placedInstanceInfo{id: "inst-1", service: "web"})

	startedSpec, started := recordingRuntime.startedSpec("inst-1")
	if !started {
		testContext.Fatal("instance not started")
	}
	for _, configContent := range startedSpec.ConfigFiles {
		if strings.Contains(configContent, "s3cret") {
			testContext.Fatal("secret value must not be passed through ConfigFiles")
		}
	}
	hostPath := startedSpec.SecretMounts["/run/secrets/db-password"]
	if hostPath == "" || startedSpec.SecretMounts[ccaSecretMountPrefix+deliveryTestSecretName] != hostPath {
		testContext.Fatalf("unexpected secret mounts %v", startedSpec.SecretMounts)
	}
	fileInfo, statError := os.Stat(hostPath)
	if statError != nil || fileInfo.Mode().Perm() != secretMaterialFileMode {
		testContext.Fatalf("secret file mode = %v (%v)", fileInfo.Mode().Perm(), statError)
	}
	directoryInfo, _ := os.Stat(materialStore.instanceDirectory("inst-1"))
	if directoryInfo.Mode().Perm() != secretMaterialDirectoryMode {
		testContext.Fatalf("material directory mode = %v", directoryInfo.Mode().Perm())
	}
	if readInstanceState(memoryStore, "inst-1") == types.InstanceWaitingForSecrets {
		testContext.Fatal("instance must not stay in waiting-for-secrets after delivery")
	}
}

func TestUnreachableAPIWaitsBeforePullAndRecovers(testContext *testing.T) {
	nodeAgent, deliveryClient, recordingRuntime, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: "connection refused"})
	instance := placedInstanceInfo{id: "inst-1", service: "web"}

	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if _, started := recordingRuntime.startedSpec("inst-1"); started || len(recordingRuntime.PulledImages) != 0 {
		testContext.Fatal("instance must not be pulled or started without its secrets")
	}
	if state := readInstanceState(memoryStore, "inst-1"); state != types.InstanceWaitingForSecrets {
		testContext.Fatalf("state = %s, want waiting-for-secrets", state)
	}
	if waitStatus := readSecretWaitStatus(testContext, memoryStore, "inst-1"); waitStatus.Reason != string(SecretWaitAPIUnreachable) {
		testContext.Fatalf("wait reason = %s", waitStatus.Reason)
	}

	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if deliveryClient.fetches() != 1 {
		testContext.Fatalf("retry must wait for the backoff, fetches = %d", deliveryClient.fetches())
	}

	deliveryClient.setDeliveryError(nil)
	nodeAgent.secretDeliveryWaits["inst-1"].retryAfter = time.Now()
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if _, started := recordingRuntime.startedSpec("inst-1"); !started {
		testContext.Fatal("instance must start once the API is reachable")
	}
	if _, getError := memoryStore.Get(ctx, types.KeyObservedInstanceSecretWait("inst-1")); getError == nil {
		testContext.Fatal("secret-wait fact must be cleared after delivery")
	}
}

func TestCachedImageWithPullSecretStillWaitsForSecrets(testContext *testing.T) {
	nodeAgent, deliveryClient, recordingRuntime, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImagePullSecret("web"), []byte("registry-creds"))
	recordingRuntime.PulledImages = []string{"nginx:1.27"}
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: "down"})

	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-1", service: "web"})
	if _, started := recordingRuntime.startedSpec("inst-1"); started {
		testContext.Fatal("cached image must not bypass secret delivery")
	}
}

func TestRefusedDeliveryRetriesOnGrantChangeNotOnEveryCycle(testContext *testing.T) {
	nodeAgent, deliveryClient, _, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitNotAuthorized, SecretNames: []string{deliveryTestSecretName}, Detail: "no grant"})
	instance := placedInstanceInfo{id: "inst-1", service: "web"}

	nodeAgent.reconcileDesiredInstance(ctx, instance)
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if deliveryClient.fetches() != 1 {
		testContext.Fatalf("refusal must not be retried every cycle, fetches = %d", deliveryClient.fetches())
	}
	if waitEntry := nodeAgent.secretDeliveryWaits["inst-1"]; time.Until(waitEntry.retryAfter) < secretDeliveryRefusedRetry-time.Minute {
		testContext.Fatalf("refusal fallback retry too early: %v", waitEntry.retryAfter)
	}
	waitStatus := readSecretWaitStatus(testContext, memoryStore, "inst-1")
	if waitStatus.Reason != string(SecretWaitNotAuthorized) || len(waitStatus.Secrets) != 1 {
		testContext.Fatalf("wait status = %+v", waitStatus)
	}

	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceSecret("web", "api-key"), []byte(""))
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if deliveryClient.fetches() != 2 {
		testContext.Fatalf("grant change must retry at once, fetches = %d", deliveryClient.fetches())
	}
}

func TestDeliveryNotConfiguredWaitsWithReason(testContext *testing.T) {
	nodeAgent, _, recordingRuntime, memoryStore, materialStore := newDeliveryTestAgent(testContext)
	nodeAgent.SetSecretDelivery(nil, materialStore)
	nodeAgent.reconcileDesiredInstance(context.Background(), placedInstanceInfo{id: "inst-1", service: "web"})
	if _, started := recordingRuntime.startedSpec("inst-1"); started {
		testContext.Fatal("instance must not start without delivery")
	}
	if waitStatus := readSecretWaitStatus(testContext, memoryStore, "inst-1"); waitStatus.Reason != string(SecretWaitDeliveryNotConfigured) {
		testContext.Fatalf("wait reason = %s", waitStatus.Reason)
	}
}

func TestContainerRestartReusesMaterialWithoutFetching(testContext *testing.T) {
	nodeAgent, deliveryClient, recordingRuntime, _, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	instance := placedInstanceInfo{id: "inst-1", service: "web"}
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	recordingRuntime.KillWorkload("inst-1", 1, "crash")
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: "down"})

	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if deliveryClient.fetches() != 1 {
		testContext.Fatalf("restart must reuse material, fetches = %d", deliveryClient.fetches())
	}
	if status, _ := recordingRuntime.Status(ctx, "inst-1"); !status.Running {
		testContext.Fatal("instance must restart with existing material while the API is down")
	}
}

func TestSweepKeepsRunningOrPlacedMaterialAndRemovesOrphans(testContext *testing.T) {
	nodeAgent, _, _, _, materialStore := newDeliveryTestAgent(testContext)
	for _, instanceID := range []string{"placed", "running-unplaced", "orphan"} {
		if writeError := materialStore.WriteInstanceSecrets(instanceID, map[string][]byte{deliveryTestSecretName: []byte("x")}); writeError != nil {
			testContext.Fatal(writeError)
		}
	}
	running := []runtime.Status{{ID: "running-unplaced", Running: true}}
	nodeAgent.sweepSecretMaterial(context.Background(), map[string]bool{"placed": true}, running)
	if remaining := strings.Join(materialStore.ListInstances(), ","); remaining != "placed,running-unplaced" {
		testContext.Fatalf("remaining material = %s", remaining)
	}
}

func TestConnectionURLWrittenToMaterialAndMounted(testContext *testing.T) {
	nodeAgent, _, recordingRuntime, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceConnection("web", "db"), []byte("/run/connections/db"))
	_, _ = memoryStore.Put(ctx, types.KeyDesiredConnection("db"), []byte(`{"driver":"postgresql","user":"app","secret":"db-password","host":"db","port":5432,"database":"app"}`))

	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-1", service: "web"})
	startedSpec, _ := recordingRuntime.startedSpec("inst-1")
	connectionContent, readError := os.ReadFile(startedSpec.SecretMounts["/run/connections/db"])
	if readError != nil || !strings.Contains(string(connectionContent), "app:s3cret@db:5432/app") {
		testContext.Fatalf("connection file = %q (%v)", connectionContent, readError)
	}
}

func TestPublishSecretDeliveryCapabilityReportsLegacyKeyFile(testContext *testing.T) {
	nodeAgent, _, _, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	nodeAgent.publishSecretDeliveryCapability(ctx)
	legacyFact, _ := memoryStore.Get(ctx, types.KeyObservedNodeLegacyKeyFile("node-1"))
	deliveryFact, _ := memoryStore.Get(ctx, types.KeyObservedNodeSecretDelivery("node-1"))
	if string(legacyFact.Value) != "absent" || string(deliveryFact.Value) != secretDeliveryProtocolVersion {
		testContext.Fatalf("legacy=%s delivery=%s", legacyFact.Value, deliveryFact.Value)
	}
	_ = os.WriteFile(nodeAgent.legacyKeyFilePath, []byte("k"), 0o600)
	nodeAgent.publishSecretDeliveryCapability(ctx)
	legacyFact, _ = memoryStore.Get(ctx, types.KeyObservedNodeLegacyKeyFile("node-1"))
	if string(legacyFact.Value) != "present" {
		testContext.Fatalf("legacy=%s, want present", legacyFact.Value)
	}
}

func TestSecretMaterialStoreRejectsUnsafeNamesAndCleansStaging(testContext *testing.T) {
	materialStore := NewSecretMaterialStore(testContext.TempDir())
	for _, unsafeName := range []string{"", "..", "../x", "a//b", secretMaterialStagingMarker + "x"} {
		if writeError := materialStore.WriteInstanceSecrets("inst-1", map[string][]byte{unsafeName: []byte("x")}); writeError == nil {
			testContext.Fatalf("expected rejection of secret name %q", unsafeName)
		}
		if writeError := materialStore.WriteInstanceSecrets(unsafeName, nil); writeError == nil {
			testContext.Fatalf("expected rejection of instance id %q", unsafeName)
		}
	}
	_ = os.MkdirAll(filepath.Join(materialStore.baseDirectory, secretMaterialStagingMarker+"inst-9-abcd"), 0o700)
	if instanceIDs := materialStore.ListInstances(); len(instanceIDs) != 0 {
		testContext.Fatalf("staging directories must not be listed: %v", instanceIDs)
	}
	if _, statError := os.Stat(filepath.Join(materialStore.baseDirectory, secretMaterialStagingMarker+"inst-9-abcd")); !os.IsNotExist(statError) {
		testContext.Fatal("staging directory must be removed")
	}
}

func TestHTTPSecretDeliveryClientMapsResponses(testContext *testing.T) {
	testCases := []struct {
		statusCode     int              // statusCode is the server response status.
		responseBody   string           // responseBody is the server response body.
		expectedReason SecretWaitReason // expectedReason is the mapped wait reason ("" = success).
	}{
		{http.StatusOK, `{"instance_id":"i","service":"web","secrets":{"db-password":"czNjcmV0"}}`, ""},
		{http.StatusForbidden, `{"error":"no grant","reason":"not_authorized","secrets":["db-password"]}`, SecretWaitNotAuthorized},
		{http.StatusConflict, `{"error":"missing","reason":"secret_missing","secrets":["db-password"]}`, SecretWaitSecretMissing},
		{http.StatusTooManyRequests, `{"error":"rate limit"}`, SecretWaitAPIUnreachable},
		{http.StatusInternalServerError, `{}`, SecretWaitAPIUnreachable},
	}
	for _, testCase := range testCases {
		testServer := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
			if request.URL.Path != nodeSecretDeliveryPath || request.Method != http.MethodPost {
				responseWriter.WriteHeader(http.StatusNotFound)
				return
			}
			responseWriter.WriteHeader(testCase.statusCode)
			_, _ = responseWriter.Write([]byte(testCase.responseBody))
		}))
		testTransport, _ := testServer.Client().Transport.(*http.Transport)
		deliveryClient := NewHTTPSecretDeliveryClient(testServer.URL, testTransport.TLSClientConfig)
		secretValues, fetchError := deliveryClient.FetchInstanceSecrets(context.Background(), "i")
		testServer.Close()
		if testCase.expectedReason == "" {
			if fetchError != nil || string(secretValues["db-password"]) != "s3cret" {
				testContext.Fatalf("status %d: values %v error %v", testCase.statusCode, secretValues, fetchError)
			}
			continue
		}
		deliveryError, isDeliveryError := fetchError.(*SecretDeliveryError)
		if !isDeliveryError || deliveryError.Reason != testCase.expectedReason {
			testContext.Fatalf("status %d: error %v, want reason %s", testCase.statusCode, fetchError, testCase.expectedReason)
		}
	}
}

// failingScanStore fails every Scan, simulating an etcd outage for reads.
type failingScanStore struct {
	store.StateStore
}

// Scan always fails.
func (failingStore failingScanStore) Scan(context.Context, string) ([]store.Fact, error) {
	return nil, errors.New("etcd unavailable")
}

func TestStoreErrorNeverStartsWithoutSecrets(testContext *testing.T) {
	_, deliveryClient, _, memoryStore, materialStore := newDeliveryTestAgent(testContext)
	recordingRuntime := newSpecRecordingRuntime()
	nodeAgent := New("node-1", failingScanStore{StateStore: memoryStore}, recordingRuntime)
	nodeAgent.SetSecretDelivery(deliveryClient, materialStore)
	nodeAgent.reconcileDesiredInstance(context.Background(), placedInstanceInfo{id: "inst-1", service: "web"})
	if _, started := recordingRuntime.startedSpec("inst-1"); started || len(recordingRuntime.PulledImages) != 0 {
		testContext.Fatal("a store error must not be treated as 'no secrets needed'")
	}
}

func TestRemovedGrantOrUngrantedConnectionForcesNewDelivery(testContext *testing.T) {
	nodeAgent, deliveryClient, _, memoryStore, materialStore := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	_ = materialStore.WriteInstanceSecrets("inst-1", map[string][]byte{deliveryTestSecretName: []byte("old"), "revoked": []byte("x")})
	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-1", service: "web"})
	if deliveryClient.fetches() != 1 {
		testContext.Fatalf("material with a revoked secret must be re-delivered, fetches = %d", deliveryClient.fetches())
	}
	if values, _ := materialStore.ReadInstanceSecrets("inst-1"); len(values) != 1 || string(values[deliveryTestSecretName]) != "s3cret" {
		testContext.Fatalf("material after re-delivery = %v", values)
	}

	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceImagePullSecret("web"), []byte("registry-creds"))
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitNotAuthorized, SecretNames: []string{"registry-creds"}, Detail: "no grant"})
	nodeAgent.reconcileDesiredInstance(ctx, placedInstanceInfo{id: "inst-1", service: "web"})
	if deliveryClient.fetches() != 2 || readInstanceState(memoryStore, "inst-1") != types.InstanceWaitingForSecrets {
		testContext.Fatalf("an ungranted pull secret must not reuse material, fetches = %d", deliveryClient.fetches())
	}
}

func TestExplicitSecretMountPathsAreCleanedAndRestricted(testContext *testing.T) {
	for _, testCase := range []struct {
		mountPath    string // mountPath is the grant's mount path.
		expectedPath string // expectedPath is the cleaned path ("" = unusable).
	}{
		{"/run/secrets//db/", "/run/secrets/db"},
		{"/proc/self/environ", ""},
		{"/run/cca-secrets/other", ""},
		{"relative", ""},
		{"/", ""},
	} {
		cleanedPath, usable := resolveExplicitSecretMountPath(testCase.mountPath)
		if (testCase.expectedPath == "") == usable || cleanedPath != testCase.expectedPath {
			testContext.Fatalf("%q → %q (%v), want %q", testCase.mountPath, cleanedPath, usable, testCase.expectedPath)
		}
	}
}

func TestHierarchicalSecretNamesRoundTripOnDisk(testContext *testing.T) {
	materialStore := NewSecretMaterialStore(testContext.TempDir())
	if writeError := materialStore.WriteInstanceSecrets("inst-1", map[string][]byte{"payments/db-password": []byte("p")}); writeError != nil {
		testContext.Fatal(writeError)
	}
	values, _ := materialStore.ReadInstanceSecrets("inst-1")
	if string(values["payments/db-password"]) != "p" || filepath.Base(materialStore.SecretValuePath("inst-1", "payments/db-password")) != "payments%2Fdb-password" {
		testContext.Fatalf("round trip failed: %v", values)
	}
}

func TestConfigChangeRestartDropsMaterialAndMissingSecretRetriesSooner(testContext *testing.T) {
	nodeAgent, _, _, memoryStore, materialStore := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	instance := placedInstanceInfo{id: "inst-1", service: "web"}
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	_, _ = memoryStore.Put(ctx, types.KeyObservedInstanceConfigHash("inst-1"), []byte("stale-hash"))
	if !nodeAgent.detectConfigChange(ctx, instance) {
		testContext.Fatal("expected a config change")
	}
	if _, hasMaterial := materialStore.ReadInstanceSecrets("inst-1"); hasMaterial {
		testContext.Fatal("config-change restart must re-fetch current secret values")
	}
	if delay := secretDeliveryRetryDelay(SecretWaitSecretMissing, 1); delay != secretDeliveryRetryMax {
		testContext.Fatalf("secret_missing retry = %v", delay)
	}
}

func TestMaterialStoreRefusesDiskBackedDirectoryWhenRequired(testContext *testing.T) {
	baseDirectory := testContext.TempDir()
	if memoryBacked, _ := isMemoryFilesystem(baseDirectory); memoryBacked {
		testContext.Skip("temporary directory is memory-backed on this host")
	}
	materialStore := NewSecretMaterialStore(baseDirectory)
	materialStore.RequireMemoryFilesystem()
	if writeError := materialStore.WriteInstanceSecrets("inst-1", map[string][]byte{"a": []byte("x")}); writeError == nil {
		testContext.Fatal("writing secrets to a disk-backed directory must be refused")
	}
	if remaining := materialStore.ListInstances(); len(remaining) != 0 {
		testContext.Fatalf("nothing may be written: %v", remaining)
	}
}

func TestStateLeavesWaitingForSecretsAfterLateDeliveryWithCachedImage(testContext *testing.T) {
	nodeAgent, deliveryClient, recordingRuntime, memoryStore, _ := newDeliveryTestAgent(testContext)
	ctx := context.Background()
	recordingRuntime.PulledImages = []string{"nginx:1.27"}
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceInitStep("web", 0), []byte(""))
	_, _ = memoryStore.Put(ctx, types.KeyDesiredServiceInitStepExec("web", 0), []byte("db-check"))
	recordingRuntime.ExecInitFailures = map[string]int{"db-check": 100}
	instance := placedInstanceInfo{id: "inst-1", service: "web"}
	deliveryClient.setDeliveryError(&SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: "down"})
	nodeAgent.reconcileDesiredInstance(ctx, instance)

	deliveryClient.setDeliveryError(nil)
	nodeAgent.secretDeliveryWaits["inst-1"].retryAfter = time.Now()
	nodeAgent.reconcileDesiredInstance(ctx, instance)
	if state := readInstanceState(memoryStore, "inst-1"); state == types.InstanceWaitingForSecrets {
		testContext.Fatal("state must leave waiting-for-secrets once secrets are delivered, even while init steps fail")
	}
}
