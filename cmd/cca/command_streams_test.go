// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamCapRefusal is the API's answer to a principal at its stream limit (M225).
const streamCapRefusal = `{"error":"too many open streams for user:alice (limit 32): close some cca watch / events --follow / audit --follow"}`

// TestStreamCommandsReportCapRefusal (M225 R-7): a stream refused with 429
// prints the server's message and exits 1 on every stream command, not the
// raw JSON body.
func TestStreamCommandsReportCapRefusal(t *testing.T) {
	refusingAPI := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(responseWriter, streamCapRefusal)
	}))
	t.Cleanup(refusingAPI.Close)
	for _, commandArgs := range [][]string{{"watch", "desired"}, {"events", "--follow"}, {"audit", "--follow"}} {
		t.Run(strings.Join(commandArgs, " "), func(t *testing.T) {
			environment := []string{"CCA_API=" + refusingAPI.URL, "CCA_TOKEN=test-token", "HOME=" + t.TempDir()}
			_, stderrContent, exitCode := runCCAWithEnv(t, environment, commandArgs...)
			if exitCode != 1 || !strings.Contains(stderrContent, "too many open streams for user:alice (limit 32)") || strings.Contains(stderrContent, `{"error"`) {
				t.Errorf("exit %d stderr %q, want 1 and the message without raw JSON", exitCode, stderrContent)
			}
		})
	}
}

// TestStreamsCommandListsOpenStreams (M225): cca streams prints who holds
// the open streams on the API server, most first, and the limit.
func TestStreamsCommandListsOpenStreams(t *testing.T) {
	streamsAPI := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/streams" {
			http.NotFound(responseWriter, request)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(responseWriter, `{"total":35,"limit":32,"principals":[{"principal":"user:alice","open_streams":32},{"principal":"user:local-admin","open_streams":3}]}`)
	}))
	t.Cleanup(streamsAPI.Close)
	environment := []string{"CCA_API=" + streamsAPI.URL, "CCA_TOKEN=test-token", "HOME=" + t.TempDir()}
	stdoutContent, stderrContent, exitCode := runCCAWithEnv(t, environment, "streams")
	if exitCode != 0 {
		t.Fatalf("exit %d stderr %q", exitCode, stderrContent)
	}
	aliceLine := strings.Index(stdoutContent, "user:alice")
	localAdminLine := strings.Index(stdoutContent, "user:local-admin")
	if aliceLine < 0 || localAdminLine < aliceLine || !strings.Contains(stdoutContent, "32") ||
		!strings.Contains(stdoutContent, "35 open streams") || !strings.Contains(stdoutContent, "limit 32 per principal") {
		t.Errorf("cca streams output:\n%s", stdoutContent)
	}
}
