// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeJoinTokenToken is the composite token the fake API hands out.
const fakeJoinTokenToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.cafingerprint"

// fakeJoinTokenAPI answers /api/node/join-token like the real route and
// records every request, or refuses everything with 403.
type fakeJoinTokenAPI struct {
	server   *httptest.Server     // server is the running test server.
	mutex    sync.Mutex           // mutex guards requests.
	requests []recordedAPIRequest // requests lists every received request in order.
	refuse   bool                 // refuse answers every request with 403.
}

// startFakeJoinTokenAPI starts the fake; refuse makes it answer 403.
func startFakeJoinTokenAPI(t *testing.T, refuse bool) *fakeJoinTokenAPI {
	t.Helper()
	fakeAPI := &fakeJoinTokenAPI{refuse: refuse}
	fakeAPI.server = httptest.NewServer(http.HandlerFunc(fakeAPI.serveJoinToken))
	t.Cleanup(fakeAPI.server.Close)
	return fakeAPI
}

// serveJoinToken records the request and answers it.
func (fakeAPI *fakeJoinTokenAPI) serveJoinToken(responseWriter http.ResponseWriter, request *http.Request) {
	requestBody, _ := io.ReadAll(request.Body)
	fakeAPI.mutex.Lock()
	fakeAPI.requests = append(fakeAPI.requests, recordedAPIRequest{method: request.Method, path: request.URL.RequestURI(), body: string(requestBody)})
	fakeAPI.mutex.Unlock()
	responseWriter.Header().Set("Content-Type", "application/json")
	switch {
	case request.URL.Path != "/api/node/join-token":
		http.NotFound(responseWriter, request)
	case fakeAPI.refuse:
		responseWriter.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(responseWriter, `{"error":"forbidden: no cluster.admin grant"}`)
	case request.Method == http.MethodPost:
		_, _ = io.WriteString(responseWriter, `{"token":"`+fakeJoinTokenToken+`","node_id":"worker-9","expires_at":"2030-01-01T00:15:00Z"}`)
	case request.Method == http.MethodGet:
		_, _ = io.WriteString(responseWriter, `[{"id":"01234567","node_id":"worker-9","expires_at":"2030-01-01T00:15:00Z"}]`)
	default:
		_, _ = io.WriteString(responseWriter, `{"ok":true}`)
	}
}

// lastRequest returns the most recent request, or an empty one.
func (fakeAPI *fakeJoinTokenAPI) lastRequest() recordedAPIRequest {
	fakeAPI.mutex.Lock()
	defer fakeAPI.mutex.Unlock()
	if len(fakeAPI.requests) == 0 {
		return recordedAPIRequest{}
	}
	return fakeAPI.requests[len(fakeAPI.requests)-1]
}

// tokenTestEnvironment points the CLI at the fake API and at an etcd that
// does not exist, so any direct etcd access fails the command.
func tokenTestEnvironment(t *testing.T, fakeAPI *fakeJoinTokenAPI) []string {
	return []string{"CCA_API=" + strings.TrimPrefix(fakeAPI.server.URL, "http://"), "CCA_TOKEN=test-admin",
		"CCA_ENDPOINTS=" + unusedLocalAddress(t), "HOME=" + t.TempDir()}
}

// TestTokenCommandsUseAPI: create keeps the "Token:" line the Ansible worker
// role parses, --quiet prints only the token, list prints ids, revoke sends
// the token in the body and never in the URL.
func TestTokenCommandsUseAPI(t *testing.T) {
	fakeAPI := startFakeJoinTokenAPI(t, false)
	environment := tokenTestEnvironment(t, fakeAPI)

	createStdout, createStderr, createExit := runCCAWithEnv(t, environment, "token", "create", "--node-id", "worker-9", "--ttl", "15m")
	if createExit != 0 || !strings.Contains(createStdout, "Token:   "+fakeJoinTokenToken+"\n") {
		t.Fatalf("token create: exit %d\nstdout: %s\nstderr: %s", createExit, createStdout, createStderr)
	}
	var sentCreate struct {
		NodeID string `json:"node_id"`
		TTL    string `json:"ttl"`
	}
	createRequest := fakeAPI.lastRequest()
	if decodeError := json.Unmarshal([]byte(createRequest.body), &sentCreate); decodeError != nil || createRequest.method != http.MethodPost ||
		sentCreate.NodeID != "worker-9" || sentCreate.TTL != "15m" {
		t.Errorf("token create sent %+v, want POST {node_id: worker-9, ttl: 15m}", createRequest)
	}

	quietStdout, _, quietExit := runCCAWithEnv(t, environment, "token", "create", "--quiet")
	if quietExit != 0 || quietStdout != fakeJoinTokenToken {
		t.Errorf("token create --quiet: exit %d stdout %q, want only the token", quietExit, quietStdout)
	}

	listStdout, _, listExit := runCCAWithEnv(t, environment, "token", "list")
	if listExit != 0 || !strings.Contains(listStdout, "01234567") || !strings.Contains(listStdout, "worker-9") {
		t.Errorf("token list: exit %d stdout %q, want the id and node", listExit, listStdout)
	}

	_, revokeStderr, revokeExit := runCCAWithEnv(t, environment, "token", "revoke", fakeJoinTokenToken)
	revokeRequest := fakeAPI.lastRequest()
	if revokeExit != 0 || revokeRequest.method != http.MethodDelete || !strings.Contains(revokeRequest.body, fakeJoinTokenToken) ||
		strings.Contains(revokeRequest.path, "0123456789abcdef") {
		t.Errorf("token revoke: exit %d stderr %q, request %+v; want DELETE with the token in the body only", revokeExit, revokeStderr, revokeRequest)
	}
}

// TestTokenCommandFailsWhenAPIRefuses: a 403 fails the command with the
// server's message, and the removed etcd flags are refused.
func TestTokenCommandFailsWhenAPIRefuses(t *testing.T) {
	fakeAPI := startFakeJoinTokenAPI(t, true)
	environment := tokenTestEnvironment(t, fakeAPI)

	_, refusedStderr, refusedExit := runCCAWithEnv(t, environment, "token", "create", "--node-id", "worker-9")
	if refusedExit == 0 || !strings.Contains(refusedStderr, "cluster.admin") {
		t.Errorf("token create refused by the API: exit %d stderr %q, want non-zero and the server's message", refusedExit, refusedStderr)
	}
	_, flagStderr, flagExit := runCCAWithEnv(t, environment, "token", "create", "--endpoints", "localhost:2379")
	if flagExit == 0 || !strings.Contains(flagStderr, "--endpoints") {
		t.Errorf("token create --endpoints: exit %d stderr %q, want the removed flag refused", flagExit, flagStderr)
	}
}
