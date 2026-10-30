// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultWebhookTimeout is the HTTP timeout for webhook authentication requests.
const defaultWebhookTimeout = 5 * time.Second

// defaultWebhookCacheTTL is how long a successful webhook response is cached
// before re-validating with the external endpoint.
const defaultWebhookCacheTTL = 2 * time.Minute

// WebhookAuthenticatorConfig holds configuration for the webhook authenticator.
type WebhookAuthenticatorConfig struct {
	EndpointURL string        // HTTPS URL to POST the token to
	Timeout     time.Duration // HTTP request timeout (0 = default 5s)
	CacheTTL    time.Duration // how long to cache responses (0 = default 2min)
}

// webhookCacheEntry holds a cached authentication result with its expiry.
type webhookCacheEntry struct {
	result    *AuthenticationResult
	expiresAt time.Time
}

// WebhookAuthenticator sends bearer tokens to a configured HTTPS endpoint for
// validation. The external service returns the principal identity, groups, and
// attributes. Responses are cached to reduce latency and load on the webhook
// endpoint. Fail-closed: any error from the webhook rejects the request.
type WebhookAuthenticator struct {
	endpointURL string
	httpClient  *http.Client
	cacheTTL    time.Duration
	cache       map[string]webhookCacheEntry
	cacheMutex  sync.RWMutex
}

// NewWebhookAuthenticator creates a webhook authenticator with the given config.
func NewWebhookAuthenticator(config WebhookAuthenticatorConfig) *WebhookAuthenticator {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultWebhookTimeout
	}
	cacheTTL := config.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultWebhookCacheTTL
	}

	return &WebhookAuthenticator{
		endpointURL: config.EndpointURL,
		httpClient:  &http.Client{Timeout: timeout},
		cacheTTL:    cacheTTL,
		cache:       make(map[string]webhookCacheEntry),
	}
}

// Authenticate extracts the bearer token from the Authorization header and
// sends it to the webhook endpoint. Returns nil result and nil error when no
// bearer token is present (allowing chain fallthrough). Returns an error when
// the token is present but the webhook rejects or cannot reach the endpoint.
func (authenticator *WebhookAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	authorizationHeader := request.Header.Get("Authorization")
	if authorizationHeader == "" {
		return nil, nil
	}
	if !strings.HasPrefix(authorizationHeader, bearerTokenPrefix) {
		return nil, nil
	}
	tokenString := strings.TrimPrefix(authorizationHeader, bearerTokenPrefix)
	if tokenString == "" {
		return nil, nil
	}

	if cachedResult := authenticator.lookupCache(tokenString); cachedResult != nil {
		return cachedResult, nil
	}

	result, validateError := authenticator.callWebhook(tokenString)
	if validateError != nil {
		return nil, fmt.Errorf("webhook: %w", validateError)
	}

	authenticator.storeCache(tokenString, result)
	return result, nil
}

// webhookRequest is the JSON body sent to the webhook endpoint.
type webhookRequest struct {
	Token string `json:"token"` // bearer token to validate
}

// webhookResponse is the expected JSON response from the webhook endpoint.
type webhookResponse struct {
	Authenticated bool              `json:"authenticated"`        // whether the token is valid
	Principal     string            `json:"principal"`            // canonical identity (e.g. "user:alice")
	Groups        []string          `json:"groups,omitempty"`     // group memberships
	Attributes    map[string]string `json:"attributes,omitempty"` // key-value attributes for ABAC
	Error         string            `json:"error,omitempty"`      // error message when not authenticated
}

// callWebhook sends the token to the webhook endpoint and parses the response.
func (authenticator *WebhookAuthenticator) callWebhook(tokenString string) (*AuthenticationResult, error) {
	requestBody, marshalError := json.Marshal(webhookRequest{Token: tokenString})
	if marshalError != nil {
		return nil, fmt.Errorf("marshal request: %w", marshalError)
	}

	httpResponse, requestError := authenticator.httpClient.Post(
		authenticator.endpointURL,
		"application/json",
		bytes.NewReader(requestBody),
	) //nolint:gosec // URL is from server configuration
	if requestError != nil {
		return nil, fmt.Errorf("request failed: %w", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from webhook", httpResponse.StatusCode)
	}

	var response webhookResponse
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&response); decodeError != nil {
		return nil, fmt.Errorf("decode response: %w", decodeError)
	}

	if !response.Authenticated {
		errorMessage := response.Error
		if errorMessage == "" {
			errorMessage = "token rejected"
		}
		return nil, fmt.Errorf("%s", errorMessage)
	}

	if response.Principal == "" {
		return nil, fmt.Errorf("webhook returned empty principal")
	}

	var attributes []Attribute
	for key, value := range response.Attributes {
		attributes = append(attributes, Attribute{Key: key, Value: value})
	}

	return &AuthenticationResult{
		Principal:     response.Principal,
		PrincipalKind: PrincipalKindUser,
		Groups:        response.Groups,
		Attributes:    attributes,
		ExpiresAt:     time.Now().Add(authenticator.cacheTTL),
		AuthMethod:    "webhook",
	}, nil
}

// lookupCache returns a cached authentication result if the token has a valid
// (non-expired) cache entry, or nil otherwise.
func (authenticator *WebhookAuthenticator) lookupCache(tokenString string) *AuthenticationResult {
	authenticator.cacheMutex.RLock()
	defer authenticator.cacheMutex.RUnlock()

	entry, exists := authenticator.cache[tokenString]
	if !exists {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.result
}

// storeCache adds an authentication result to the cache with the configured TTL.
func (authenticator *WebhookAuthenticator) storeCache(tokenString string, result *AuthenticationResult) {
	authenticator.cacheMutex.Lock()
	defer authenticator.cacheMutex.Unlock()

	authenticator.cache[tokenString] = webhookCacheEntry{
		result:    result,
		expiresAt: time.Now().Add(authenticator.cacheTTL),
	}
}

// CacheSize returns the number of entries in the response cache.
func (authenticator *WebhookAuthenticator) CacheSize() int {
	authenticator.cacheMutex.RLock()
	defer authenticator.cacheMutex.RUnlock()
	return len(authenticator.cache)
}
