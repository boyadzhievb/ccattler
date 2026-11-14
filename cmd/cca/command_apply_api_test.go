// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedAPIRequest is one request the fake API server received.
type recordedAPIRequest struct {
	method string // method is the HTTP method.
	path   string // path is the URL path without query.
	body   string // body is the raw request body.
}

// fakeAPIServer answers /api/apply with a fixed status and body and records
// every request, so tests can see exactly what the CLI sent.
type fakeAPIServer struct {
	server        *httptest.Server     // server is the running test server.
	mutex         sync.Mutex           // mutex guards requests.
	requests      []recordedAPIRequest // requests lists every received request in order.
	applyStatus   int                  // applyStatus is the HTTP status for /api/apply.
	applyResponse string               // applyResponse is the JSON body for /api/apply.
}

// startFakeAPIServer starts a fake API that answers /api/apply as given.
func startFakeAPIServer(t *testing.T, applyStatus int, applyResponse string) *fakeAPIServer {
	t.Helper()
	fakeServer := &fakeAPIServer{applyStatus: applyStatus, applyResponse: applyResponse}
	fakeServer.server = httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestBody, _ := io.ReadAll(request.Body)
		fakeServer.mutex.Lock()
		fakeServer.requests = append(fakeServer.requests, recordedAPIRequest{method: request.Method, path: request.URL.Path, body: string(requestBody)})
		fakeServer.mutex.Unlock()
		if request.URL.Path != "/api/apply" {
			http.NotFound(responseWriter, request)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(fakeServer.applyStatus)
		_, _ = io.WriteString(responseWriter, fakeServer.applyResponse)
	}))
	t.Cleanup(fakeServer.server.Close)
	return fakeServer
}

// address returns host:port of the fake API.
func (fakeServer *fakeAPIServer) address() string {
	return strings.TrimPrefix(fakeServer.server.URL, "http://")
}

// applyRequests returns the recorded POST /api/apply requests.
func (fakeServer *fakeAPIServer) applyRequests() []recordedAPIRequest {
	fakeServer.mutex.Lock()
	defer fakeServer.mutex.Unlock()
	var applyCalls []recordedAPIRequest
	for _, recorded := range fakeServer.requests {
		if recorded.method == http.MethodPost && recorded.path == "/api/apply" {
			applyCalls = append(applyCalls, recorded)
		}
	}
	return applyCalls
}

// unusedLocalAddress returns a 127.0.0.1 address nothing listens on.
func unusedLocalAddress(t *testing.T) string {
	t.Helper()
	probeListener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatal(listenError)
	}
	address := probeListener.Addr().String()
	_ = probeListener.Close()
	return address
}

// applyTestEnvironment points the CLI at the API and at an etcd endpoint that
// does not exist, with a fresh HOME so no user context leaks in.
func applyTestEnvironment(t *testing.T, apiAddress string) []string {
	return []string{"CCA_API=" + apiAddress, "CCA_TOKEN=test-admin", "CCA_ENDPOINTS=" + unusedLocalAddress(t), "HOME=" + t.TempDir()}
}

// webServiceConfiguration is a minimal valid configuration.
const webServiceConfiguration = `service web {
    image nginx:1.27
    instances 1
}
`

// TestApplyCommandUsesAPIEvenWithEtcdEndpoints runs cca apply on a host that
// has etcd endpoints configured: the configuration must reach the cluster only
// through POST /api/apply, and an API refusal must fail the command (spec
// api-route-authorization, "Configuration changes go through the API").
func TestApplyCommandUsesAPIEvenWithEtcdEndpoints(t *testing.T) {
	configurationPath := writeTempFile(t, t.TempDir(), "web.cca", webServiceConfiguration)

	acceptingAPI := startFakeAPIServer(t, http.StatusOK, `{"ok":true,"facts_set":4}`)
	_, acceptedStderr, acceptedExit := runCCAWithEnv(t, applyTestEnvironment(t, acceptingAPI.address()), "apply", configurationPath)
	if acceptedExit != 0 {
		t.Errorf("apply with etcd endpoints configured: exit %d, want 0\nstderr: %s", acceptedExit, acceptedStderr)
	}
	if applyCalls := acceptingAPI.applyRequests(); len(applyCalls) != 1 || !strings.Contains(applyCalls[0].body, "service web") {
		t.Errorf("want one POST /api/apply carrying the configuration, got %d calls %+v", len(applyCalls), applyCalls)
	}

	refusingAPI := startFakeAPIServer(t, http.StatusForbidden, `{"error":"forbidden: no workload.create grant"}`)
	_, refusedStderr, refusedExit := runCCAWithEnv(t, applyTestEnvironment(t, refusingAPI.address()), "apply", configurationPath)
	if refusedExit == 0 || !strings.Contains(refusedStderr, "forbidden") {
		t.Errorf("apply refused by the API: exit %d stderr %q, want non-zero and the API's forbidden error", refusedExit, refusedStderr)
	}
}

