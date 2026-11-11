// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
// each node's agent debug API through the API server proxy.
func executeImagesListCommand(nodeID string) {
	targetNodeIDs := discoverNodesWithDebugAPI(nodeID)
	if len(targetNodeIDs) == 0 {
		fmt.Fprintln(os.Stderr, "no nodes with debug API found")
		os.Exit(1)
	}

	for _, targetNodeID := range targetNodeIDs {
		responseBody := queryNodeDebugEndpoint(targetNodeID, "images")

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
// images/pull to each node's agent debug API through the API server proxy.
func executeImagesPullCommand(imageReference string, nodeID string) {
	targetNodeIDs := discoverNodesWithDebugAPI(nodeID)
	if len(targetNodeIDs) == 0 {
		fmt.Fprintln(os.Stderr, "no nodes with debug API found")
		os.Exit(1)
	}

	for _, targetNodeID := range targetNodeIDs {
		fmt.Printf("Pulling %s on %s... ", imageReference, targetNodeID)

		pullRequest := agent.DebugImagePullRequest{ImageReference: imageReference}
		requestBody, _ := json.Marshal(pullRequest)

		httpResponse, requestError := postNodeDebugEndpoint(targetNodeID, "images/pull", requestBody)
		if requestError != nil {
			fmt.Printf("FAILED (%v)\n", requestError)
			continue
		}

		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			_ = httpResponse.Body.Close()
			fmt.Printf("FAILED (HTTP %d)\n", httpResponse.StatusCode)
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

// stateFactEntry is one fact returned by GET /api/state.
type stateFactEntry struct {
	Key   string `json:"key"`   // Key is the fact's store key.
	Value string `json:"value"` // Value is the fact's value.
}

// fetchStateFactsWithPrefix reads the facts under prefix through GET
// /api/state as the CLI user; connection or decode failures exit.
func fetchStateFactsWithPrefix(prefix string) []stateFactEntry {
	apiURL := statusAPIBaseURL() + "/api/state?prefix=" + prefix
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(apiURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'server' running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)
	var facts []stateFactEntry
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}
	return facts
}

// discoverNodesWithDebugAPI returns the sorted IDs of nodes that have published
// a debug_address. When filterNodeID is non-empty, only that node is returned;
// the API proxy reports it if the node has no debug API.
func discoverNodesWithDebugAPI(filterNodeID string) []string {
	if filterNodeID != "" {
		return []string{filterNodeID}
	}

	var nodeIDs []string
	for _, factEntry := range fetchStateFactsWithPrefix("observed/node/") {
		if strings.HasSuffix(factEntry.Key, "/debug_address") && factEntry.Value != "" {
			extractedNodeID := extractNodeIDFromDebugKey(factEntry.Key)
			if extractedNodeID != "" {
				nodeIDs = append(nodeIDs, extractedNodeID)
			}
		}
	}
	sort.Strings(nodeIDs)
	return nodeIDs
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
