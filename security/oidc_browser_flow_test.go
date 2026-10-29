// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCECodePair(t *testing.T) {
	codeVerifier, codeChallenge, generateError := generatePKCECodePair()
	if generateError != nil {
		t.Fatalf("generate PKCE pair: %v", generateError)
	}
	if len(codeVerifier) < 43 {
		t.Errorf("code verifier too short: %d chars", len(codeVerifier))
	}
	if codeChallenge == "" {
		t.Fatal("empty code challenge")
	}
	if codeVerifier == codeChallenge {
		t.Error("code verifier and challenge should differ")
	}
}

func TestGenerateRandomState(t *testing.T) {
	stateOne, errorOne := generateRandomState()
	stateTwo, errorTwo := generateRandomState()
	if errorOne != nil || errorTwo != nil {
		t.Fatalf("generate state: %v, %v", errorOne, errorTwo)
	}
	if stateOne == stateTwo {
		t.Error("two random states should differ")
	}
}

func TestExecuteOIDCBrowserFlowSuccess(t *testing.T) {
	mockIdP := newMockOIDCProvider(t)
	defer mockIdP.close()

	flowConfig := OIDCBrowserFlowConfig{
		AuthorizeURL: mockIdP.authorizeURL(),
		TokenURL:     mockIdP.tokenURL(),
		ClientID:     "test-client-id",
	}

	openBrowserFunc := func(authorizeURL string) error {
		parsedURL, parseError := url.Parse(authorizeURL)
		if parseError != nil {
			return parseError
		}
		queryParams := parsedURL.Query()
		if queryParams.Get("response_type") != "code" {
			t.Errorf("expected response_type=code, got %q", queryParams.Get("response_type"))
		}
		if queryParams.Get("client_id") != "test-client-id" {
			t.Errorf("expected client_id=test-client-id, got %q", queryParams.Get("client_id"))
		}
		if queryParams.Get("code_challenge_method") != "S256" {
			t.Errorf("expected code_challenge_method=S256, got %q", queryParams.Get("code_challenge_method"))
		}
		redirectURI := queryParams.Get("redirect_uri")
		stateToken := queryParams.Get("state")
		go func() {
			callbackURL := fmt.Sprintf("%s?code=test-auth-code&state=%s", redirectURI, stateToken)
			httpClient := &http.Client{Timeout: 5 * time.Second}
			callbackResponse, callbackError := httpClient.Get(callbackURL) //nolint:gosec // test URL
			if callbackError != nil {
				t.Errorf("callback request failed: %v", callbackError)
				return
			}
			_ = callbackResponse.Body.Close()
		}()
		return nil
	}

	result, flowError := ExecuteOIDCBrowserFlow(context.Background(), flowConfig, openBrowserFunc)
	if flowError != nil {
		t.Fatalf("browser flow: %v", flowError)
	}
	if result.AccessToken != "mock-access-token" {
		t.Errorf("access_token = %q, want %q", result.AccessToken, "mock-access-token")
	}
	if result.IDToken != "mock-id-token" {
		t.Errorf("id_token = %q, want %q", result.IDToken, "mock-id-token")
	}
	if result.RefreshToken != "mock-refresh-token" {
		t.Errorf("refresh_token = %q, want %q", result.RefreshToken, "mock-refresh-token")
	}
	if result.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want 3600", result.ExpiresIn)
	}
}

func TestExecuteOIDCBrowserFlowIdPError(t *testing.T) {
	mockIdP := newMockOIDCProvider(t)
	defer mockIdP.close()

	flowConfig := OIDCBrowserFlowConfig{
		AuthorizeURL: mockIdP.authorizeURL(),
		TokenURL:     mockIdP.tokenURL(),
		ClientID:     "test-client-id",
	}

	openBrowserFunc := func(authorizeURL string) error {
		parsedURL, _ := url.Parse(authorizeURL)
		queryParams := parsedURL.Query()
		redirectURI := queryParams.Get("redirect_uri")
		stateToken := queryParams.Get("state")
		go func() {
			callbackURL := fmt.Sprintf("%s?error=access_denied&error_description=user+denied&state=%s", redirectURI, stateToken)
			httpClient := &http.Client{Timeout: 5 * time.Second}
			callbackResponse, _ := httpClient.Get(callbackURL) //nolint:gosec // test URL
			if callbackResponse != nil {
				_ = callbackResponse.Body.Close()
			}
		}()
		return nil
	}

	_, flowError := ExecuteOIDCBrowserFlow(context.Background(), flowConfig, openBrowserFunc)
	if flowError == nil {
		t.Fatal("expected error for IdP error callback")
	}
	if !strings.Contains(flowError.Error(), "access_denied") {
		t.Errorf("error should mention access_denied: %v", flowError)
	}
}

func TestExecuteOIDCBrowserFlowTimeout(t *testing.T) {
	mockIdP := newMockOIDCProvider(t)
	defer mockIdP.close()

	flowConfig := OIDCBrowserFlowConfig{
		AuthorizeURL: mockIdP.authorizeURL(),
		TokenURL:     mockIdP.tokenURL(),
		ClientID:     "test-client-id",
	}

	timeoutContext, cancelFunc := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelFunc()

	_, flowError := ExecuteOIDCBrowserFlow(timeoutContext, flowConfig, func(string) error {
		return nil
	})
	if flowError == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(flowError.Error(), "timed out") {
		t.Errorf("error should mention timeout: %v", flowError)
	}
}

