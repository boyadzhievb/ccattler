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
