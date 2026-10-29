// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Login, logout, and whoami CLI commands for user authentication.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/boyadzhievb/ccattler/security"
)

// credentialFileName is the name of the credentials file stored in the user's
// home directory under .ccattler/.
const credentialFileName = "credentials.json"

// tokenRefreshThreshold is how close to expiry the access token must be before
// the CLI automatically refreshes it on invocation.
const tokenRefreshThreshold = 5 * time.Minute

// credentialFile holds the JSON structure written to ~/.ccattler/credentials.json
// after a successful login.
type credentialFile struct {
	Server       string `json:"server"`                   // API server address used during login
	Token        string `json:"token"`                    // JWT bearer token for API authentication
	RefreshToken string `json:"refresh_token,omitempty"`  // OAuth2 refresh token for token renewal
	User         string `json:"user"`                     // username from the token subject claim
	Team         string `json:"team"`                     // team from the token claims (may be empty)
	Expires      string `json:"expires"`                  // RFC3339 expiration timestamp
	OIDCIssuer   string `json:"oidc_issuer,omitempty"`    // external OIDC issuer for refresh
	OIDCClientID string `json:"oidc_client_id,omitempty"` // external OIDC client ID for refresh
}

// executeLoginCommand authenticates against the server and stores the token in
// ~/.ccattler/credentials.json. When --token is provided, validates and stores
// the token directly. Without --token, attempts OIDC browser login if the
// server has an external OIDC provider configured.
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
	if tokenValue != "" {
		validateAndStoreCredential(serverAddress, tokenValue, "", "", "")
		return
	}

	executeOIDCBrowserLogin(serverAddress)
}

// executeOIDCBrowserLogin fetches the server's OIDC configuration, discovers
// the IdP endpoints, and runs the OAuth2 PKCE browser flow.
func executeOIDCBrowserLogin(serverAddress string) {
	oidcConfig, fetchError := fetchServerOIDCConfig(serverAddress)
	if fetchError != nil {
		fmt.Fprintf(os.Stderr, "cannot fetch OIDC config from %s: %v\n", serverAddress, fetchError)
		fmt.Fprintln(os.Stderr, "hint: use 'cca login <server> --token <jwt>' for manual token login")
		os.Exit(1)
	}

	externalIssuer, issuerFound := oidcConfig["external_oidc_issuer"].(string)
	externalClientID, clientIDFound := oidcConfig["external_oidc_client_id"].(string)
	if !issuerFound || !clientIDFound || externalIssuer == "" || externalClientID == "" {
		fmt.Fprintln(os.Stderr, "server does not have an external OIDC provider configured")
		fmt.Fprintln(os.Stderr, "hint: start the server with --oidc-issuer and --oidc-client-id")
		fmt.Fprintln(os.Stderr, "  or use: cca login <server> --token <jwt>")
		os.Exit(1)
	}

	authorizeURL, tokenURL, discoveryError := security.DiscoverOIDCEndpoints(externalIssuer)
	if discoveryError != nil {
		fmt.Fprintf(os.Stderr, "OIDC discovery failed for %s: %v\n", externalIssuer, discoveryError)
		os.Exit(1)
	}

	flowConfig := security.OIDCBrowserFlowConfig{
		AuthorizeURL: authorizeURL,
		TokenURL:     tokenURL,
		ClientID:     externalClientID,
	}

	fmt.Println("Opening browser for login...")
	fmt.Println("If the browser does not open, visit the URL printed below.")

	flowResult, flowError := security.ExecuteOIDCBrowserFlow(
		context.Background(),
		flowConfig,
		openBrowserURL,
	)
	if flowError != nil {
		fmt.Fprintf(os.Stderr, "OIDC login failed: %v\n", flowError)
		os.Exit(1)
	}

	bearerToken := flowResult.IDToken
	if bearerToken == "" {
		bearerToken = flowResult.AccessToken
	}

	validateAndStoreCredential(serverAddress, bearerToken, flowResult.RefreshToken, externalIssuer, externalClientID)
}

