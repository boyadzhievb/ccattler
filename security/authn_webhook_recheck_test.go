// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Webhook behaviour that open-stream re-checks rely on (M216 review SA-4).

// TestWebhookRefusalStatusIsNotUnavailable: a 4xx answer is the IdP refusing
// the token, not an outage, so an open stream must not tolerate it; a 5xx
// stays unavailable.
func TestWebhookRefusalStatusIsNotUnavailable(t *testing.T) {
	for statusCode, wantUnavailable := range map[int]bool{
		http.StatusUnauthorized: false, http.StatusForbidden: false,
		http.StatusInternalServerError: true, http.StatusBadGateway: true,
	} {
		webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
			responseWriter.WriteHeader(statusCode)
		}))
		authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{EndpointURL: webhookServer.URL})
		tokenRequest := httptest.NewRequest(http.MethodGet, "/api/watch", nil)
		tokenRequest.Header.Set("Authorization", "Bearer revoked-token")
		_, authError := authenticator.Authenticate(tokenRequest)
		webhookServer.Close()
		if authError == nil {
			t.Fatalf("HTTP %d from the webhook authenticated the token", statusCode)
		}
		if gotUnavailable := errors.Is(authError, ErrAuthenticationUnavailable); gotUnavailable != wantUnavailable {
			t.Errorf("HTTP %d: unavailable = %v, want %v (%v)", statusCode, gotUnavailable, wantUnavailable, authError)
		}
	}
}

// TestWebhookFreshAuthenticationSkipsCache: a stream re-check asks the
// webhook again, so a token revoked at the IdP is refused at the next
// re-check, not after the 2-minute cache entry expires.
func TestWebhookFreshAuthenticationSkipsCache(t *testing.T) {
	var webhookCalls atomic.Int64
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		webhookCalls.Add(1)
		_ = json.NewEncoder(responseWriter).Encode(webhookResponse{Authenticated: true, Principal: "user:carol"})
	}))
	defer webhookServer.Close()
	authenticator := NewWebhookAuthenticator(WebhookAuthenticatorConfig{EndpointURL: webhookServer.URL, CacheTTL: time.Hour})
	tokenRequest := httptest.NewRequest(http.MethodGet, "/api/watch", nil)
	tokenRequest.Header.Set("Authorization", "Bearer carol-token")
	for range 2 {
		if _, authError := authenticator.Authenticate(tokenRequest); authError != nil {
			t.Fatal(authError)
		}
	}
	if calls := webhookCalls.Load(); calls != 1 {
		t.Fatalf("plain requests called the webhook %d times, want 1 (cached)", calls)
	}
	recheckRequest := tokenRequest.WithContext(WithFreshAuthentication(tokenRequest.Context()))
	if _, authError := authenticator.Authenticate(recheckRequest); authError != nil {
		t.Fatal(authError)
	}
	if calls := webhookCalls.Load(); calls != 2 {
		t.Errorf("a fresh re-check called the webhook %d times in all, want 2 (cache skipped)", calls)
	}
}
