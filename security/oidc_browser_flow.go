// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultBrowserFlowTimeout is the maximum time to wait for the user to
// complete the browser-based OIDC login before the callback server shuts down.
const defaultBrowserFlowTimeout = 5 * time.Minute

// codeVerifierLength is the byte length of the PKCE code verifier before
// base64url encoding. RFC 7636 requires 43-128 characters; 32 bytes yields 43.
const codeVerifierLength = 32

// OIDCBrowserFlowConfig holds the parameters needed to execute an OAuth2
// authorization code flow with PKCE.
type OIDCBrowserFlowConfig struct {
	AuthorizeURL string // IdP authorization endpoint (e.g. "https://accounts.google.com/o/oauth2/v2/auth")
	TokenURL     string // IdP token endpoint (e.g. "https://oauth2.googleapis.com/token")
	ClientID     string // OAuth2 client ID registered with the IdP
	Scopes       string // space-separated scopes (default: "openid email profile")
}

// OIDCBrowserFlowResult contains the tokens returned by a successful OIDC
// browser login.
type OIDCBrowserFlowResult struct {
	AccessToken  string // bearer token for API access
	IDToken      string // OIDC ID token (JWT with user claims)
	RefreshToken string // refresh token for obtaining new access tokens
	ExpiresIn    int    // access token lifetime in seconds
	TokenType    string // token type (typically "Bearer")
}

// ExecuteOIDCBrowserFlow runs the full OAuth2 PKCE browser flow: starts a
// localhost callback server, opens the browser to the IdP authorization URL,
// waits for the callback with the authorization code, exchanges it for tokens,
// and returns the result. The openBrowserFunc callback is called with the URL
// the user should visit.
func ExecuteOIDCBrowserFlow(ctx context.Context, config OIDCBrowserFlowConfig, openBrowserFunc func(string) error) (*OIDCBrowserFlowResult, error) {
	codeVerifier, codeChallenge, generateError := generatePKCECodePair()
	if generateError != nil {
		return nil, fmt.Errorf("oidc browser flow: generate PKCE pair: %w", generateError)
	}

	callbackListener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		return nil, fmt.Errorf("oidc browser flow: listen: %w", listenError)
	}
	callbackAddr, _ := callbackListener.Addr().(*net.TCPAddr) //nolint:errcheck // always succeeds for TCP listener
	callbackPort := callbackAddr.Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", callbackPort)

	stateToken, stateError := generateRandomState()
	if stateError != nil {
		_ = callbackListener.Close()
		return nil, fmt.Errorf("oidc browser flow: generate state: %w", stateError)
	}

	scopes := config.Scopes
	if scopes == "" {
		scopes = "openid email profile"
	}

	authorizeParams := url.Values{
		"response_type":         {"code"},
		"client_id":             {config.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"state":                 {stateToken},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	authorizeURL := config.AuthorizeURL + "?" + authorizeParams.Encode()

	authorizationCodeChannel := make(chan string, 1)
	callbackErrorChannel := make(chan error, 1)

	callbackMux := http.NewServeMux()
	callbackMux.HandleFunc("/callback", func(responseWriter http.ResponseWriter, request *http.Request) {
		queryState := request.URL.Query().Get("state")
		if queryState != stateToken {
			callbackErrorChannel <- fmt.Errorf("state mismatch: expected %q, got %q", stateToken, queryState)
			responseWriter.WriteHeader(http.StatusBadRequest)
			_, _ = responseWriter.Write([]byte("Login failed: state mismatch. You can close this tab."))
			return
		}
		if errorCode := request.URL.Query().Get("error"); errorCode != "" {
			errorDescription := request.URL.Query().Get("error_description")
			callbackErrorChannel <- fmt.Errorf("IdP error: %s (%s)", errorCode, errorDescription)
			responseWriter.WriteHeader(http.StatusBadRequest)
			_, _ = responseWriter.Write([]byte("Login failed. You can close this tab."))
			return
		}
		authorizationCode := request.URL.Query().Get("code")
		if authorizationCode == "" {
			callbackErrorChannel <- fmt.Errorf("no authorization code in callback")
			responseWriter.WriteHeader(http.StatusBadRequest)
			_, _ = responseWriter.Write([]byte("Login failed: no code received. You can close this tab."))
			return
		}
		responseWriter.Header().Set("Content-Type", "text/html")
		_, _ = responseWriter.Write([]byte("<html><body><h2>Login successful!</h2><p>You can close this tab and return to the terminal.</p></body></html>"))
		authorizationCodeChannel <- authorizationCode
	})

	callbackServer := &http.Server{
		Handler:           callbackMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = callbackServer.Serve(callbackListener)
	}()
	defer func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = callbackServer.Shutdown(shutdownContext)
	}()

	if openBrowserError := openBrowserFunc(authorizeURL); openBrowserError != nil {
		return nil, fmt.Errorf("oidc browser flow: open browser: %w", openBrowserError)
	}

	flowContext, flowCancel := context.WithTimeout(ctx, defaultBrowserFlowTimeout)
	defer flowCancel()

	var authorizationCode string
	select {
	case authorizationCode = <-authorizationCodeChannel:
	case callbackError := <-callbackErrorChannel:
		return nil, fmt.Errorf("oidc browser flow: %w", callbackError)
	case <-flowContext.Done():
		return nil, fmt.Errorf("oidc browser flow: timed out waiting for callback")
	}

	return exchangeAuthorizationCode(config, authorizationCode, redirectURI, codeVerifier)
}

