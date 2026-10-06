// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
)

// findAvailablePort returns a free TCP port on localhost.
func findAvailablePort(testContext *testing.T) int {
	testContext.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		testContext.Fatalf("failed to find available port: %v", listenError)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// startTestDebugServer creates and starts a DebugServer on a random port,
// returning the base URL and a cancel function.
func startTestDebugServer(testContext *testing.T, runtimeAdapter runtime.Runtime) (string, context.CancelFunc) {
	testContext.Helper()
	port := findAvailablePort(testContext)
	listenAddress := fmt.Sprintf("127.0.0.1:%d", port)
	baseURL := fmt.Sprintf("http://%s", listenAddress)

	ctx, cancel := context.WithCancel(context.Background())
	debugServer := NewDebugServer("test-node", listenAddress, runtimeAdapter)

	go func() {
		_ = debugServer.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialError := net.Dial("tcp", listenAddress)
		if dialError == nil {
			conn.Close()
			return baseURL, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	testContext.Fatalf("debug server did not start within 2s")
	return "", nil
}

func TestDebugContainersEmpty(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/containers")
	if requestError != nil {
		testContext.Fatalf("GET /debug/containers failed: %v", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		testContext.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var containerResponse DebugContainersResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&containerResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if containerResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", containerResponse.NodeID)
	}
	if containerResponse.Count != 0 {
		testContext.Errorf("expected count=0, got %d", containerResponse.Count)
	}
	if len(containerResponse.Containers) != 0 {
		testContext.Errorf("expected empty containers, got %d", len(containerResponse.Containers))
	}
}

func TestDebugContainersWithRunningWorkloads(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	simulatorRuntime.Start(ctx, runtime.Spec{ID: "instance-aaa", ServiceName: "web", Image: "nginx:1.28"})
	simulatorRuntime.Start(ctx, runtime.Spec{ID: "instance-bbb", ServiceName: "api", Image: "myapi:v3"})

	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/containers")
	if requestError != nil {
		testContext.Fatalf("GET /debug/containers failed: %v", requestError)
	}
	defer response.Body.Close()

	var containerResponse DebugContainersResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&containerResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if containerResponse.Count != 2 {
		testContext.Fatalf("expected count=2, got %d", containerResponse.Count)
	}
	if containerResponse.Containers[0].InstanceID != "instance-aaa" {
		testContext.Errorf("expected first container instance-aaa, got %q", containerResponse.Containers[0].InstanceID)
	}
	if !containerResponse.Containers[0].Running {
		testContext.Errorf("expected instance-aaa to be running")
	}
	if containerResponse.Containers[1].InstanceID != "instance-bbb" {
		testContext.Errorf("expected second container instance-bbb, got %q", containerResponse.Containers[1].InstanceID)
	}
}

func TestDebugImagesEmptyForSimulator(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/images")
	if requestError != nil {
		testContext.Fatalf("GET /debug/images failed: %v", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		testContext.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var imagesResponse DebugImagesResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&imagesResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if imagesResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", imagesResponse.NodeID)
	}
	if imagesResponse.Count != 0 {
		testContext.Errorf("expected count=0 for simulator runtime, got %d", imagesResponse.Count)
	}
}

func TestDebugStatsEmpty(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/stats")
	if requestError != nil {
		testContext.Fatalf("GET /debug/stats failed: %v", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		testContext.Fatalf("expected 200, got %d", response.StatusCode)
	}

	var statsResponse DebugStatsResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&statsResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if statsResponse.NodeID != "test-node" {
		testContext.Errorf("expected node_id=test-node, got %q", statsResponse.NodeID)
	}
	if statsResponse.WorkloadCount != 0 {
		testContext.Errorf("expected workload_count=0, got %d", statsResponse.WorkloadCount)
	}
	if statsResponse.RunningCount != 0 {
		testContext.Errorf("expected running_count=0, got %d", statsResponse.RunningCount)
	}
}

func TestDebugStatsWithWorkloads(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	simulatorRuntime.Start(ctx, runtime.Spec{ID: "instance-aaa", ServiceName: "web", Image: "nginx:1.28"})
	simulatorRuntime.Start(ctx, runtime.Spec{ID: "instance-bbb", ServiceName: "api", Image: "myapi:v3"})

	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/stats")
	if requestError != nil {
		testContext.Fatalf("GET /debug/stats failed: %v", requestError)
	}
	defer response.Body.Close()

	var statsResponse DebugStatsResponse
	if decodeError := json.NewDecoder(response.Body).Decode(&statsResponse); decodeError != nil {
		testContext.Fatalf("decode error: %v", decodeError)
	}

	if statsResponse.WorkloadCount != 2 {
		testContext.Errorf("expected workload_count=2, got %d", statsResponse.WorkloadCount)
	}
	if statsResponse.RunningCount != 2 {
		testContext.Errorf("expected running_count=2, got %d", statsResponse.RunningCount)
	}
}

func TestDebugContainersMethodNotAllowed(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Post(baseURL+"/debug/containers", "application/json", nil)
	if requestError != nil {
		testContext.Fatalf("POST /debug/containers failed: %v", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusMethodNotAllowed {
		testContext.Errorf("expected 405, got %d", response.StatusCode)
	}
}

func TestDebugContainersContentType(testContext *testing.T) {
	simulatorRuntime := runtime.NewSimulatorRuntime()
	baseURL, cancel := startTestDebugServer(testContext, simulatorRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/containers")
	if requestError != nil {
		testContext.Fatalf("GET /debug/containers failed: %v", requestError)
	}
	defer response.Body.Close()

	contentType := response.Header.Get("Content-Type")
	if contentType != "application/json" {
		testContext.Errorf("expected Content-Type=application/json, got %q", contentType)
	}
}