// fetchServerOIDCConfig fetches the OIDC configuration from the server's
// /api/auth/oidc-config endpoint.
func fetchServerOIDCConfig(serverAddress string) (map[string]interface{}, error) {
	requestURL := fmt.Sprintf("%s://%s/api/auth/oidc-config", statusAPIScheme, serverAddress)
	httpClient := buildTLSHTTPClient()
	httpResponse, requestError := httpClient.Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		return nil, requestError
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", httpResponse.StatusCode)
	}

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return nil, readError
	}

	var result map[string]interface{}
	if unmarshalError := json.Unmarshal(responseBody, &result); unmarshalError != nil {
		return nil, unmarshalError
	}
	return result, nil
}

// buildTLSHTTPClient returns an HTTP client that trusts the CCattler CA if
// present but does not inject any bearer token.
func buildTLSHTTPClient() *http.Client {
	return &http.Client{
		Transport: buildTLSTransportFromCACert(),
		Timeout:   30 * time.Second,
	}
}

// validateAndStoreCredential calls /api/auth/whoami with the provided token to
// verify it is valid, then writes the credential file.
func validateAndStoreCredential(serverAddress string, tokenValue string, refreshToken string, oidcIssuer string, oidcClientID string) {
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
		Server:       serverAddress,
		Token:        tokenValue,
		RefreshToken: refreshToken,
		User:         whoamiResponse.Principal,
		Team:         whoamiResponse.Team,
		Expires:      time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
		OIDCIssuer:   oidcIssuer,
		OIDCClientID: oidcClientID,
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
		fmt.Println("use: cca login <server>")
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
	if credential.OIDCIssuer != "" {
		fmt.Printf("Auth:    OIDC (%s)\n", credential.OIDCIssuer)
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

	credentialJSON, marshalError := json.MarshalIndent(credential, "", "  ") //nolint:gosec // refresh_token is a credential but we must persist it
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

// refreshCredentialIfExpiring checks whether the stored credential is close to
// expiry and refreshes it using the OIDC refresh token if available. Called
// automatically before API requests in the CLI auth flow.
func refreshCredentialIfExpiring() {
	credential, loadError := loadCredentialFile()
	if loadError != nil {
		return
	}
	if credential.RefreshToken == "" || credential.OIDCIssuer == "" {
		return
	}
	if credential.Expires == "" {
		return
	}
	expiresTime, parseError := time.Parse(time.RFC3339, credential.Expires)
	if parseError != nil {
		return
	}
	if time.Until(expiresTime) > tokenRefreshThreshold {
		return
	}

	_, tokenURL, discoveryError := security.DiscoverOIDCEndpoints(credential.OIDCIssuer)
	if discoveryError != nil {
		return
	}

	refreshResult, refreshError := security.RefreshOIDCToken(tokenURL, credential.OIDCClientID, credential.RefreshToken)
	if refreshError != nil {
		fmt.Fprintf(os.Stderr, "warning: token refresh failed: %v\n", refreshError)
		fmt.Fprintln(os.Stderr, "hint: run 'cca login' to re-authenticate")
		return
	}

	newToken := refreshResult.IDToken
	if newToken == "" {
		newToken = refreshResult.AccessToken
	}

	credential.Token = newToken
	if refreshResult.RefreshToken != "" {
		credential.RefreshToken = refreshResult.RefreshToken
	}
	if refreshResult.ExpiresIn > 0 {
		credential.Expires = time.Now().Add(time.Duration(refreshResult.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	} else {
		credential.Expires = time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339)
	}

	writeCredentialFile(*credential)
}

// openBrowserURL opens the given URL in the user's default browser.
func openBrowserURL(targetURL string) error {
	fmt.Printf("  %s\n", targetURL)
	var openCommand string
	var openArgs []string
	switch runtime.GOOS {
	case "darwin":
		openCommand = "open"
		openArgs = []string{targetURL}
	case "linux":
		openCommand = "xdg-open"
		openArgs = []string{targetURL}
	case "windows":
		openCommand = "rundll32"
		openArgs = []string{"url.dll,FileProtocolHandler", targetURL}
	default:
		fmt.Fprintln(os.Stderr, "cannot auto-open browser; please visit the URL above")
		return nil
	}
	return exec.Command(openCommand, openArgs...).Start() //nolint:gosec // opening user-visible URL in browser
}
