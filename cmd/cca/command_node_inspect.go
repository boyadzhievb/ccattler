// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/boyadzhievb/ccattler/agent"
)

// executeNodeInspectCommand queries a node agent's debug API and displays
// live runtime containers, images, and resource usage. It first discovers
// the agent's debug address from the control plane, then queries the agent
// directly.
func executeNodeInspectCommand(nodeID string) {
	debugAddress := discoverNodeDebugAddress(nodeID)

	fmt.Printf("Inspecting node %s (debug API at %s)\n\n", nodeID, debugAddress)

	displayNodeContainers(debugAddress)
	displayNodeStats(debugAddress)
	displayNodeImages(debugAddress)
}

// discoverNodeDebugAddress reads the node's debug_address fact from the
// control plane API. Falls back to querying the store directly via the
// state API endpoint. Exits with an error if the address cannot be found.
func discoverNodeDebugAddress(nodeID string) string {
	factKey := fmt.Sprintf("observed/node/%s/debug_address", nodeID)
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

	fmt.Fprintf(os.Stderr, "node %q has no debug API address — is the agent running with --debug-listen?\n", nodeID)
	os.Exit(1)
	return ""
}

// displayNodeContainers fetches and prints the container list from the agent's
// debug API in a tabular format.
func displayNodeContainers(debugAddress string) {
	responseBody := queryAgentDebugEndpoint(debugAddress, "/debug/containers")

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
func displayNodeStats(debugAddress string) {
	responseBody := queryAgentDebugEndpoint(debugAddress, "/debug/stats")

	var statsResponse agent.DebugStatsResponse
	if decodeError := json.Unmarshal(responseBody, &statsResponse); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode stats: %v\n", decodeError)
		return
	}

	fmt.Println("RESOURCE USAGE")
	fmt.Printf("  Workloads:  %d total, %d running\n", statsResponse.WorkloadCount, statsResponse.RunningCount)
	fmt.Printf("  CPU:        %dm\n", statsResponse.TotalCPUMillicores)
	fmt.Printf("  Memory:     %s\n", formatBytesHuman(statsResponse.TotalMemoryBytes))
	fmt.Println()
}

// displayNodeImages fetches and prints the image list from the agent's debug API.
func displayNodeImages(debugAddress string) {
	responseBody := queryAgentDebugEndpoint(debugAddress, "/debug/images")

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

// queryAgentDebugEndpoint sends a GET request to the agent's debug API and
// returns the response body. Exits on connection failure.
func queryAgentDebugEndpoint(debugAddress string, endpointPath string) []byte {
	requestURL := "http://" + debugAddress + endpointPath
	if strings.HasPrefix(debugAddress, "http://") || strings.HasPrefix(debugAddress, "https://") {
		requestURL = debugAddress + endpointPath
	}
	httpResponse, requestError := http.Get(requestURL) //nolint:gosec // CLI connects to agent debug API
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to agent debug API at %s: %v\n", debugAddress, requestError)
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "read response: %v\n", readError)
		os.Exit(1)
	}
	return responseBody
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
