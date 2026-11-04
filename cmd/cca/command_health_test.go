// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchControlPlaneHealthReportUnhealthyStore checks that a 503 response
// is decoded as a report (not an error) and printed with the store message.
func TestFetchControlPlaneHealthReportUnhealthyStore(testContext *testing.T) {
	healthServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			testContext.Errorf("unexpected path %s", request.URL.Path)
		}
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
		_, _ = responseWriter.Write([]byte(`{"status":"unhealthy","mode":"full","checks":[` +
			`{"name":"store","status":"unhealthy","message":"etcdserver: request timed out"},` +
			`{"name":"controllers","status":"healthy"}]}`))
	}))
	defer healthServer.Close()

	healthReport, fetchError := fetchControlPlaneHealthReport(healthServer.Client(), healthServer.URL)
	if fetchError != nil {
		testContext.Fatalf("fetch: %v", fetchError)
	}
	if healthReport.Status != "unhealthy" || len(healthReport.Checks) != 2 {
		testContext.Fatalf("unexpected report: %+v", healthReport)
	}

	var outputBuffer bytes.Buffer
	writeControlPlaneHealthReport(&outputBuffer, healthReport)
	printedOutput := outputBuffer.String()
	for _, expectedFragment := range []string{"etcdserver: request timed out", "controllers  healthy", "overall      unhealthy (mode full)"} {
		if !strings.Contains(printedOutput, expectedFragment) {
			testContext.Errorf("output missing %q:\n%s", expectedFragment, printedOutput)
		}
	}
}

// TestFetchControlPlaneHealthReportUnreachable checks that a closed server is
// reported as an error naming the API address.
func TestFetchControlPlaneHealthReportUnreachable(testContext *testing.T) {
	healthServer := httptest.NewServer(http.NotFoundHandler())
	closedServerURL := healthServer.URL
	healthServer.Close()

	_, fetchError := fetchControlPlaneHealthReport(http.DefaultClient, closedServerURL)
	if fetchError == nil || !strings.Contains(fetchError.Error(), closedServerURL) {
		testContext.Fatalf("expected unreachable error naming %s, got %v", closedServerURL, fetchError)
	}
}
