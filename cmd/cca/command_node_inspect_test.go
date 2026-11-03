// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
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

// startTestDebugProxy starts a loopback debug server for nodeID and an API
// server that proxies to it (Flow 24), and points the CLI at that API server.
// The API server uses the demo admin authenticator.
func startTestDebugProxy(testContext *testing.T, nodeID string, runtimeAdapter runtime.Runtime) {
	testContext.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatalf("failed to listen: %v", listenError)
	}
	listenAddress := listener.Addr().String()
	listener.Close()

	debugServer := agent.NewDebugServer(nodeID, listenAddress, runtimeAdapter, "")
	serverCtx, serverCancel := context.WithCancel(context.Background())
	testContext.Cleanup(serverCancel)
	go func() { _ = debugServer.Start(serverCtx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, dialError := net.Dial("tcp", listenAddress); dialError == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	factStore := store.NewMemoryStore()
	testContext.Cleanup(func() { factStore.Close() })
	if _, putError := factStore.Put(context.Background(), types.KeyObservedNodeDebugAddress(nodeID), []byte("http://"+listenAddress)); putError != nil {
		testContext.Fatalf("publish debug address: %v", putError)
	}
	apiServer := api.NewServer(factStore)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&demoAlwaysAdminAuthenticator{}))
	demoAuthorizer := security.NewAPIAuthorizer()
	demoAuthorizer.Grant("user:local-demo", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(demoAuthorizer)
	apiTestServer := httptest.NewServer(apiServer.Handler())
	testContext.Cleanup(apiTestServer.Close)

	previousScheme, previousAddress := statusAPIScheme, statusAPIListenAddress
	statusAPIScheme, statusAPIListenAddress = "http", strings.TrimPrefix(apiTestServer.URL, "http://")
	testContext.Cleanup(func() { statusAPIScheme, statusAPIListenAddress = previousScheme, previousAddress })
}

func TestQueryNodeDebugEndpointThroughProxy(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.Start(context.Background(), runtime.Spec{ID: "test-instance", ServiceName: "web", Image: "nginx:1.28"})
	startTestDebugProxy(testContext, "test-node", simulatorRuntime)

	responseBody := queryNodeDebugEndpoint("test-node", "containers")

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

	statsBody := queryNodeDebugEndpoint("test-node", "stats")
	var statsResponse agent.DebugStatsResponse
	if decodeError := json.Unmarshal(statsBody, &statsResponse); decodeError != nil {
		testContext.Fatalf("decode stats error: %v", decodeError)
	}
	if statsResponse.RunningCount != 1 {
		testContext.Errorf("expected running_count=1, got %d", statsResponse.RunningCount)
	}

	imagesBody := queryNodeDebugEndpoint("test-node", "images")
	var imagesResponse agent.DebugImagesResponse
	if decodeError := json.Unmarshal(imagesBody, &imagesResponse); decodeError != nil {
		testContext.Fatalf("decode images error: %v", decodeError)
	}
	if imagesResponse.Count != 0 {
		testContext.Errorf("expected count=0 for simulator, got %d", imagesResponse.Count)
	}
}

func TestPostNodeDebugEndpointThroughProxy(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.Start(context.Background(), runtime.Spec{ID: "exec-test", ServiceName: "web", Image: "nginx:1.28"})
	startTestDebugProxy(testContext, "test-node", simulatorRuntime)

	requestBody, _ := json.Marshal(agent.DebugExecRequest{InstanceID: "exec-test", Command: "echo hello"})
	httpResponse, requestError := postNodeDebugEndpoint("test-node", "exec", requestBody)
	if requestError != nil {
		testContext.Fatalf("exec through proxy: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	var execResponse agent.DebugExecResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&execResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}
	if execResponse.InstanceID != "exec-test" || execResponse.Output == "" {
		testContext.Errorf("unexpected exec response through proxy: %+v", execResponse)
	}
}

func TestBuildPublishedDebugURL(testContext *testing.T) {
	testCases := []struct {
		debugListen      string
		advertiseAddress string
		tlsEnabled       bool
		expected         string
	}{
		{"0.0.0.0:9771", "192.168.1.10", true, "https://192.168.1.10:9771"},
		{"192.168.1.10:9771", "", true, "https://192.168.1.10:9771"},
		{"127.0.0.1:9771", "192.168.1.10", false, "http://127.0.0.1:9771"},
	}
	for _, testCase := range testCases {
		if published := buildPublishedDebugURL(testCase.debugListen, testCase.advertiseAddress, testCase.tlsEnabled); published != testCase.expected {
			testContext.Errorf("buildPublishedDebugURL(%q, %q, %v) = %q, want %q",
				testCase.debugListen, testCase.advertiseAddress, testCase.tlsEnabled, published, testCase.expected)
		}
	}
}
