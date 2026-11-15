// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// fetchClusterStatus GETs /api/status (with the given query) and decodes it.
func fetchClusterStatus(testingContext *testing.T, baseURL, query string) ClusterStatus {
	testingContext.Helper()
	response, requestError := http.Get(baseURL + "/api/status" + query)
	if requestError != nil {
		testingContext.Fatal(requestError)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		testingContext.Fatalf("GET /api/status%s: got %d, want 200", query, response.StatusCode)
	}
	var status ClusterStatus
	if decodeError := json.NewDecoder(response.Body).Decode(&status); decodeError != nil {
		testingContext.Fatal(decodeError)
	}
	return status
}

// statusHasService reports whether the status lists a service by name.
func statusHasService(status ClusterStatus, serviceName string) bool {
	for _, service := range status.Services {
		if service.Name == serviceName {
			return true
		}
	}
	return false
}

// TestStatusShowsApplyImmediately: after a successful apply, the next status
// read shows the change, even if a status answer was cached just before.
func TestStatusShowsApplyImmediately(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	if statusHasService(fetchClusterStatus(t, baseURL, ""), "fresh") {
		t.Fatal("service fresh present before apply")
	}
	dslConfig := "service fresh {\n  image nginx:1.27\n  instances 1\n}"
	response, applyError := http.Post(baseURL+"/api/apply", "text/plain", strings.NewReader(dslConfig))
	if applyError != nil {
		t.Fatal(applyError)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("apply: got %d, want 200", response.StatusCode)
	}

	if !statusHasService(fetchClusterStatus(t, baseURL, ""), "fresh") {
		t.Fatal("status after apply does not show service fresh (stale cache)")
	}
}

// TestStatusAllInstancesNotCachedAsDefault: a ?all=true answer (with stopped
// instances) must not be served to a later plain status request.
func TestStatusAllInstancesNotCachedAsDefault(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	stoppedInstance := types.Instance{ID: "web-stopped", Service: "web", State: types.InstanceStopped}
	if writeError := types.WriteInstance(ctx, factStore, stoppedInstance); writeError != nil {
		t.Fatal(writeError)
	}

	if len(fetchClusterStatus(t, baseURL, "?all=true").Instances) != 1 {
		t.Fatal("?all=true should list the stopped instance")
	}
	if instances := fetchClusterStatus(t, baseURL, "").Instances; len(instances) != 0 {
		t.Fatalf("plain status lists %d instances, want 0 (got the cached ?all=true answer)", len(instances))
	}
}