// TestApplyCommandSendsLocalFileContents applies a configuration with a config
// file read from a local path: the CLI must send the file's contents, so the
// server never reads a client path.
func TestApplyCommandSendsLocalFileContents(t *testing.T) {
	configurationDirectory := t.TempDir()
	localFilePath := writeTempFile(t, configurationDirectory, "app.conf", "hello-from-local-file\n")
	configurationPath := writeTempFile(t, configurationDirectory, "web.cca", `service web {
    image nginx:1.27
    instances 1
    config {
        file "/etc/web/app.conf" from "`+localFilePath+`"
    }
}
`)

	fakeAPI := startFakeAPIServer(t, http.StatusOK, `{"ok":true,"facts_set":5}`)
	_, stderrContent, exitCode := runCCAWithEnv(t, applyTestEnvironment(t, fakeAPI.address()), "apply", configurationPath)
	if exitCode != 0 {
		t.Fatalf("apply with a local config file: exit %d\nstderr: %s", exitCode, stderrContent)
	}
	applyCalls := fakeAPI.applyRequests()
	if len(applyCalls) != 1 {
		t.Fatalf("want one POST /api/apply, got %d", len(applyCalls))
	}
	var sentRequest struct {
		Files map[string]string `json:"files"`
	}
	if decodeError := json.Unmarshal([]byte(applyCalls[0].body), &sentRequest); decodeError != nil {
		t.Fatalf("apply body is not JSON: %v\n%s", decodeError, applyCalls[0].body)
	}
	if sentRequest.Files[localFilePath] != "hello-from-local-file\n" {
		t.Errorf("apply body files[%q] = %q, want the local file's contents", localFilePath, sentRequest.Files[localFilePath])
	}
}

// TestApplyCommandFailsWithoutAPIUnlessSimulate: with no reachable API the
// command fails naming the address instead of printing a simulated cluster;
// --simulate keeps the local simulation.
func TestApplyCommandFailsWithoutAPIUnlessSimulate(t *testing.T) {
	configurationPath := writeTempFile(t, t.TempDir(), "web.cca", webServiceConfiguration)
	unreachableAPIAddress := unusedLocalAddress(t)
	environment := []string{"CCA_API=" + unreachableAPIAddress, "CCA_TOKEN=test-admin", "HOME=" + t.TempDir()}

	stdoutContent, stderrContent, exitCode := runCCAWithEnv(t, environment, "apply", configurationPath)
	if exitCode == 0 || strings.Contains(stdoutContent, "simulated") {
		t.Errorf("apply without an API: exit %d, want non-zero and no simulation\nstdout: %s", exitCode, stdoutContent)
	}
	if !strings.Contains(stderrContent, unreachableAPIAddress) {
		t.Errorf("apply without an API: stderr %q does not name the API address %s", stderrContent, unreachableAPIAddress)
	}

	simulatedStdout, simulatedStderr, simulatedExit := runCCAWithEnv(t, environment, "apply", "--simulate", configurationPath)
	if simulatedExit != 0 || !strings.Contains(simulatedStdout, "simulated nodes") {
		t.Errorf("apply --simulate: exit %d, want 0 and a simulated cluster\nstdout: %s\nstderr: %s", simulatedExit, simulatedStdout, simulatedStderr)
	}
}
