// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_health.go implements `cca health`, which reports control-plane
// health (fact store reachability, controller leader lease) from the API
// server's /healthz endpoint. It replaces `etcdctl endpoint health`.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// healthStatusHealthy is the overall and per-check status the API server
// reports when a component is working.
const healthStatusHealthy = "healthy"

// controlPlaneHealthCheck is one entry of the /healthz checks list.
type controlPlaneHealthCheck struct {
	Name    string `json:"name"`              // Name is the checked component: store or controllers.
	Status  string `json:"status"`            // Status is healthy, unhealthy, or unknown.
	Message string `json:"message,omitempty"` // Message explains a non-healthy status.
}

// controlPlaneHealthReport is the decoded /healthz response body.
type controlPlaneHealthReport struct {
	Status string                    `json:"status"` // Status is the overall result: healthy or unhealthy.
	Mode   string                    `json:"mode"`   // Mode is the API server mode: full or api-only.
	Checks []controlPlaneHealthCheck `json:"checks"` // Checks lists each component's result.
}

// executeHealthCommand prints the control-plane health report and exits 1
// when the API server is unreachable or reports unhealthy.
func executeHealthCommand() {
	healthReport, fetchError := fetchControlPlaneHealthReport(buildAuthenticatedHTTPClient(), statusAPIBaseURL())
	if fetchError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", fetchError)
		os.Exit(1)
	}
	writeControlPlaneHealthReport(os.Stdout, healthReport)
	if healthReport.Status != healthStatusHealthy {
		os.Exit(1)
	}
}

// fetchControlPlaneHealthReport requests /healthz and decodes the report. A
// 503 response still carries a report body, so only transport and decode
// failures are errors.
func fetchControlPlaneHealthReport(httpClient *http.Client, apiBaseURL string) (controlPlaneHealthReport, error) {
	var healthReport controlPlaneHealthReport
	httpResponse, requestError := httpClient.Get(apiBaseURL + "/healthz") //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		return healthReport, fmt.Errorf("cannot reach API server at %s: %w", apiBaseURL, requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&healthReport); decodeError != nil {
		return healthReport, fmt.Errorf("decode health response (HTTP %d): %w", httpResponse.StatusCode, decodeError)
	}
	return healthReport, nil
}

// writeControlPlaneHealthReport prints one line per check followed by the
// overall status and server mode.
func writeControlPlaneHealthReport(outputWriter io.Writer, healthReport controlPlaneHealthReport) {
	for _, healthCheck := range healthReport.Checks {
		if healthCheck.Message != "" {
			_, _ = fmt.Fprintf(outputWriter, "%-12s %-10s %s\n", healthCheck.Name, healthCheck.Status, healthCheck.Message)
			continue
		}
		_, _ = fmt.Fprintf(outputWriter, "%-12s %s\n", healthCheck.Name, healthCheck.Status)
	}
	_, _ = fmt.Fprintf(outputWriter, "overall      %s (mode %s)\n", healthReport.Status, healthReport.Mode)
}
