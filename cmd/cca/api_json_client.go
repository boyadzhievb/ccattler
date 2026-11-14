// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// api_json_client.go is the CLI's JSON request helper for API routes that
// replaced direct etcd access (cca apply, cca diff, cca token; M219).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// apiErrorBodyLimit caps how much of an error response is read.
const apiErrorBodyLimit = 64 << 10

// errAPIServerUnreachable marks a request that never got an answer, so a
// command can add its own hint (e.g. cca apply suggests --simulate).
var errAPIServerUnreachable = errors.New("cannot reach the API server")

// sendAPIJSONRequest sends requestBody (nil for none) as JSON with the CLI's
// credentials and returns the body of a 200 answer. Any other status is an
// error with the server's message; no answer wraps errAPIServerUnreachable
// and names the address.
func sendAPIJSONRequest(ctx context.Context, method string, routePath string, requestBody any) ([]byte, error) {
	var bodyReader io.Reader
	if requestBody != nil {
		encodedBody, encodeError := json.Marshal(requestBody)
		if encodeError != nil {
			return nil, encodeError
		}
		bodyReader = bytes.NewReader(encodedBody)
	}
	apiBaseURL := statusAPIBaseURL()
	request, requestError := http.NewRequestWithContext(ctx, method, apiBaseURL+routePath, bodyReader)
	if requestError != nil {
		return nil, requestError
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, sendError := buildAuthenticatedHTTPClient().Do(request) //nolint:gosec // CLI connects to user-configured API server
	if sendError != nil {
		return nil, fmt.Errorf("%w at %s: %w", errAPIServerUnreachable, apiBaseURL, sendError)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, apiErrorBodyLimit))
		return nil, fmt.Errorf("API server %s answered HTTP %d: %s", apiBaseURL, response.StatusCode, extractAPIErrorMessage(errorBody))
	}
	return io.ReadAll(response.Body)
}
