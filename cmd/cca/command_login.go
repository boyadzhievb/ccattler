// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Login, logout, and whoami CLI commands for user authentication.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// credentialFileName is the name of the credentials file stored in the user's
// home directory under .ccattler/.
const credentialFileName = "credentials.json"

// credentialFile holds the JSON structure written to ~/.ccattler/credentials.json
// after a successful login.
type credentialFile struct {
	Server  string `json:"server"`  // API server address used during login
	Token   string `json:"token"`   // JWT bearer token for API authentication
	User    string `json:"user"`    // username from the token subject claim
	Team    string `json:"team"`    // team from the token claims (may be empty)
	Expires string `json:"expires"` // RFC3339 expiration timestamp
}

// executeLoginCommand authenticates against the server and stores the token in
// ~/.ccattler/credentials.json. When --token is provided, validates and stores
// the token directly. Without --token, the token is fetched interactively.
func executeLoginCommand(args []string) {
	serverAddress := ""
	tokenValue := ""

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--token":
			if argIndex+1 < len(args) {
				argIndex++
				tokenValue = args[argIndex]
			}
		default:
			if serverAddress == "" {
				serverAddress = args[argIndex]
			}
		}
	}

	if serverAddress == "" {
		serverAddress = statusAPIListenAddress
	}
	if tokenValue == "" {
		fmt.Fprintln(os.Stderr, "usage: cca login <server> --token <jwt>")
		fmt.Fprintln(os.Stderr, "  (OIDC browser login will be available in a future release)")
		os.Exit(1)
	}

	validateAndStoreCredential(serverAddress, tokenValue)
}

// validateAndStoreCredential calls /api/auth/whoami with the provided token to
// verify it is valid, then writes the credential file.
func validateAndStoreCredential(serverAddress string, tokenValue string) {
	requestURL := fmt.Sprintf("%s://%s/api/auth/whoami", statusAPIScheme, serverAddress)
	httpClient := buildBearerTokenHTTPClient(tokenValue)
	httpResponse, requestError := httpClient.Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to %s — is the server running?\n", serverAddress)
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != 200 {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "authentication failed: %s\n", responseBody)
		os.Exit(1)
	}

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error reading response: %v\n", readError)
		os.Exit(1)
	}

	var whoamiResponse struct {
		Principal string   `json:"principal"`
		Team      string   `json:"team"`
		Groups    []string `json:"groups"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &whoamiResponse); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}

	credential := credentialFile{
		Server:  serverAddress,
		Token:   tokenValue,
		User:    whoamiResponse.Principal,
		Team:    whoamiResponse.Team,
		Expires: time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
	}

	writeCredentialFile(credential)
	fmt.Printf("Logged in as %s", whoamiResponse.Principal)
	if whoamiResponse.Team != "" {
		fmt.Printf(" (team: %s)", whoamiResponse.Team)
	}
	fmt.Println()
}

// executeLogoutCommand removes the stored credentials file.
func executeLogoutCommand() {
	credentialPath := resolveCredentialFilePath()
	if _, statError := os.Stat(credentialPath); os.IsNotExist(statError) {
		fmt.Println("not logged in")
		return
	}
	if removeError := os.Remove(credentialPath); removeError != nil {
		fmt.Fprintf(os.Stderr, "error removing credentials: %v\n", removeError)
		os.Exit(1)
	}
	fmt.Println("Logged out")
}

// executeWhoamiCommand displays the current user identity from the stored
// credential or by querying the API.
func executeWhoamiCommand() {
	credential, loadError := loadCredentialFile()
	if loadError != nil {
		fmt.Println("not logged in (no credential file)")
		fmt.Println("use: cca login <server> --token <jwt>")
		return
	}

	fmt.Printf("User:    %s\n", credential.User)
	if credential.Team != "" {
		fmt.Printf("Team:    %s\n", credential.Team)
	}
	fmt.Printf("Server:  %s\n", credential.Server)
	if credential.Expires != "" {
		expiresTime, parseError := time.Parse(time.RFC3339, credential.Expires)
		if parseError == nil {
			remaining := time.Until(expiresTime)
			if remaining > 0 {
				fmt.Printf("Expires: %s (%s remaining)\n",
					expiresTime.Format("2006-01-02 15:04 UTC"),
					formatDurationRounded(remaining))
			} else {
				fmt.Printf("Expires: %s (EXPIRED)\n", expiresTime.Format("2006-01-02 15:04 UTC"))
			}
		}
	}
}

// formatDurationRounded formats a duration as a human-readable string with
// hours and minutes precision.
func formatDurationRounded(duration time.Duration) string {
	totalHours := int(duration.Hours())
	remainingMinutes := int(duration.Minutes()) % 60
	if totalHours > 0 {
		return fmt.Sprintf("%dh %dm", totalHours, remainingMinutes)
	}
	return fmt.Sprintf("%dm", remainingMinutes)
}

// resolveCredentialFilePath returns the path to ~/.ccattler/credentials.json.
func resolveCredentialFilePath() string {
	homeDirectory, homeError := os.UserHomeDir()
	if homeError != nil {
		return filepath.Join(".ccattler", credentialFileName)
	}
	return filepath.Join(homeDirectory, ".ccattler", credentialFileName)
}

// writeCredentialFile writes the credential to ~/.ccattler/credentials.json,
// creating the directory if needed.
func writeCredentialFile(credential credentialFile) {
	credentialPath := resolveCredentialFilePath()
	credentialDirectory := filepath.Dir(credentialPath)

	if mkdirError := os.MkdirAll(credentialDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s: %v\n", credentialDirectory, mkdirError)
		os.Exit(1)
	}

	credentialJSON, marshalError := json.MarshalIndent(credential, "", "  ")
	if marshalError != nil {
		fmt.Fprintf(os.Stderr, "error marshaling credential: %v\n", marshalError)
		os.Exit(1)
	}

	if writeError := os.WriteFile(credentialPath, credentialJSON, 0600); writeError != nil { //nolint:gosec // credential file with restricted permissions
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", credentialPath, writeError)
		os.Exit(1)
	}
}

// loadCredentialFile reads and parses ~/.ccattler/credentials.json.
func loadCredentialFile() (*credentialFile, error) {
	credentialPath := resolveCredentialFilePath()
	credentialJSON, readError := os.ReadFile(credentialPath) //nolint:gosec // path is derived from user's home directory
	if readError != nil {
		return nil, readError
	}
	var credential credentialFile
	if unmarshalError := json.Unmarshal(credentialJSON, &credential); unmarshalError != nil {
		return nil, unmarshalError
	}
	return &credential, nil
}
