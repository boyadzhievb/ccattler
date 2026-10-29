// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Role management CLI commands: grant, revoke, list, check.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// executeRoleCommand dispatches `cca role <subcommand>` to the appropriate
// handler: grant, revoke, list, or check.
func executeRoleCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca role <grant|revoke|list|check> [args]")
		os.Exit(1)
	}
	subcommand := args[0]
	switch subcommand {
	case "grant":
		executeRoleGrant(args[1:])
	case "revoke":
		executeRoleRevoke(args[1:])
	case "list":
		executeRoleList(args[1:])
	case "check":
		executeRoleCheck(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: cca role <grant|revoke|list|check> [args]")
		os.Exit(1)
	}
}

// roleGrantRequest is the JSON body for grant and revoke API calls.
type roleGrantRequest struct {
	Principal  string `json:"principal"`
	Role       string `json:"role"`
	Capability string `json:"capability"`
	Scope      string `json:"scope"`
}

// executeRoleGrant grants a role or capability to a principal via POST /api/role.
// Usage: cca role grant <principal> --role <role> [--scope <scope>]
//
//	cca role grant <principal> --capability <cap> [--scope <scope>]
func executeRoleGrant(args []string) {
	principalName := ""
	roleName := ""
	capabilityName := ""
	scopePath := ""

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--role":
			if argIndex+1 < len(args) {
				argIndex++
				roleName = args[argIndex]
			}
		case "--capability":
			if argIndex+1 < len(args) {
				argIndex++
				capabilityName = args[argIndex]
			}
		case "--scope":
			if argIndex+1 < len(args) {
				argIndex++
				scopePath = args[argIndex]
			}
		default:
			if principalName == "" {
				principalName = args[argIndex]
			}
		}
	}

	if principalName == "" || (roleName == "" && capabilityName == "") {
		fmt.Fprintln(os.Stderr, "usage: cca role grant <principal> --role <role> [--scope <scope>]")
		fmt.Fprintln(os.Stderr, "       cca role grant <principal> --capability <cap> [--scope <scope>]")
		os.Exit(1)
	}

	requestBody := roleGrantRequest{
		Principal:  principalName,
		Role:       roleName,
		Capability: capabilityName,
		Scope:      scopePath,
	}
	bodyJSON, _ := json.Marshal(requestBody)

	requestURL := fmt.Sprintf("%s/api/role", statusAPIBaseURL())
	httpResponse, requestError := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", bytes.NewReader(bodyJSON)) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	if roleName != "" {
		fmt.Printf("granted role %q to %s\n", roleName, principalName)
	} else {
		scopeLabel := "cluster"
		if scopePath != "" {
			scopeLabel = scopePath
		}
		fmt.Printf("granted capability %q to %s at scope %s\n", capabilityName, principalName, scopeLabel)
	}
}

// executeRoleRevoke removes a role or capability from a principal via DELETE /api/role.
func executeRoleRevoke(args []string) {
	principalName := ""
	roleName := ""
	capabilityName := ""
	scopePath := ""

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--role":
			if argIndex+1 < len(args) {
				argIndex++
				roleName = args[argIndex]
			}
		case "--capability":
			if argIndex+1 < len(args) {
				argIndex++
				capabilityName = args[argIndex]
			}
		case "--scope":
			if argIndex+1 < len(args) {
				argIndex++
				scopePath = args[argIndex]
			}
		default:
			if principalName == "" {
				principalName = args[argIndex]
			}
		}
	}

	if principalName == "" || (roleName == "" && capabilityName == "") {
		fmt.Fprintln(os.Stderr, "usage: cca role revoke <principal> --role <role>")
		fmt.Fprintln(os.Stderr, "       cca role revoke <principal> --capability <cap> [--scope <scope>]")
		os.Exit(1)
	}

	requestBody := roleGrantRequest{
		Principal:  principalName,
		Role:       roleName,
		Capability: capabilityName,
		Scope:      scopePath,
	}
	bodyJSON, _ := json.Marshal(requestBody)

	requestURL := fmt.Sprintf("%s/api/role", statusAPIBaseURL())
	deleteRequest, _ := http.NewRequest(http.MethodDelete, requestURL, bytes.NewReader(bodyJSON)) //nolint:gosec // CLI connects to user-configured API server
	deleteRequest.Header.Set("Content-Type", "application/json")
	httpResponse, requestError := buildAuthenticatedHTTPClient().Do(deleteRequest) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	if roleName != "" {
		fmt.Printf("revoked role %q from %s\n", roleName, principalName)
	} else {
		fmt.Printf("revoked capability %q from %s\n", capabilityName, principalName)
	}
}

