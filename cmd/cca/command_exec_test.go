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

// TestParseExecArgs verifies parsing of exec arguments with -- separator.
func TestParseExecArgs(testContext *testing.T) {
	testCases := []struct {
		args             []string
		expectedInstance string
		expectedCommand  []string
	}{
		{[]string{"inst-1", "--", "ls", "-la"}, "inst-1", []string{"ls", "-la"}},
		{[]string{"inst-2", "--", "echo", "hello"}, "inst-2", []string{"echo", "hello"}},
		{[]string{"inst-3"}, "inst-3", nil},
		{[]string{}, "", nil},
		{[]string{"inst-4", "--"}, "inst-4", []string{}},
	}

	for _, testCase := range testCases {
		instanceID, commandParts := parseExecArgs(testCase.args)
		if instanceID != testCase.expectedInstance {
			testContext.Errorf("parseExecArgs(%v) instance = %q, want %q",
				testCase.args, instanceID, testCase.expectedInstance)
		}
		if len(commandParts) != len(testCase.expectedCommand) {
			testContext.Errorf("parseExecArgs(%v) command len = %d, want %d",
				testCase.args, len(commandParts), len(testCase.expectedCommand))
			continue
		}
		for index := range commandParts {
			if commandParts[index] != testCase.expectedCommand[index] {
				testContext.Errorf("parseExecArgs(%v) command[%d] = %q, want %q",
					testCase.args, index, commandParts[index], testCase.expectedCommand[index])
			}
		}
	}
}

// TestDebugExecEndpoint verifies the POST /debug/exec endpoint via the
// agent debug server with a simulator runtime.
func TestDebugExecEndpoint(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()
	simulatorRuntime.Start(ctx, runtime.Spec{ID: "exec-test", ServiceName: "web", Image: "nginx:1.28"})

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

	execRequest := agent.DebugExecRequest{
		InstanceID: "exec-test",
		Command:    "echo hello",
	}
	requestBody, _ := json.Marshal(execRequest)

	httpResponse, requestError := http.Post(
		"http://"+listenAddress+"/debug/exec",
		"application/json",
		bytes.NewReader(requestBody))
	if requestError != nil {
		testContext.Fatalf("exec request failed: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var execResponse agent.DebugExecResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&execResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if execResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", execResponse.NodeID)
	}
	if execResponse.InstanceID != "exec-test" {
		testContext.Errorf("expected instance_id=exec-test, got %q", execResponse.InstanceID)
	}
	if execResponse.Error != "" {
		testContext.Errorf("expected no error, got %q", execResponse.Error)
	}
	if execResponse.Output == "" {
		testContext.Error("expected non-empty output from simulator exec")
	}
}

// TestDebugExecEndpointNotFound verifies exec returns an error for unknown instances.
func TestDebugExecEndpointNotFound(testContext *testing.T) {
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

	execRequest := agent.DebugExecRequest{
		InstanceID: "nonexistent",
		Command:    "echo hello",
	}
	requestBody, _ := json.Marshal(execRequest)

	httpResponse, requestError := http.Post(
		"http://"+listenAddress+"/debug/exec",
		"application/json",
		bytes.NewReader(requestBody))
	if requestError != nil {
		testContext.Fatalf("exec request failed: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var execResponse agent.DebugExecResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&execResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if execResponse.Error == "" {
		testContext.Error("expected error for nonexistent instance")
	}
	if execResponse.ExitCode != 1 {
		testContext.Errorf("expected exit_code=1, got %d", execResponse.ExitCode)
	}
}
