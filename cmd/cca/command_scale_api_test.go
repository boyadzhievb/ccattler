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

// fakeScaleAPI answers POST /api/scale with a fixed status and body and keeps
// the request bodies it received.
type fakeScaleAPI struct {
	server         *httptest.Server // server is the running test server.
	mutex          sync.Mutex       // mutex guards receivedBodies.
	receivedBodies []string         // receivedBodies lists every /api/scale body in order.
}

// startFakeScaleAPI starts a fake API answering /api/scale with status and body.
func startFakeScaleAPI(t *testing.T, scaleStatus int, scaleResponse string) *fakeScaleAPI {
	t.Helper()
	fakeAPI := &fakeScaleAPI{}
	fakeAPI.server = httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/scale" {
			http.NotFound(responseWriter, request)
			return
		}
		requestBody, _ := io.ReadAll(request.Body)
		fakeAPI.mutex.Lock()
		fakeAPI.receivedBodies = append(fakeAPI.receivedBodies, string(requestBody))
		fakeAPI.mutex.Unlock()
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(scaleStatus)
		_, _ = io.WriteString(responseWriter, scaleResponse)
	}))
	t.Cleanup(fakeAPI.server.Close)
	return fakeAPI
}

// lastScaleRequest decodes the last /api/scale body; ok is false when none arrived.
func (fakeAPI *fakeScaleAPI) lastScaleRequest(t *testing.T) (map[string]any, bool) {
	t.Helper()
	fakeAPI.mutex.Lock()
	defer fakeAPI.mutex.Unlock()
	if len(fakeAPI.receivedBodies) == 0 {
		return nil, false
	}
	var decoded map[string]any
	if decodeError := json.Unmarshal([]byte(fakeAPI.receivedBodies[len(fakeAPI.receivedBodies)-1]), &decoded); decodeError != nil {
		t.Fatalf("scale body is not JSON: %v", decodeError)
	}
	return decoded, true
}

// TestScaleCommandSendsApproval: spec service-scaling, "Scripted scale above
// the limit". Without a terminal, cca scale sends an approval only from
// --approve-above-limit, prints how to approve when refused, and prints the
// server's warning on success.
func TestScaleCommandSendsApproval(t *testing.T) {
	refusingAPI := startFakeScaleAPI(t, http.StatusBadRequest,
		`{"error":"700 instances is above the approval limit of 600; resend with approved_instances 700 as cluster.admin"}`)
	_, stderrText, exitCode := runCCAWithEnv(t, applyTestEnvironment(t, strings.TrimPrefix(refusingAPI.server.URL, "http://")),
		"scale", "web", "700")
	if exitCode == 0 || !strings.Contains(stderrText, "--approve-above-limit 700") {
		t.Errorf("refused scale: exit %d, stderr %q; want non-zero and the --approve-above-limit 700 hint", exitCode, stderrText)
	}

	approvingAPI := startFakeScaleAPI(t, http.StatusOK,
		`{"ok":true,"service":"web","instances":700,"warning":"700 instances for web is at or above the warning threshold of 500"}`)
	stdoutText, stderrText, exitCode := runCCAWithEnv(t, applyTestEnvironment(t, strings.TrimPrefix(approvingAPI.server.URL, "http://")),
		"scale", "web", "700", "--approve-above-limit", "700")
	if exitCode != 0 {
		t.Fatalf("approved scale: exit %d, stderr %q", exitCode, stderrText)
	}
	sentRequest, received := approvingAPI.lastScaleRequest(t)
	if !received || sentRequest["approved_instances"] != float64(700) || sentRequest["instances"] != float64(700) {
		t.Errorf("approved scale sent %v (received=%v), want instances 700 and approved_instances 700", sentRequest, received)
	}
	if !strings.Contains(stdoutText+stderrText, "warning threshold of 500") {
		t.Errorf("server warning not printed: stdout %q stderr %q", stdoutText, stderrText)
	}
}

// largeServiceConfiguration declares one service at 700 instances, above the limit.
const largeServiceConfiguration = "service web {\n  image nginx:1.28\n  instances 700\n}\n"

// TestApplyCommandSendsApprovalAndPrintsWarnings: spec service-scaling, "Apply
// with approval". cca apply sends --approve-above-limit as approved_instances
// and prints every warning the server returns.
func TestApplyCommandSendsApprovalAndPrintsWarnings(t *testing.T) {
	configurationPath := writeTempFile(t, t.TempDir(), "web.cca", largeServiceConfiguration)
	acceptingAPI := startFakeAPIServer(t, http.StatusOK,
		`{"ok":true,"facts_set":5,"warnings":["700 instances for web is at or above the warning threshold of 500"]}`)
	stdoutText, stderrText, exitCode := runCCAWithEnv(t, applyTestEnvironment(t, acceptingAPI.address()),
		"apply", "--approve-above-limit", "700", configurationPath)
	if exitCode != 0 {
		t.Fatalf("approved apply: exit %d, stderr %q", exitCode, stderrText)
	}
	applyCalls := acceptingAPI.applyRequests()
	if len(applyCalls) != 1 || !strings.Contains(applyCalls[0].body, `"approved_instances":700`) {
		t.Errorf("apply sent %+v, want one request with approved_instances 700", applyCalls)
	}
	if !strings.Contains(stdoutText+stderrText, "warning threshold of 500") {
		t.Errorf("warning not printed: stdout %q stderr %q", stdoutText, stderrText)
	}
}

// TestApplySimulateAppliesInstanceLimit: cca apply --simulate refuses a count
// above the limit without --approve-above-limit and accepts it with the flag.
func TestApplySimulateAppliesInstanceLimit(t *testing.T) {
	configurationPath := writeTempFile(t, t.TempDir(), "web.cca", largeServiceConfiguration)
	environment := []string{"HOME=" + t.TempDir()}
	_, refusedStderr, refusedExit := runCCAWithEnv(t, environment, "apply", "--simulate", configurationPath)
	if refusedExit == 0 || !strings.Contains(refusedStderr, "--approve-above-limit 700") {
		t.Errorf("simulate without approval: exit %d stderr %q, want non-zero and the approval hint", refusedExit, refusedStderr)
	}
	approvedStdout, approvedStderr, approvedExit := runCCAWithEnv(t, environment,
		"apply", "--simulate", "--approve-above-limit", "700", configurationPath)
	if approvedExit != 0 || !strings.Contains(approvedStderr, "warning threshold of 500") {
		t.Errorf("simulate with approval: exit %d stdout %q stderr %q, want 0 and the warning", approvedExit, approvedStdout, approvedStderr)
	}
}
