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

// startClosingStreamAPI serves every stream route with one event and then,
// when sendCloseFrame is set, the server's close frame.
func startClosingStreamAPI(t *testing.T, sendCloseFrame bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(responseWriter, `data: {"key":"desired/service/web","value":"x","timestamp":"2026-11-15T10:00:00Z","kind":"put","target":"web","action":"get","principal":"user:alice"}`+"\n\n")
		if sendCloseFrame {
			_, _ = io.WriteString(responseWriter, "event: closed\ndata: {\"reason\":\"access changed\"}\n\n")
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestStreamCommandsReportServerClose (M216): when the server ends a stream
// with its close frame, cca watch, events --follow and audit --follow print
// the event, then the reason on stderr, and exit 1; a stream that just ends
// exits 0 as before.
func TestStreamCommandsReportServerClose(t *testing.T) {
	streamCommands := [][]string{
		{"watch", "desired"},
		{"events", "--follow"},
		{"audit", "--follow"},
	}
	for _, commandArgs := range streamCommands {
		t.Run(strings.Join(commandArgs, " "), func(t *testing.T) {
			closingAPI := startClosingStreamAPI(t, true)
			environment := []string{"CCA_API=" + closingAPI.URL, "CCA_TOKEN=test-token", "HOME=" + t.TempDir()}
			stdoutContent, stderrContent, exitCode := runCCAWithEnv(t, environment, commandArgs...)
			if exitCode != 1 || !strings.Contains(stderrContent, "stream ended by the server: access changed") {
				t.Errorf("exit %d stderr %q, want 1 and the close reason", exitCode, stderrContent)
			}
			if strings.Contains(stdoutContent, "event: closed") {
				t.Errorf("the close frame was printed as an event:\n%s", stdoutContent)
			}
			if !strings.Contains(stdoutContent, "web") {
				t.Errorf("the event before the close frame was not printed:\n%s", stdoutContent)
			}

			plainEndAPI := startClosingStreamAPI(t, false)
			plainEnvironment := []string{"CCA_API=" + plainEndAPI.URL, "CCA_TOKEN=test-token", "HOME=" + t.TempDir()}
			if _, plainStderr, plainExit := runCCAWithEnv(t, plainEnvironment, commandArgs...); plainExit != 0 {
				t.Errorf("stream without a close frame: exit %d stderr %q, want 0", plainExit, plainStderr)
			}
		})
	}
}
