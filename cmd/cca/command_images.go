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

// imagesCommandConfig holds parsed flags for the "images" subcommands.
type imagesCommandConfig struct {
	subcommand     string // subcommand is "list" or "pull".
	nodeID         string // nodeID restricts the operation to a single node (optional).
	imageReference string // imageReference is the image to pull (required for "pull").
}

// parseImagesCommandArgs extracts subcommand and flags from the arguments
// following "images".
func parseImagesCommandArgs(args []string) imagesCommandConfig {
	parsedConfig := imagesCommandConfig{}
	if len(args) == 0 {
		return parsedConfig
	}
	parsedConfig.subcommand = args[0]

	for argIndex := 1; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--node":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		default:
			if parsedConfig.subcommand == "pull" && parsedConfig.imageReference == "" {
				parsedConfig.imageReference = currentArg
			}
		}
	}
	return parsedConfig
}

// executeImagesListCommand lists cached images on one or all nodes by querying
// each node's agent debug API.
func executeImagesListCommand(nodeID string) {
	debugAddresses := discoverNodeDebugAddresses(nodeID)
	if len(debugAddresses) == 0 {
		fmt.Fprintln(os.Stderr, "no nodes with debug API found")
		os.Exit(1)
	}

	for targetNodeID, debugAddress := range debugAddresses {
		responseBody := queryAgentDebugEndpoint(debugAddress, "/debug/images")

		var imagesResponse agent.DebugImagesResponse
		if decodeError := json.Unmarshal(responseBody, &imagesResponse); decodeError != nil {
			fmt.Fprintf(os.Stderr, "  error decoding images from %s: %v\n", targetNodeID, decodeError)
			continue
		}

		fmt.Printf("NODE %s (%d images)\n", targetNodeID, imagesResponse.Count)
		if imagesResponse.Count == 0 {
			fmt.Println("  (none)")
		} else {
			fmt.Printf("  %-40s  %-16s  %-14s  %s\n", "REPOSITORY", "TAG", "IMAGE ID", "SIZE")
			for _, imageEntry := range imagesResponse.Images {
				imageIDShort := imageEntry.ImageID
				if len(imageIDShort) > 12 {
					imageIDShort = imageIDShort[:12]
				}
				fmt.Printf("  %-40s  %-16s  %-14s  %s\n",
					imageEntry.Repository, imageEntry.Tag, imageIDShort,
					formatBytesHuman(imageEntry.SizeBytes))
			}
		}
		fmt.Println()
	}
}

// executeImagesPullCommand pulls an image on one or all nodes by sending
// POST /debug/images/pull to each node's agent debug API.
func executeImagesPullCommand(imageReference string, nodeID string) {
	debugAddresses := discoverNodeDebugAddresses(nodeID)
	if len(debugAddresses) == 0 {
		fmt.Fprintln(os.Stderr, "no nodes with debug API found")
		os.Exit(1)
	}

	for targetNodeID, debugAddress := range debugAddresses {
		fmt.Printf("Pulling %s on %s... ", imageReference, targetNodeID)

		pullRequest := agent.DebugImagePullRequest{ImageReference: imageReference}
		requestBody, _ := json.Marshal(pullRequest)

		requestURL := "http://" + debugAddress + "/debug/images/pull"
		if strings.HasPrefix(debugAddress, "http://") || strings.HasPrefix(debugAddress, "https://") {
			requestURL = debugAddress + "/debug/images/pull"
		}

		httpResponse, requestError := http.Post(requestURL, "application/json", bytes.NewReader(requestBody)) //nolint:gosec // CLI connects to agent debug API
		if requestError != nil {
			fmt.Printf("FAILED (%v)\n", requestError)
			continue
		}

		var pullResponse agent.DebugImagePullResponse
		decodeError := json.NewDecoder(httpResponse.Body).Decode(&pullResponse)
		_ = httpResponse.Body.Close()
		if decodeError != nil {
			fmt.Printf("FAILED (decode: %v)\n", decodeError)
			continue
		}

		if pullResponse.Success {
			fmt.Println("OK")
		} else {
			fmt.Printf("FAILED (%s)\n", pullResponse.Error)
		}
	}
}

// discoverNodeDebugAddresses returns a map of nodeID → debug address for
// nodes that have published a debug_address. When filterNodeID is non-empty,
// only that node is returned.
func discoverNodeDebugAddresses(filterNodeID string) map[string]string {
	if filterNodeID != "" {
		address := discoverNodeDebugAddress(filterNodeID)
		return map[string]string{filterNodeID: address}
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/state?prefix=observed/node/"
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

	debugAddresses := make(map[string]string)
	for _, factEntry := range facts {
		if strings.HasSuffix(factEntry.Key, "/debug_address") && factEntry.Value != "" {
			extractedNodeID := extractNodeIDFromDebugKey(factEntry.Key)
			if extractedNodeID != "" {
				debugAddresses[extractedNodeID] = factEntry.Value
			}
		}
	}
	return debugAddresses
}

// extractNodeIDFromDebugKey extracts the node ID from a key like
// "observed/node/{nodeID}/debug_address".
func extractNodeIDFromDebugKey(factKey string) string {
	trimmed := strings.TrimPrefix(factKey, "observed/node/")
	nodeID := strings.TrimSuffix(trimmed, "/debug_address")
	if nodeID == trimmed || nodeID == "" {
		return ""
	}
	return nodeID
}
