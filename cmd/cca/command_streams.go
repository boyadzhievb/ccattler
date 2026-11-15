// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_streams.go implements `cca streams`: who holds the open watch,
// events and audit-follow streams on the API server (M225).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

// streamsResponse mirrors GET /api/streams.
type streamsResponse struct {
	Total      int `json:"total"` // Total is every open stream on the API server.
	Limit      int `json:"limit"` // Limit is the open-stream limit per principal.
	Principals []struct {
		Principal   string `json:"principal"`    // Principal is the kind:name.
		OpenStreams int    `json:"open_streams"` // OpenStreams is how many it holds open.
	} `json:"principals"` // Principals is sorted by open streams, most first.
}

// executeStreamsCommand prints the open streams per principal (cluster.admin).
func executeStreamsCommand(args []string) {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: cca streams")
		os.Exit(1)
	}
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(statusAPIBaseURL() + "/api/streams") //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	checkAPIResponseStatus(httpResponse)

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error reading response: %v\n", readError)
		os.Exit(1)
	}
	var streams streamsResponse
	if unmarshalError := json.Unmarshal(responseBody, &streams); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}
	fmt.Printf("%d open streams on this API server (limit %d per principal)\n", streams.Total, streams.Limit)
	if len(streams.Principals) == 0 {
		return
	}
	var rows [][]string
	for _, principalStreams := range streams.Principals {
		rows = append(rows, []string{principalStreams.Principal, strconv.Itoa(principalStreams.OpenStreams)})
	}
	printAlignedTable([]string{"PRINCIPAL", "OPEN STREAMS"}, rows)
}
