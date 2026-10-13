// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOIDCDiscoveryClientFetchAndCacheKeys(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create issuer: %v", issuerError)
	}

	jwksDoc := workloadIssuer.JWKSDocument()
	discoveryDoc := workloadIssuer.OIDCDiscoveryDocument()

	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryDoc.JWKSURI = "http://" + request.Host + "/oidc/jwks"
			_ = json.NewEncoder(responseWriter).Encode(discoveryDoc)
		case "/oidc/jwks":
			_ = json.NewEncoder(responseWriter).Encode(jwksDoc)
		default:
			http.NotFound(responseWriter, request)
		}
	}))
	defer testServer.Close()

	discoveryClient := NewOIDCDiscoveryClient(testServer.URL)
	fetchError := discoveryClient.FetchAndCacheKeys()
	if fetchError != nil {
		t.Fatalf("fetch keys: %v", fetchError)
	}

	if discoveryClient.KeyCount() != 1 {
		t.Errorf("key count = %d, want 1", discoveryClient.KeyCount())
	}

	publicKey := discoveryClient.PublicKeyByID(workloadIssuer.KeyID())
	if publicKey == nil {
		t.Fatal("expected to find public key by ID")
	}

	firstKey := discoveryClient.FirstPublicKey()
	if firstKey == nil {
		t.Fatal("expected non-nil first public key")
	}
}

func TestOIDCDiscoveryClientMissingJWKSURI(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"issuer": "https://example.com"})
	}))
	defer testServer.Close()

	discoveryClient := NewOIDCDiscoveryClient(testServer.URL)
	fetchError := discoveryClient.FetchAndCacheKeys()
	if fetchError == nil {
		t.Fatal("expected error for missing jwks_uri")
	}
}

func TestOIDCDiscoveryClientUnreachableServer(t *testing.T) {
	discoveryClient := NewOIDCDiscoveryClient("http://127.0.0.1:1")
	fetchError := discoveryClient.FetchAndCacheKeys()
	if fetchError == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestOIDCDiscoveryClientKeyNotFound(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(responseWriter).Encode(map[string]string{
				"issuer":   "https://example.com",
				"jwks_uri": "http://" + request.Host + "/oidc/jwks",
			})
		case "/oidc/jwks":
			_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"keys": []interface{}{}})
		}
	}))
	defer testServer.Close()

	discoveryClient := NewOIDCDiscoveryClient(testServer.URL)
	_ = discoveryClient.FetchAndCacheKeys()

	publicKey := discoveryClient.PublicKeyByID("nonexistent-kid")
	if publicKey != nil {
		t.Error("expected nil for nonexistent key ID")
	}
}

func TestParseJWKEntryValidEC(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create issuer: %v", issuerError)
	}

	jwksDoc := workloadIssuer.JWKSDocument()
	jwkJSON, _ := json.Marshal(jwksDoc.Keys[0])
	var jwkMap map[string]interface{}
	_ = json.Unmarshal(jwkJSON, &jwkMap)

	publicKey, keyID, parseError := parseJWKEntry(jwkMap)
	if parseError != nil {
		t.Fatalf("parse JWK: %v", parseError)
	}
	if publicKey == nil {
		t.Fatal("expected non-nil public key")
	}
	if keyID != workloadIssuer.KeyID() {
		t.Errorf("kid = %q, want %q", keyID, workloadIssuer.KeyID())
	}
}

func TestParseJWKEntryUnsupportedKeyType(t *testing.T) {
	_, _, parseError := parseJWKEntry(map[string]interface{}{"kty": "RSA"})
	if parseError == nil {
		t.Fatal("expected error for unsupported key type")
	}
}
