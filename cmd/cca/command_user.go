// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// User management CLI commands for the cca CLI.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// executeUserCommand dispatches `cca user <subcommand>` to the appropriate
// handler: create, token, list, or delete.
func executeUserCommand(args []string) {
	subcommand := args[0]
	switch subcommand {
	case "create":
		executeUserCreate(args[1:])
	case "token":
		executeUserCreateToken(args[1:])
	case "list":
		executeUserList()
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cca user delete <name>")
			os.Exit(1)
		}
		executeUserDelete(args[1])
	default:
		fmt.Fprintln(os.Stderr, "usage: cca user <create|token|list|delete> [args]")
		os.Exit(1)
	}
}

// executeUserCreate creates a user account via POST /api/user.
func executeUserCreate(args []string) {
	userName := ""
	teamName := ""
	var groupNames []string

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--team":
			if argIndex+1 < len(args) {
				argIndex++
				teamName = args[argIndex]
			}
		case "--group":
			if argIndex+1 < len(args) {
				argIndex++
				groupNames = append(groupNames, args[argIndex])
			}
		default:
			if userName == "" {
				userName = args[argIndex]
			}
		}
	}

	if userName == "" {
		fmt.Fprintln(os.Stderr, "usage: cca user create <name> [--team <team>] [--group <group>...]")
		os.Exit(1)
	}

	requestBody := map[string]interface{}{
		"team":   teamName,
		"groups": groupNames,
	}
	bodyJSON, _ := json.Marshal(requestBody)

	requestURL := fmt.Sprintf("http://%s/api/user?name=%s", statusAPIListenAddress, userName)
	httpResponse, requestError := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", bytes.NewReader(bodyJSON)) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	teamLabel := ""
	if teamName != "" {
		teamLabel = fmt.Sprintf(" (team: %s)", teamName)
	}
	fmt.Printf("user %q created%s\n", userName, teamLabel)
}

// executeUserCreateToken mints a JWT for the named user via POST /api/user/token.
func executeUserCreateToken(args []string) {
	userName := ""
	tokenTTL := ""

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--ttl":
			if argIndex+1 < len(args) {
				argIndex++
				tokenTTL = args[argIndex]
			}
		default:
			if userName == "" {
				userName = args[argIndex]
			}
		}
	}

	if userName == "" {
		fmt.Fprintln(os.Stderr, "usage: cca user token <name> [--ttl <duration>]")
		os.Exit(1)
	}

	requestBody := map[string]string{
		"username": userName,
	}
	if tokenTTL != "" {
		requestBody["ttl"] = tokenTTL
	}
	bodyJSON, _ := json.Marshal(requestBody)

	requestURL := fmt.Sprintf("http://%s/api/user/token", statusAPIListenAddress)
	httpResponse, requestError := buildAuthenticatedHTTPClient().Post(requestURL, "application/json", bytes.NewReader(bodyJSON)) //nolint:gosec // CLI connects to user-configured API server
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

	var tokenResponse struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &tokenResponse); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}

	fmt.Println(tokenResponse.Token)
}

// executeUserList lists all user accounts via GET /api/user.
func executeUserList() {
	requestURL := fmt.Sprintf("http://%s/api/user", statusAPIListenAddress)
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
		Users []string `json:"users"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &listResponse); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}

	if len(listResponse.Users) == 0 {
		fmt.Println("no users")
		return
	}
	for _, userName := range listResponse.Users {
		fmt.Println(userName)
	}
}

// executeUserDelete removes a user account via DELETE /api/user.
func executeUserDelete(userName string) {
	requestURL := fmt.Sprintf("http://%s/api/user?name=%s", statusAPIListenAddress, userName)
	deleteRequest, _ := http.NewRequest(http.MethodDelete, requestURL, nil)        //nolint:gosec // CLI connects to user-configured API server
	httpResponse, requestError := buildAuthenticatedHTTPClient().Do(deleteRequest) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	fmt.Printf("user %q deleted\n", userName)
}
