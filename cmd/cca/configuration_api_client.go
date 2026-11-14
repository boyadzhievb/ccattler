// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// configuration_api_client.go sends configurations to POST /api/apply and
// POST /api/diff. cca apply and cca diff reach the cluster only through these
// routes, so authorization, the policy gate and audit always apply (M219).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/boyadzhievb/ccattler/lang"
)

const (
	// configurationApplyPath is the API route that writes a configuration.
	configurationApplyPath = "/api/apply"
	// configurationDiffPath is the API route that compares a configuration with the cluster.
	configurationDiffPath = "/api/diff"
)

// configurationRequestBody is the JSON body of POST /api/apply and /api/diff.
type configurationRequestBody struct {
	Config string            `json:"config"`          // Config is the rendered DSL text.
	Files  map[string]string `json:"files,omitempty"` // Files maps declared path → contents of referenced files (gate tls cert, config "from").
}

// configurationApplyResponse is the JSON answer of POST /api/apply.
type configurationApplyResponse struct {
	FactsSet int `json:"facts_set"` // FactsSet is the number of facts the server wrote.
}

// postConfigurationToAPI sends requestBody to routePath (sendAPIJSONRequest)
// and, when the server is unreachable, points to --simulate.
func postConfigurationToAPI(ctx context.Context, routePath string, requestBody configurationRequestBody) ([]byte, error) {
	responseBody, sendError := sendAPIJSONRequest(ctx, http.MethodPost, routePath, requestBody)
	if errors.Is(sendError, errAPIServerUnreachable) {
		return nil, fmt.Errorf("%w (use --simulate for a local simulation)", sendError)
	}
	return responseBody, sendError
}

// applyRenderedFilesThroughAPI sends each rendered file to POST /api/apply
// with the files it references, then seals its inline gate keys through
// PUT /api/gate-key (gate facts first, Flow 28). Exits on the first error.
func applyRenderedFilesThroughAPI(ctx context.Context, renderedFiles []renderedDSLContent) {
	gateKeyWriter := newAPIGateKeyWriter()
	for _, rendered := range renderedFiles {
		fmt.Printf("Applying %s...\n", rendered.filePath)
		printDeprecationWarnings(rendered)
		referenced, readError := lang.ReadClientReferencedFiles(rendered.content, filepath.Dir(rendered.filePath))
		if readError != nil {
			exitWithFileError(readError, rendered.filePath)
		}
		responseBody, applyError := postConfigurationToAPI(ctx, configurationApplyPath,
			configurationRequestBody{Config: rendered.content, Files: referenced.Files})
		if applyError != nil {
			exitWithFileError(applyError, rendered.filePath)
		}
		var applyResult configurationApplyResponse
		_ = json.Unmarshal(responseBody, &applyResult)
		if keyError := sendInlineGateKeys(ctx, gateKeyWriter, referenced.InlineGateKeys); keyError != nil {
			exitWithFileError(keyError, rendered.filePath)
		}
		fmt.Printf("Applied %s (%d facts)\n", rendered.filePath, applyResult.FactsSet)
	}
	fmt.Println("Configuration accepted by the API server. Controllers will reconcile.")
}

// sendInlineGateKeys seals each inline gate key, sorted by gate name, through
// the writer. Errors name the gate and never contain the key.
func sendInlineGateKeys(ctx context.Context, gateKeyWriter lang.GateKeyWriter, inlineGateKeys map[string]string) error {
	gateNames := make([]string, 0, len(inlineGateKeys))
	for gateName := range inlineGateKeys {
		gateNames = append(gateNames, gateName)
	}
	sort.Strings(gateNames)
	for _, gateName := range gateNames {
		if putError := gateKeyWriter.PutGateKey(ctx, gateName, inlineGateKeys[gateName]); putError != nil {
			return fmt.Errorf("gate %q: storing tls key: %w", gateName, putError)
		}
	}
	return nil
}

// diffRenderedFilesThroughAPI sends the combined configuration and the files
// all rendered files reference to POST /api/diff and returns the changes.
func diffRenderedFilesThroughAPI(ctx context.Context, renderedFiles []renderedDSLContent) ([]lang.FactChange, error) {
	referencedFiles := map[string]string{}
	for _, rendered := range renderedFiles {
		referenced, readError := lang.ReadClientReferencedFiles(rendered.content, filepath.Dir(rendered.filePath))
		if readError != nil {
			return nil, annotateErrorWithFileName(readError, rendered.filePath)
		}
		if mergeError := mergeReferencedFiles(referencedFiles, referenced.Files, rendered.filePath); mergeError != nil {
			return nil, mergeError
		}
	}
	responseBody, diffError := postConfigurationToAPI(ctx, configurationDiffPath,
		configurationRequestBody{Config: combineDSLFileContents(renderedFiles), Files: referencedFiles})
	if diffError != nil {
		return nil, diffError
	}
	var changes []lang.FactChange
	if decodeError := json.Unmarshal(responseBody, &changes); decodeError != nil {
		return nil, fmt.Errorf("decode diff response: %w", decodeError)
	}
	return changes, nil
}

// mergeReferencedFiles adds one file's referenced files to the combined map.
// The same declared path with different contents (relative paths in files of
// different directories) is an error, since the server keys files by path.
func mergeReferencedFiles(combinedFiles map[string]string, fileReferences map[string]string, sourceFilePath string) error {
	for declaredPath, contents := range fileReferences {
		if existing, isPresent := combinedFiles[declaredPath]; isPresent && existing != contents {
			return fmt.Errorf("%s: %q names a different file than in another configuration file; use distinct paths", sourceFilePath, declaredPath)
		}
		combinedFiles[declaredPath] = contents
	}
	return nil
}

// exitWithFileError prints an error annotated with the configuration file
// name and exits with status 1.
func exitWithFileError(commandError error, filePath string) {
	fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(commandError, filePath))
	os.Exit(1)
}