func TestRefreshOIDCTokenSuccess(t *testing.T) {
	mockIdP := newMockOIDCProvider(t)
	defer mockIdP.close()

	result, refreshError := RefreshOIDCToken(mockIdP.tokenURL(), "test-client-id", "old-refresh-token")
	if refreshError != nil {
		t.Fatalf("refresh: %v", refreshError)
	}
	if result.AccessToken != "mock-access-token" {
		t.Errorf("access_token = %q, want %q", result.AccessToken, "mock-access-token")
	}
}

func TestRefreshOIDCTokenError(t *testing.T) {
	errorServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "refresh token expired",
		})
	}))
	defer errorServer.Close()

	_, refreshError := RefreshOIDCToken(errorServer.URL+"/token", "test-client-id", "expired-token")
	if refreshError == nil {
		t.Fatal("expected error for expired refresh token")
	}
}

func TestRefreshOIDCTokenKeepsOldRefreshToken(t *testing.T) {
	noRefreshServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"access_token": "new-access-token",
			"token_type":   "Bearer",
			"expires_in":   1800,
		})
	}))
	defer noRefreshServer.Close()

	result, refreshError := RefreshOIDCToken(noRefreshServer.URL+"/token", "test-client-id", "original-refresh")
	if refreshError != nil {
		t.Fatalf("refresh: %v", refreshError)
	}
	if result.RefreshToken != "original-refresh" {
		t.Errorf("refresh_token = %q, want %q (should keep original)", result.RefreshToken, "original-refresh")
	}
}

func TestDiscoverOIDCEndpoints(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(responseWriter, request)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{ //nolint:gosec // test data
			"authorization_endpoint": "https://idp.example.com/authorize",
			"token_endpoint":         "https://idp.example.com/token",
			"jwks_uri":               "https://idp.example.com/jwks",
		})
	}))
	defer discoveryServer.Close()

	authorizeEndpoint, tokenEndpoint, discoveryError := DiscoverOIDCEndpoints(discoveryServer.URL)
	if discoveryError != nil {
		t.Fatalf("discover: %v", discoveryError)
	}
	if authorizeEndpoint != "https://idp.example.com/authorize" {
		t.Errorf("authorize = %q", authorizeEndpoint)
	}
	if tokenEndpoint != "https://idp.example.com/token" { //nolint:gosec // test assertion
		t.Errorf("token = %q", tokenEndpoint)
	}
}

func TestDiscoverOIDCEndpointsMissingFields(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{
			"issuer": "https://idp.example.com",
		})
	}))
	defer discoveryServer.Close()

	_, _, discoveryError := DiscoverOIDCEndpoints(discoveryServer.URL)
	if discoveryError == nil {
		t.Fatal("expected error for missing endpoints")
	}
}

func TestExchangeAuthorizationCodeValidatesVerifier(t *testing.T) {
	var receivedVerifier string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		parsedForm, _ := url.ParseQuery(string(bodyBytes))
		receivedVerifier = parsedForm.Get("code_verifier")
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"access_token": "test-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	config := OIDCBrowserFlowConfig{
		TokenURL: tokenServer.URL + "/token",
		ClientID: "test-client",
	}

	_, exchangeError := exchangeAuthorizationCode(config, "test-code", "http://127.0.0.1:0/callback", "test-verifier-value")
	if exchangeError != nil {
		t.Fatalf("exchange: %v", exchangeError)
	}
	if receivedVerifier != "test-verifier-value" {
		t.Errorf("code_verifier = %q, want %q", receivedVerifier, "test-verifier-value")
	}
}

// mockOIDCProvider simulates an external OIDC identity provider for testing.
type mockOIDCProvider struct {
	server *httptest.Server
}

// newMockOIDCProvider creates a test OIDC provider that serves discovery,
// authorization, and token endpoints.
func newMockOIDCProvider(t *testing.T) *mockOIDCProvider {
	t.Helper()
	provider := &mockOIDCProvider{}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{
			"issuer":                 provider.server.URL,
			"authorization_endpoint": provider.server.URL + "/authorize",
			"token_endpoint":         provider.server.URL + "/token",
		})
	})
	mux.HandleFunc("/token", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			responseWriter.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"access_token":  "mock-access-token",
			"id_token":      "mock-id-token",
			"refresh_token": "mock-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})

	provider.server = httptest.NewServer(mux)
	return provider
}

// authorizeURL returns the mock provider's authorization endpoint.
func (provider *mockOIDCProvider) authorizeURL() string {
	return provider.server.URL + "/authorize"
}

// tokenURL returns the mock provider's token endpoint.
func (provider *mockOIDCProvider) tokenURL() string {
	return provider.server.URL + "/token"
}

// close shuts down the mock provider.
func (provider *mockOIDCProvider) close() {
	provider.server.Close()
}
