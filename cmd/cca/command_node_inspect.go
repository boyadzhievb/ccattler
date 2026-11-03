// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/boyadzhievb/ccattler/agent"
)

// executeNodeInspectCommand queries a node agent's debug API through the API
// server proxy and displays live runtime containers, images, and resource usage.
func executeNodeInspectCommand(nodeID string) {
	fmt.Printf("Inspecting node %s\n\n", nodeID)

	displayNodeContainers(nodeID)
	displayNodeStats(nodeID)
	displayNodeImages(nodeID)
}

// displayNodeContainers fetches and prints the container list from the agent's
// debug API in a tabular format.
func displayNodeContainers(nodeID string) {
	responseBody := queryNodeDebugEndpoint(nodeID, "containers")

	var containerResponse agent.DebugContainersResponse
	if decodeError := json.Unmarshal(responseBody, &containerResponse); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode containers: %v\n", decodeError)
		return
	}

	fmt.Printf("CONTAINERS (%d)\n", containerResponse.Count)
	if containerResponse.Count == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-24s  %-10s  %-8s  %-12s  %s\n", "INSTANCE", "STATE", "PID", "CPU", "MEMORY")
		for _, container := range containerResponse.Containers {
			stateDisplay := "stopped"
			if container.Running {
				stateDisplay = "running"
			}
			pidDisplay := "-"
			if container.PID > 0 {
				pidDisplay = fmt.Sprintf("%d", container.PID)
			}
			cpuDisplay := "-"
			if container.CPUMillicores > 0 {
				cpuDisplay = fmt.Sprintf("%dm", container.CPUMillicores)
			}
			memoryDisplay := "-"
			if container.MemoryBytes > 0 {
				memoryDisplay = formatBytesHuman(container.MemoryBytes)
			}
			fmt.Printf("  %-24s  %-10s  %-8s  %-12s  %s\n",
				container.InstanceID, stateDisplay, pidDisplay, cpuDisplay, memoryDisplay)
		}
	}
	fmt.Println()
}

// displayNodeStats fetches and prints aggregate resource usage from the agent's
// debug API.
func displayNodeStats(nodeID string) {
	responseBody := queryNodeDebugEndpoint(nodeID, "stats")

	var statsResponse agent.DebugStatsResponse
	if decodeError := json.Unmarshal(responseBody, &statsResponse); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode stats: %v\n", decodeError)
		return
	}

	fmt.Println("RESOURCE USAGE")
	fmt.Printf("  Workloads:  %d total, %d running, %d stopped\n", statsResponse.WorkloadCount, statsResponse.RunningCount, statsResponse.StoppedCount)
	fmt.Printf("  Log dirs:   %d\n", statsResponse.LogDirCount)
	fmt.Printf("  CPU:        %dm\n", statsResponse.TotalCPUMillicores)
	fmt.Printf("  Memory:     %s\n", formatBytesHuman(statsResponse.TotalMemoryBytes))
	fmt.Println()
}

// displayNodeImages fetches and prints the image list from the agent's debug API.
func displayNodeImages(nodeID string) {
	responseBody := queryNodeDebugEndpoint(nodeID, "images")

	var imagesResponse agent.DebugImagesResponse
	if decodeError := json.Unmarshal(responseBody, &imagesResponse); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode images: %v\n", decodeError)
		return
	}

	fmt.Printf("IMAGES (%d)\n", imagesResponse.Count)
	if imagesResponse.Count == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-40s  %-16s  %-14s  %s\n", "REPOSITORY", "TAG", "IMAGE ID", "SIZE")
		for _, imageEntry := range imagesResponse.Images {
			sizeDisplay := formatBytesHuman(imageEntry.SizeBytes)
			imageIDShort := imageEntry.ImageID
			if len(imageIDShort) > 12 {
				imageIDShort = imageIDShort[:12]
			}
			fmt.Printf("  %-40s  %-16s  %-14s  %s\n",
				imageEntry.Repository, imageEntry.Tag, imageIDShort, sizeDisplay)
		}
	}
}

// nodeDebugProxyURL returns the API server URL that proxies the given debug
// endpoint on a node's agent (Flow 24).
func nodeDebugProxyURL(nodeID string, endpointName string) string {
	return statusAPIBaseURL() + "/api/node/debug/" + url.PathEscape(nodeID) + "/" + endpointName
}

// queryNodeDebugEndpoint sends an authenticated GET for a node debug endpoint
// through the API server and returns the response body. Exits on failure.
func queryNodeDebugEndpoint(nodeID string, endpointName string) []byte {
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(nodeDebugProxyURL(nodeID, endpointName)) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "read response: %v\n", readError)
		os.Exit(1)
	}
	return responseBody
}

// postNodeDebugEndpoint sends an authenticated JSON POST for a node debug
// endpoint through the API server. The caller closes the response body.
func postNodeDebugEndpoint(nodeID string, endpointName string, requestBody []byte) (*http.Response, error) {
	return buildAuthenticatedHTTPClient().Post(nodeDebugProxyURL(nodeID, endpointName), "application/json", bytes.NewReader(requestBody))
}

// formatBytesHuman formats a byte count into a human-readable string with
// appropriate units (B, KiB, MiB, GiB).
func formatBytesHuman(bytesCount int64) string {
	if bytesCount == 0 {
		return "0B"
	}
	const unitKiB = 1024
	const unitMiB = 1024 * 1024
	const unitGiB = 1024 * 1024 * 1024
	if bytesCount >= unitGiB {
		return fmt.Sprintf("%.1fGiB", float64(bytesCount)/float64(unitGiB))
	}
	if bytesCount >= unitMiB {
		return fmt.Sprintf("%.1fMiB", float64(bytesCount)/float64(unitMiB))
	}
	if bytesCount >= unitKiB {
		return fmt.Sprintf("%.1fKiB", float64(bytesCount)/float64(unitKiB))
	}
	return fmt.Sprintf("%dB", bytesCount)
}