// executeRoleList lists all role grants via GET /api/role.
func executeRoleList(args []string) {
	principalFilter := ""
	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--principal":
			if argIndex+1 < len(args) {
				argIndex++
				principalFilter = args[argIndex]
			}
		default:
			if principalFilter == "" {
				principalFilter = args[argIndex]
			}
		}
	}

	requestURL := fmt.Sprintf("%s/api/role", statusAPIBaseURL())
	if principalFilter != "" {
		requestURL += "?principal=" + principalFilter
	}
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
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

	var listResponse struct {
		Grants []roleListEntry `json:"grants"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &listResponse); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}

	if len(listResponse.Grants) == 0 {
		fmt.Println("no role grants")
		return
	}

	header := []string{"PRINCIPAL", "ROLE", "CAPABILITIES", "SCOPE"}
	var rows [][]string
	for _, entry := range listResponse.Grants {
		capabilityDisplay := strings.Join(entry.Capabilities, ", ")
		if capabilityDisplay == "" {
			capabilityDisplay = "-"
		}
		scopeDisplay := entry.Scope
		if scopeDisplay == "" {
			scopeDisplay = "cluster"
		}
		rows = append(rows, []string{entry.Principal, entry.Role, capabilityDisplay, scopeDisplay})
	}
	printRoleTable(header, rows)
}

// roleListEntry represents a single grant in the list response.
type roleListEntry struct {
	Principal    string   `json:"principal"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Scope        string   `json:"scope"`
}

// executeRoleCheck checks whether a principal has a capability at a scope.
func executeRoleCheck(args []string) {
	principalName := ""
	capabilityName := ""
	scopePath := ""

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--capability":
			if argIndex+1 < len(args) {
				argIndex++
				capabilityName = args[argIndex]
			}
		case "--scope":
			if argIndex+1 < len(args) {
				argIndex++
				scopePath = args[argIndex]
			}
		default:
			if principalName == "" {
				principalName = args[argIndex]
			}
		}
	}

	if principalName == "" || capabilityName == "" {
		fmt.Fprintln(os.Stderr, "usage: cca role check <principal> --capability <cap> [--scope <scope>]")
		os.Exit(1)
	}

	requestBody := map[string]string{
		"principal":  principalName,
		"capability": capabilityName,
		"scope":      scopePath,
	}
	bodyJSON, _ := json.Marshal(requestBody)

	requestURL := fmt.Sprintf("%s/api/role/check", statusAPIBaseURL())
	httpResponse, requestError := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", bytes.NewReader(bodyJSON)) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	responseBody, _ := io.ReadAll(httpResponse.Body)

	var checkResponse struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(responseBody, &checkResponse)

	if checkResponse.Allowed {
		fmt.Printf("ALLOWED: %s has %q at scope %s\n", principalName, capabilityName, scopeOrCluster(scopePath))
	} else {
		fmt.Printf("DENIED: %s does not have %q at scope %s\n", principalName, capabilityName, scopeOrCluster(scopePath))
		if checkResponse.Reason != "" {
			fmt.Printf("  reason: %s\n", checkResponse.Reason)
		}
		os.Exit(1)
	}
}

// scopeOrCluster returns "cluster" when the scope is empty.
func scopeOrCluster(scopePath string) string {
	if scopePath == "" {
		return "cluster"
	}
	return scopePath
}

// printRoleTable prints an aligned table for role list output.
func printRoleTable(header []string, rows [][]string) {
	columnWidths := make([]int, len(header))
	for columnIndex, columnHeader := range header {
		columnWidths[columnIndex] = len(columnHeader)
	}
	for _, row := range rows {
		for columnIndex, cellValue := range row {
			if len(cellValue) > columnWidths[columnIndex] {
				columnWidths[columnIndex] = len(cellValue)
			}
		}
	}

	formatParts := make([]string, len(header))
	for columnIndex, columnWidth := range columnWidths {
		formatParts[columnIndex] = fmt.Sprintf("%%-%ds", columnWidth)
	}
	formatString := strings.Join(formatParts, "  ")

	headerValues := make([]interface{}, len(header))
	for columnIndex, columnHeader := range header {
		headerValues[columnIndex] = columnHeader
	}
	fmt.Printf(formatString+"\n", headerValues...)

	for _, row := range rows {
		rowValues := make([]interface{}, len(row))
		for columnIndex, cellValue := range row {
			rowValues[columnIndex] = cellValue
		}
		fmt.Printf(formatString+"\n", rowValues...)
	}
}
