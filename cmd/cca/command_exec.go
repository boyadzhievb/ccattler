// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/boyadzhievb/ccattler/agent"
)

// executeExecCommand runs a command inside a container instance by discovering
// which node hosts the instance, then relaying the exec request through the
// agent's debug API. The command arguments follow a "--" separator.
func executeExecCommand(instanceID string, commandParts []string) {
	command := strings.Join(commandParts, " ")
	if command == "" {
		fmt.Fprintln(os.Stderr, "error: no command specified after --")
		os.Exit(1)
	}

	placementNodeID := discoverInstancePlacement(instanceID)
	debugAddress := discoverNodeDebugAddress(placementNodeID)

	execRequest := agent.DebugExecRequest{
		InstanceID: instanceID,
		Command:    command,
	}
	requestBody, marshalError := json.Marshal(execRequest)
	if marshalError != nil {
		fmt.Fprintf(os.Stderr, "error encoding request: %v\n", marshalError)
		os.Exit(1)
	}

	requestURL := "http://" + debugAddress + "/debug/exec"
	if strings.HasPrefix(debugAddress, "http://") || strings.HasPrefix(debugAddress, "https://") {
		requestURL = debugAddress + "/debug/exec"
	}

	httpResponse, requestError := http.Post(requestURL, "application/json", bytes.NewReader(requestBody)) //nolint:gosec // CLI connects to agent debug API
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to agent debug API at %s: %v\n", debugAddress, requestError)
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var execResponse agent.DebugExecResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&execResponse); decodeError != nil {
		fmt.Fprintf(os.Stderr, "error decoding response: %v\n", decodeError)
		os.Exit(1)
	}

	if execResponse.Output != "" {
		fmt.Print(execResponse.Output)
	}

	if execResponse.Error != "" {
		fmt.Fprintf(os.Stderr, "exec error: %s\n", execResponse.Error)
		os.Exit(execResponse.ExitCode)
	}
}

// discoverInstancePlacement reads the placement fact for the given instance
// from the control plane API and returns the node ID where it is placed.
func discoverInstancePlacement(instanceID string) string {
	factKey := "placement/instance/" + instanceID
	apiURL := "http://" + statusAPIListenAddress + "/api/state?prefix=" + factKey
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var facts []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}

	for _, factEntry := range facts {
		if factEntry.Key == factKey && factEntry.Value != "" {
			return factEntry.Value
		}
	}

	fmt.Fprintf(os.Stderr, "instance %q has no placement — is it scheduled?\n", instanceID)
	os.Exit(1)
	return ""
}

// parseExecArgs splits arguments into instanceID and command parts,
// separated by "--". Returns the instance ID and everything after "--".
func parseExecArgs(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	instanceID := args[0]
	for index, argument := range args[1:] {
		if argument == "--" {
			return instanceID, args[index+2:]
		}
	}
	return instanceID, nil
}
