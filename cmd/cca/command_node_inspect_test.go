// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
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

func TestFormatBytesHuman(testContext *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KiB"},
		{1536, "1.5KiB"},
		{1048576, "1.0MiB"},
		{1073741824, "1.0GiB"},
		{2684354560, "2.5GiB"},
	}

	for _, testCase := range tests {
		result := formatBytesHuman(testCase.input)
		if result != testCase.expected {
			testContext.Errorf("formatBytesHuman(%d) = %q, want %q", testCase.input, result, testCase.expected)
		}
	}
}

func TestBuildPublishableDebugAddress(testContext *testing.T) {
	tests := []struct {
		debugListen      string
		advertiseAddress string
		expected         string
	}{
		{"127.0.0.1:9771", "", "127.0.0.1:9771"},
		{"127.0.0.1:9771", "192.168.1.10", "192.168.1.10:9771"},
		{"0.0.0.0:9771", "10.0.0.5", "10.0.0.5:9771"},
		{"192.168.1.10:9771", "10.0.0.5", "192.168.1.10:9771"},
		{"bad-address", "10.0.0.5", "bad-address"},
	}

	for _, testCase := range tests {
		result := buildPublishableDebugAddress(testCase.debugListen, testCase.advertiseAddress)
		if result != testCase.expected {
			testContext.Errorf("buildPublishableDebugAddress(%q, %q) = %q, want %q",
				testCase.debugListen, testCase.advertiseAddress, result, testCase.expected)
		}
	}
}

func TestQueryAgentDebugEndpoint(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()
	simulatorRuntime.Start(ctx, runtime.Spec{ID: "test-instance", ServiceName: "web", Image: "nginx:1.28"})

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

	responseBody := queryAgentDebugEndpoint(listenAddress, "/debug/containers")

	var containerResponse agent.DebugContainersResponse
	if decodeError := json.Unmarshal(responseBody, &containerResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if containerResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", containerResponse.NodeID)
	}
	if containerResponse.Count != 1 {
		testContext.Errorf("expected count=1, got %d", containerResponse.Count)
	}
	if len(containerResponse.Containers) > 0 && containerResponse.Containers[0].InstanceID != "test-instance" {
		testContext.Errorf("expected instance test-instance, got %q", containerResponse.Containers[0].InstanceID)
	}

	statsBody := queryAgentDebugEndpoint(listenAddress, "/debug/stats")
	var statsResponse agent.DebugStatsResponse
	if decodeError := json.Unmarshal(statsBody, &statsResponse); decodeError != nil {
		testContext.Fatalf("decode stats error: %v", decodeError)
	}
	if statsResponse.RunningCount != 1 {
		testContext.Errorf("expected running_count=1, got %d", statsResponse.RunningCount)
	}

	imagesBody := queryAgentDebugEndpoint(listenAddress, "/debug/images")
	var imagesResponse agent.DebugImagesResponse
	if decodeError := json.Unmarshal(imagesBody, &imagesResponse); decodeError != nil {
		testContext.Fatalf("decode images error: %v", decodeError)
	}
	if imagesResponse.Count != 0 {
		testContext.Errorf("expected count=0 for simulator, got %d", imagesResponse.Count)
	}
}

func TestQueryAgentDebugEndpointConnectionFailure(testContext *testing.T) {
	// queryAgentDebugEndpoint calls os.Exit on failure, so we can't test it
	// directly in-process. Instead, verify the HTTP request would fail.
	_, requestError := http.Get("http://127.0.0.1:1/debug/containers") //nolint:gosec // test only
	if requestError == nil {
		testContext.Error("expected connection error to port 1")
	}
}
