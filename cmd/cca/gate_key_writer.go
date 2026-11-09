// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/api"
)

// gateKeyRequestTimeout bounds one PUT /api/gate-key request.
const gateKeyRequestTimeout = 10 * time.Second

// gateKeyErrorBodyLimit caps how much of an error response is read.
const gateKeyErrorBodyLimit = 4 << 10

// apiGateKeyWriter seals inline gate keys through PUT /api/gate-key with the
// CLI's credentials (Flow 28 step 2). The CLI never holds an encryption key.
type apiGateKeyWriter struct {
	httpClient *http.Client // httpClient carries the CLI token and CA trust.
	apiBaseURL string       // apiBaseURL is e.g. "https://127.0.0.1:9770".
}

// newAPIGateKeyWriter creates a writer for the configured API server.
func newAPIGateKeyWriter() *apiGateKeyWriter {
	return &apiGateKeyWriter{httpClient: buildAuthenticatedHTTPClient(), apiBaseURL: statusAPIBaseURL()}
}

// PutGateKey sends the key of gateName; any non-200 answer is an error with
// the server's message (never the key).
func (gateKeyWriter *apiGateKeyWriter) PutGateKey(ctx context.Context, gateName string, privateKeyPEM string) error {
	requestBody, _ := json.Marshal(api.GateKeyRequest{Gate: gateName, PrivateKeyPEM: privateKeyPEM})
	requestContext, cancelRequest := context.WithTimeout(ctx, gateKeyRequestTimeout)
	defer cancelRequest()
	request, requestError := http.NewRequestWithContext(requestContext, http.MethodPut, gateKeyWriter.apiBaseURL+api.GateKeyPath, bytes.NewReader(requestBody))
	if requestError != nil {
		return requestError
	}
	request.Header.Set("Content-Type", "application/json")
	response, sendError := gateKeyWriter.httpClient.Do(request)
	if sendError != nil {
		return fmt.Errorf("cannot reach the API server at %s: %w", gateKeyWriter.apiBaseURL, sendError)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	errorBody, _ := io.ReadAll(io.LimitReader(response.Body, gateKeyErrorBodyLimit))
	return fmt.Errorf("API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(errorBody)))
}

// localSimulationGateKeyWriter accepts inline gate keys in local in-memory
// modes (cca apply without --store etcd, cca run), which run no gate proxy:
// the key is not stored, and a note says so.
type localSimulationGateKeyWriter struct{}

// PutGateKey prints that the key of gateName is not stored locally.
func (localSimulationGateKeyWriter) PutGateKey(_ context.Context, gateName string, _ string) error {
	fmt.Printf("note: gate %s: inline tls key not stored (local mode has no gate TLS)\n", gateName)
	return nil
}
