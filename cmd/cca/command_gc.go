// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Garbage collection CLI commands.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// gcResponse mirrors the JSON body returned by POST /api/gc.
type gcResponse struct {
	Deleted     int      `json:"deleted"`
	InstanceIDs []string `json:"instance_ids"`
}

// executeGCCommand dispatches `cca gc <subcommand>`.
func executeGCCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca gc instances")
		os.Exit(1)
	}
	switch args[0] {
	case "instances":
		executeGCInstances()
	default:
		fmt.Fprintf(os.Stderr, "unknown gc target: %s\nusage: cca gc instances\n", args[0])
		os.Exit(1)
	}
}

// executeGCInstances calls POST /api/gc to garbage-collect stopped instance
// facts from the store, then prints the results.
func executeGCInstances() {
	requestURL := fmt.Sprintf("%s/api/gc", statusAPIBaseURL())
	httpResponse, requestError := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", nil) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "gc failed (%d): %s\n", httpResponse.StatusCode, string(responseBody))
		os.Exit(1)
	}

	var gcResult gcResponse
	if unmarshalError := json.Unmarshal(responseBody, &gcResult); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "invalid response: %s\n", string(responseBody))
		os.Exit(1)
	}

	if gcResult.Deleted == 0 {
		fmt.Println("No stopped instances to collect.")
		return
	}

	fmt.Printf("Collected %d stopped instance(s):\n", gcResult.Deleted)
	for _, instanceID := range gcResult.InstanceIDs {
		fmt.Printf("  %s\n", instanceID)
	}
}
