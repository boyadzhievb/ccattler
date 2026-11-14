// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// putStatusFixtureFacts writes two services, three instances (running,
// pending, stopped) and one node, so every section of the text has content.
func putStatusFixtureFacts(t *testing.T, factStore store.StateStore) {
	t.Helper()
	ctx := context.Background()
	facts := map[string]string{
		types.KeyDesiredService("web"):                  "",
		types.KeyDesiredServiceImage("web"):             "nginx:1.27",
		types.KeyDesiredServiceInstances("web"):         "2",
		types.KeyDesiredService("worker"):               "",
		types.KeyDesiredServiceImage("worker"):          "busybox:1",
		types.KeyDesiredServiceInstances("worker"):      "1",
		types.KeyObservedNodeState("node-1"):            "alive",
		types.KeyObservedInstanceService("web-aaaa"):    "web",
		types.KeyObservedInstanceState("web-aaaa"):      string(types.InstanceRunning),
		types.KeyObservedInstanceService("web-bbbb"):    "web",
		types.KeyObservedInstanceState("web-bbbb"):      string(types.InstancePending),
		types.KeyObservedInstanceService("worker-cccc"): "worker",
		types.KeyObservedInstanceState("worker-cccc"):   string(types.InstanceStopped),
		types.KeyPlacementInstance("web-aaaa"):          "node-1",
		types.KeyObservedInstanceIP("web-aaaa"):         "10.100.1.5",
	}
	for factKey, factValue := range facts {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
}

// TestStatusTextThroughAPIMatchesInProcessText checks that rendering the
// /api/status?all=true JSON gives the same text the in-process builder
// printed before `cca status` moved to the API (task 3.2: same layout).
func TestStatusTextThroughAPIMatchesInProcessText(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	putStatusFixtureFacts(t, factStore)

	apiServer := api.NewServer(factStore)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&noGrantsTestAuthenticator{}))
	apiServer.SetRequirePrincipal(true)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:nogrants", security.CapabilityWorkloadRead, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiTestServer := httptest.NewServer(apiServer.Handler())
	defer apiTestServer.Close()

	response, requestError := http.Get(apiTestServer.URL + "/api/status?all=true")
	if requestError != nil {
		t.Fatal(requestError)
	}
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status = %d: %s", response.StatusCode, responseBody)
	}
	var statusData clusterStatusResponse
	if decodeError := json.Unmarshal(responseBody, &statusData); decodeError != nil {
		t.Fatal(decodeError)
	}
	var activeInstances []instanceStatusEntry
	instanceStates := make([]string, 0, len(statusData.Instances))
	for _, instance := range statusData.Instances {
		instanceStates = append(instanceStates, instance.State)
		if instance.State != string(types.InstanceStopped) {
			activeInstances = append(activeInstances, instance)
		}
	}
	statusData.Instances = activeInstances

	throughAPI := renderStatusText(statusData, instanceStates)
	inProcess := buildStatusTextOutput(context.Background(), factStore)
	for _, wantFragment := range []string{"INSTANCES (2 active)", "1 stopped", "web-aaaa", "10.100.1.5", "NODES (1)"} {
		if !strings.Contains(inProcess, wantFragment) {
			t.Errorf("fixture text lacks %q, so the comparison would prove nothing:\n%s", wantFragment, inProcess)
		}
	}
	if throughAPI != inProcess {
		t.Errorf("text through the API differs from the in-process text\n--- through API ---\n%s\n--- in process ---\n%s", throughAPI, inProcess)
	}
}
