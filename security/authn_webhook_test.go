// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookAuthenticatorSuccess(t *testing.T) {
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", request.Method)
		}
		var webhookReq webhookRequest
		if decodeError := json.NewDecoder(request.Body).Decode(&webhookReq); decodeError != nil {
			t.Fatalf("decode request: %v", decodeError)
		}
		if webhookReq.Token != "valid-token-123" {
			responseWriter.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
				Authenticated: false,
				Error:         "invalid token",
			})
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
			Authenticated: true,
			Principal:     "user:alice@example.com",
			Groups:        []string{"developers", "team-platform"},
			Attributes:    map[string]string{"team": "platform", "role": "admin"},
		})
	}))
	defer webhookServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: webhookServer.URL,
	})

	validRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	validRequest.Header.Set("Authorization", "Bearer valid-token-123")

	result, authError := authenticator.Authenticate(validRequest)
	if authError != nil {
		t.Fatalf("authenticate: %v", authError)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Principal != "user:alice@example.com" {
		t.Errorf("principal = %q, want %q", result.Principal, "user:alice@example.com")
	}
	if len(result.Groups) != 2 {
		t.Errorf("groups count = %d, want 2", len(result.Groups))
	}
	if len(result.Attributes) != 2 {
		t.Errorf("attributes count = %d, want 2", len(result.Attributes))
	}
}

func TestWebhookAuthenticatorInvalidToken(t *testing.T) {
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
			Authenticated: false,
			Error:         "token expired",
		})
	}))
	defer webhookServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: webhookServer.URL,
	})

	invalidRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	invalidRequest.Header.Set("Authorization", "Bearer expired-token")

	result, authError := authenticator.Authenticate(invalidRequest)
	if authError == nil {
		t.Fatal("expected error for invalid token")
	}
	if result != nil {
		t.Error("expected nil result for invalid token")
	}
}

func TestWebhookAuthenticatorNoAuthHeader(t *testing.T) {
	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: "http://localhost:0/webhook",
	})

	noAuthRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	result, authError := authenticator.Authenticate(noAuthRequest)
	if authError != nil {
		t.Fatalf("unexpected error: %v", authError)
	}
	if result != nil {
		t.Error("expected nil result when no auth header (chain passthrough)")
	}
}

func TestWebhookAuthenticatorNonBearerHeader(t *testing.T) {
	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: "http://localhost:0/webhook",
	})

	basicAuthRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	basicAuthRequest.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	result, authError := authenticator.Authenticate(basicAuthRequest)
	if authError != nil {
		t.Fatalf("unexpected error: %v", authError)
	}
	if result != nil {
		t.Error("expected nil result for non-bearer auth (chain passthrough)")
	}
}

func TestWebhookAuthenticatorWebhookDown(t *testing.T) {
	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: "http://127.0.0.1:1/webhook",
		Timeout:     500 * time.Millisecond,
	})

	failRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	failRequest.Header.Set("Authorization", "Bearer some-token")

	result, authError := authenticator.Authenticate(failRequest)
	if authError == nil {
		t.Fatal("expected error when webhook is unreachable (fail-closed)")
	}
	if result != nil {
		t.Error("expected nil result when webhook is unreachable")
	}
}

func TestWebhookAuthenticatorHTTPErrorCode(t *testing.T) {
	errorServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusInternalServerError)
	}))
	defer errorServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: errorServer.URL,
	})

	errorRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	errorRequest.Header.Set("Authorization", "Bearer some-token")

	_, authError := authenticator.Authenticate(errorRequest)
	if authError == nil {
		t.Fatal("expected error for HTTP 500 response")
	}
}

func TestWebhookAuthenticatorResponseCaching(t *testing.T) {
	callCount := 0
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		callCount++
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
			Authenticated: true,
			Principal:     "user:cached",
		})
	}))
	defer webhookServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: webhookServer.URL,
		CacheTTL:    10 * time.Second,
	})

	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	cachedRequest.Header.Set("Authorization", "Bearer cache-me")

	firstResult, firstError := authenticator.Authenticate(cachedRequest)
	if firstError != nil {
		t.Fatalf("first call: %v", firstError)
	}
	if firstResult.Principal != "user:cached" {
		t.Errorf("first principal = %q", firstResult.Principal)
	}

	secondResult, secondError := authenticator.Authenticate(cachedRequest)
	if secondError != nil {
		t.Fatalf("second call: %v", secondError)
	}
	if secondResult.Principal != "user:cached" {
		t.Errorf("second principal = %q", secondResult.Principal)
	}

	if callCount != 1 {
		t.Errorf("webhook called %d times, want 1 (second should be cached)", callCount)
	}
	if authenticator.CacheSize() != 1 {
		t.Errorf("cache size = %d, want 1", authenticator.CacheSize())
	}
}

func TestWebhookAuthenticatorCacheExpiry(t *testing.T) {
	callCount := 0
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		callCount++
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
			Authenticated: true,
			Principal:     "user:expiring",
		})
	}))
	defer webhookServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: webhookServer.URL,
		CacheTTL:    50 * time.Millisecond,
	})

	expiryRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	expiryRequest.Header.Set("Authorization", "Bearer expire-me")

	_, _ = authenticator.Authenticate(expiryRequest)
	time.Sleep(100 * time.Millisecond)
	_, _ = authenticator.Authenticate(expiryRequest)

	if callCount != 2 {
		t.Errorf("webhook called %d times, want 2 (cache should have expired)", callCount)
	}
}

func TestWebhookAuthenticatorEmptyPrincipal(t *testing.T) {
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{
			Authenticated: true,
			Principal:     "",
		})
	}))
	defer webhookServer.Close()

	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{
		EndpointURL: webhookServer.URL,
	})

	emptyRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	emptyRequest.Header.Set("Authorization", "Bearer empty-principal")

	_, authError := authenticator.Authenticate(emptyRequest)
	if authError == nil {
		t.Fatal("expected error for empty principal from webhook")
	}
}
