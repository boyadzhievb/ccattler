// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultJWKSRefreshInterval is how often the JWKS cache is refreshed from
// the external IdP.
const defaultJWKSRefreshInterval = 1 * time.Hour

// defaultJWKSFetchTimeout is the HTTP timeout for fetching OIDC discovery
// documents and JWKS endpoints.
const defaultJWKSFetchTimeout = 10 * time.Second

// OIDCDiscoveryClient fetches and caches OIDC configuration and public keys
// from an external identity provider. The cached JWKS is refreshed periodically
// or on demand when token validation fails.
type OIDCDiscoveryClient struct {
	issuerURL     string                      // base URL of the OIDC provider (e.g. "https://accounts.google.com")
	httpClient    *http.Client                // HTTP client for fetching discovery documents
	cachedKeys    map[string]*ecdsa.PublicKey // kid → public key
	cachedJWKSURL string                      // resolved JWKS endpoint URL
	lastRefresh   time.Time                   // when the JWKS cache was last populated
	mutex         sync.RWMutex                // protects cached fields
}

// NewOIDCDiscoveryClient creates a client for the given OIDC issuer URL.
// Call FetchAndCacheKeys to populate the key cache before use.
func NewOIDCDiscoveryClient(issuerURL string) *OIDCDiscoveryClient {
	return &OIDCDiscoveryClient{
		issuerURL:  strings.TrimRight(issuerURL, "/"),
		httpClient: &http.Client{Timeout: defaultJWKSFetchTimeout},
		cachedKeys: make(map[string]*ecdsa.PublicKey),
	}
}

// FetchAndCacheKeys retrieves the OIDC discovery document and JWKS from the
// external IdP, parsing all EC P-256 public keys into the cache. Returns an
// error if the discovery endpoint or JWKS endpoint is unreachable.
func (oidcDiscoveryClient *OIDCDiscoveryClient) FetchAndCacheKeys() error {
	discoveryURL := oidcDiscoveryClient.issuerURL + "/.well-known/openid-configuration"

	discoveryDoc, fetchError := oidcDiscoveryClient.fetchJSON(discoveryURL)
	if fetchError != nil {
		return fmt.Errorf("fetch OIDC discovery from %s: %w", discoveryURL, fetchError)
	}

	jwksURI, jwksFound := discoveryDoc["jwks_uri"].(string)
	if !jwksFound || jwksURI == "" {
		return fmt.Errorf("OIDC discovery document missing jwks_uri")
	}

	jwksDoc, fetchError := oidcDiscoveryClient.fetchJSON(jwksURI)
	if fetchError != nil {
		return fmt.Errorf("fetch JWKS from %s: %w", jwksURI, fetchError)
	}

	keysRaw, keysFound := jwksDoc["keys"].([]interface{})
	if !keysFound {
		return fmt.Errorf("JWKS document missing keys array")
	}

	parsedKeys := make(map[string]*ecdsa.PublicKey)
	for _, keyRaw := range keysRaw {
		keyMap, isMap := keyRaw.(map[string]interface{})
		if !isMap {
			continue
		}
		publicKey, keyID, parseError := parseJWKEntry(keyMap)
		if parseError != nil {
			continue
		}
		parsedKeys[keyID] = publicKey
	}

	oidcDiscoveryClient.mutex.Lock()
	oidcDiscoveryClient.cachedKeys = parsedKeys
	oidcDiscoveryClient.cachedJWKSURL = jwksURI
	oidcDiscoveryClient.lastRefresh = time.Now()
	oidcDiscoveryClient.mutex.Unlock()

	return nil
}

