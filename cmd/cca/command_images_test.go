// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/runtime"
)

// TestParseImagesCommandArgs verifies parsing of images subcommand flags.
func TestParseImagesCommandArgs(testContext *testing.T) {
	testCases := []struct {
		args               []string
		expectedSubcommand string
		expectedNodeID     string
		expectedImage      string
	}{
		{[]string{"list"}, "list", "", ""},
		{[]string{"list", "--node", "node-1"}, "list", "node-1", ""},
		{[]string{"pull", "nginx:1.28"}, "pull", "", "nginx:1.28"},
		{[]string{"pull", "nginx:1.28", "--node", "node-2"}, "pull", "node-2", "nginx:1.28"},
		{[]string{}, "", "", ""},
	}

	for _, testCase := range testCases {
		parsedConfig := parseImagesCommandArgs(testCase.args)
		if parsedConfig.subcommand != testCase.expectedSubcommand {
			testContext.Errorf("parseImagesCommandArgs(%v) subcommand = %q, want %q",
				testCase.args, parsedConfig.subcommand, testCase.expectedSubcommand)
		}
		if parsedConfig.nodeID != testCase.expectedNodeID {
			testContext.Errorf("parseImagesCommandArgs(%v) nodeID = %q, want %q",
				testCase.args, parsedConfig.nodeID, testCase.expectedNodeID)
		}
		if parsedConfig.imageReference != testCase.expectedImage {
			testContext.Errorf("parseImagesCommandArgs(%v) image = %q, want %q",
				testCase.args, parsedConfig.imageReference, testCase.expectedImage)
		}
	}
}

// TestExtractNodeIDFromDebugKey verifies node ID extraction from fact keys.
func TestExtractNodeIDFromDebugKey(testContext *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"observed/node/node-1/debug_address", "node-1"},
		{"observed/node/worker-abc/debug_address", "worker-abc"},
		{"observed/node//debug_address", ""},
		{"some/other/key", ""},
	}

	for _, testCase := range testCases {
		result := extractNodeIDFromDebugKey(testCase.input)
		if result != testCase.expected {
			testContext.Errorf("extractNodeIDFromDebugKey(%q) = %q, want %q",
				testCase.input, result, testCase.expected)
		}
	}
}

// TestDebugImagePullEndpoint verifies the POST /debug/images/pull endpoint
// via the agent debug server with a simulator runtime.
func TestDebugImagePullEndpoint(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()

	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatalf("failed to listen: %v", listenError)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	listenAddress := fmt.Sprintf("127.0.0.1:%d", port)
	debugServer := agent.NewDebugServer("test-node", listenAddress, simulatorRuntime)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	go func() {
		_ = debugServer.Start(serverCtx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialError := net.Dial("tcp", listenAddress)
		if dialError == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	pullRequest := agent.DebugImagePullRequest{ImageReference: "nginx:1.28"}
	requestBody, _ := json.Marshal(pullRequest)

	httpResponse, requestError := http.Post(
		"http://"+listenAddress+"/debug/images/pull",
		"application/json",
		bytes.NewReader(requestBody))
	if requestError != nil {
		testContext.Fatalf("pull request failed: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var pullResponse agent.DebugImagePullResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&pullResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if pullResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", pullResponse.NodeID)
	}
	if !pullResponse.Success {
		testContext.Errorf("expected success=true, got false (error: %s)", pullResponse.Error)
	}
	if pullResponse.ImageReference != "nginx:1.28" {
		testContext.Errorf("expected image=nginx:1.28, got %q", pullResponse.ImageReference)
	}

	if len(simulatorRuntime.PulledImages) != 1 || simulatorRuntime.PulledImages[0] != "nginx:1.28" {
		testContext.Errorf("expected PulledImages=[nginx:1.28], got %v", simulatorRuntime.PulledImages)
	}
}

// TestDebugImagePullEndpointMethodNotAllowed verifies GET is rejected.
func TestDebugImagePullEndpointMethodNotAllowed(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()

	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatalf("failed to listen: %v", listenError)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	listenAddress := fmt.Sprintf("127.0.0.1:%d", port)
	debugServer := agent.NewDebugServer("test-node", listenAddress, simulatorRuntime)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	go func() {
		_ = debugServer.Start(serverCtx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialError := net.Dial("tcp", listenAddress)
		if dialError == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	httpResponse, requestError := http.Get("http://" + listenAddress + "/debug/images/pull") //nolint:gosec // test only
	if requestError != nil {
		testContext.Fatalf("request failed: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusMethodNotAllowed {
		testContext.Errorf("expected 405, got %d", httpResponse.StatusCode)
	}
}