// exchangeAuthorizationCode sends the authorization code to the IdP token
// endpoint with the PKCE code verifier and returns the token response.
func exchangeAuthorizationCode(config OIDCBrowserFlowConfig, authorizationCode string, redirectURI string, codeVerifier string) (*OIDCBrowserFlowResult, error) {
	tokenRequestBody := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorizationCode},
		"redirect_uri":  {redirectURI},
		"client_id":     {config.ClientID},
		"code_verifier": {codeVerifier},
	}

	tokenHTTPClient := &http.Client{Timeout: 30 * time.Second}
	tokenResponse, requestError := tokenHTTPClient.PostForm(config.TokenURL, tokenRequestBody) //nolint:gosec // URL is from server's OIDC config
	if requestError != nil {
		return nil, fmt.Errorf("token exchange: %w", requestError)
	}
	defer func() { _ = tokenResponse.Body.Close() }()

	responseBody, readError := io.ReadAll(tokenResponse.Body)
	if readError != nil {
		return nil, fmt.Errorf("token exchange: read response: %w", readError)
	}

	if tokenResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: HTTP %d: %s", tokenResponse.StatusCode, string(responseBody))
	}

	var tokenResponseJSON struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &tokenResponseJSON); unmarshalError != nil {
		return nil, fmt.Errorf("token exchange: parse response: %w", unmarshalError)
	}
	if tokenResponseJSON.Error != "" {
		return nil, fmt.Errorf("token exchange: %s (%s)", tokenResponseJSON.Error, tokenResponseJSON.ErrorDesc)
	}
	if tokenResponseJSON.AccessToken == "" && tokenResponseJSON.IDToken == "" {
		return nil, fmt.Errorf("token exchange: no access_token or id_token in response")
	}

	return &OIDCBrowserFlowResult{
		AccessToken:  tokenResponseJSON.AccessToken,
		IDToken:      tokenResponseJSON.IDToken,
		RefreshToken: tokenResponseJSON.RefreshToken,
		ExpiresIn:    tokenResponseJSON.ExpiresIn,
		TokenType:    tokenResponseJSON.TokenType,
	}, nil
}

// RefreshOIDCToken exchanges a refresh token for a new access token using the
// IdP token endpoint.
func RefreshOIDCToken(tokenURL string, clientID string, refreshToken string) (*OIDCBrowserFlowResult, error) {
	refreshRequestBody := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	tokenHTTPClient := &http.Client{Timeout: 30 * time.Second}
	tokenResponse, requestError := tokenHTTPClient.PostForm(tokenURL, refreshRequestBody) //nolint:gosec // URL is from server's OIDC config
	if requestError != nil {
		return nil, fmt.Errorf("token refresh: %w", requestError)
	}
	defer func() { _ = tokenResponse.Body.Close() }()

	responseBody, readError := io.ReadAll(tokenResponse.Body)
	if readError != nil {
		return nil, fmt.Errorf("token refresh: read response: %w", readError)
	}

	if tokenResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh: HTTP %d: %s", tokenResponse.StatusCode, string(responseBody))
	}

	var tokenResponseJSON struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &tokenResponseJSON); unmarshalError != nil {
		return nil, fmt.Errorf("token refresh: parse response: %w", unmarshalError)
	}
	if tokenResponseJSON.Error != "" {
		return nil, fmt.Errorf("token refresh: %s (%s)", tokenResponseJSON.Error, tokenResponseJSON.ErrorDesc)
	}

	result := &OIDCBrowserFlowResult{
		AccessToken:  tokenResponseJSON.AccessToken,
		IDToken:      tokenResponseJSON.IDToken,
		RefreshToken: tokenResponseJSON.RefreshToken,
		ExpiresIn:    tokenResponseJSON.ExpiresIn,
		TokenType:    tokenResponseJSON.TokenType,
	}
	if result.RefreshToken == "" {
		result.RefreshToken = refreshToken
	}
	return result, nil
}

// generatePKCECodePair creates a code_verifier and its S256 code_challenge
// as defined by RFC 7636.
func generatePKCECodePair() (string, string, error) {
	verifierBytes := make([]byte, codeVerifierLength)
	if _, readError := rand.Read(verifierBytes); readError != nil {
		return "", "", readError
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	challengeHash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	return codeVerifier, codeChallenge, nil
}

// generateRandomState creates a random state parameter for CSRF protection.
func generateRandomState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, readError := rand.Read(stateBytes); readError != nil {
		return "", readError
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}

// DiscoverOIDCEndpoints fetches the OIDC discovery document and extracts
// the authorization and token endpoints.
func DiscoverOIDCEndpoints(issuerURL string) (authorizeURL string, tokenURL string, discoveryError error) {
	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	httpClient := &http.Client{Timeout: defaultJWKSFetchTimeout}

	httpResponse, requestError := httpClient.Get(discoveryURL) //nolint:gosec // fetching from configured OIDC issuer URL
	if requestError != nil {
		return "", "", fmt.Errorf("fetch OIDC discovery: %w", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("OIDC discovery: HTTP %d", httpResponse.StatusCode)
	}

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return "", "", fmt.Errorf("OIDC discovery: read response: %w", readError)
	}

	var discoveryDocument struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if unmarshalError := json.Unmarshal(responseBody, &discoveryDocument); unmarshalError != nil {
		return "", "", fmt.Errorf("OIDC discovery: parse: %w", unmarshalError)
	}
	if discoveryDocument.AuthorizationEndpoint == "" {
		return "", "", fmt.Errorf("OIDC discovery: missing authorization_endpoint")
	}
	if discoveryDocument.TokenEndpoint == "" {
		return "", "", fmt.Errorf("OIDC discovery: missing token_endpoint")
	}

	return discoveryDocument.AuthorizationEndpoint, discoveryDocument.TokenEndpoint, nil
}