// PublicKeyByID returns the cached public key for the given key ID, or nil if
// not found. If the cache is stale (older than the refresh interval), a
// background refresh is triggered.
func (oidcDiscoveryClient *OIDCDiscoveryClient) PublicKeyByID(keyID string) *ecdsa.PublicKey {
	oidcDiscoveryClient.mutex.RLock()
	publicKey := oidcDiscoveryClient.cachedKeys[keyID]
	isStale := time.Since(oidcDiscoveryClient.lastRefresh) > defaultJWKSRefreshInterval
	oidcDiscoveryClient.mutex.RUnlock()

	if publicKey == nil && isStale {
		_ = oidcDiscoveryClient.FetchAndCacheKeys()
		oidcDiscoveryClient.mutex.RLock()
		publicKey = oidcDiscoveryClient.cachedKeys[keyID]
		oidcDiscoveryClient.mutex.RUnlock()
	}

	return publicKey
}

// FirstPublicKey returns the first cached public key, or nil if the cache is
// empty. Used when the IdP publishes a single signing key.
func (oidcDiscoveryClient *OIDCDiscoveryClient) FirstPublicKey() *ecdsa.PublicKey {
	oidcDiscoveryClient.mutex.RLock()
	defer oidcDiscoveryClient.mutex.RUnlock()
	for _, publicKey := range oidcDiscoveryClient.cachedKeys {
		return publicKey
	}
	return nil
}

// KeyCount returns the number of public keys currently cached.
func (oidcDiscoveryClient *OIDCDiscoveryClient) KeyCount() int {
	oidcDiscoveryClient.mutex.RLock()
	defer oidcDiscoveryClient.mutex.RUnlock()
	return len(oidcDiscoveryClient.cachedKeys)
}

// fetchJSON makes an HTTP GET request and decodes the response as JSON.
func (oidcDiscoveryClient *OIDCDiscoveryClient) fetchJSON(requestURL string) (map[string]interface{}, error) {
	httpResponse, requestError := oidcDiscoveryClient.httpClient.Get(requestURL) //nolint:gosec // fetching from configured OIDC issuer URL
	if requestError != nil {
		return nil, requestError
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", httpResponse.StatusCode, requestURL)
	}

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return nil, fmt.Errorf("read response body: %w", readError)
	}

	var result map[string]interface{}
	if unmarshalError := json.Unmarshal(responseBody, &result); unmarshalError != nil {
		return nil, fmt.Errorf("decode JSON: %w", unmarshalError)
	}
	return result, nil
}

// parseJWKEntry extracts an ECDSA P-256 public key from a JWK map entry.
// Returns the public key, key ID, and any parsing error. Non-EC or non-P-256
// keys return an error.
func parseJWKEntry(jwkMap map[string]interface{}) (*ecdsa.PublicKey, string, error) {
	keyType, _ := jwkMap["kty"].(string)
	if keyType != "EC" {
		return nil, "", fmt.Errorf("unsupported key type: %s", keyType)
	}

	curveName, _ := jwkMap["crv"].(string)
	if curveName != "P-256" {
		return nil, "", fmt.Errorf("unsupported curve: %s", curveName)
	}

	xEncoded, _ := jwkMap["x"].(string)
	yEncoded, _ := jwkMap["y"].(string)
	keyID, _ := jwkMap["kid"].(string)

	if xEncoded == "" || yEncoded == "" {
		return nil, "", fmt.Errorf("missing x or y coordinate")
	}

	xBytes, decodeError := base64.RawURLEncoding.DecodeString(xEncoded)
	if decodeError != nil {
		return nil, "", fmt.Errorf("decode x coordinate: %w", decodeError)
	}

	yBytes, decodeError := base64.RawURLEncoding.DecodeString(yEncoded)
	if decodeError != nil {
		return nil, "", fmt.Errorf("decode y coordinate: %w", decodeError)
	}

	// SEC 1 uncompressed point: 0x04 || X || Y
	uncompressedPoint := make([]byte, 1+len(xBytes)+len(yBytes))
	uncompressedPoint[0] = 0x04
	copy(uncompressedPoint[1:1+len(xBytes)], xBytes)
	copy(uncompressedPoint[1+len(xBytes):], yBytes)

	publicKey, parseError := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), uncompressedPoint)
	if parseError != nil {
		return nil, "", fmt.Errorf("parse public key: %w", parseError)
	}

	return publicKey, keyID, nil
}
