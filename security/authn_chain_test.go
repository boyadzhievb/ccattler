// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// claimOrderedChain is the server chain order (local token, internal JWT,
// external OIDC, webhook) with a stub webhook that counts calls and accepts
// every token as "user:webhook-user".
type claimOrderedChain struct {
	chain            *AuthenticatorChain  // chain is the chain under test.
	localToken       string               // localToken is the valid local admin token.
	userTokenIssuer  *UserTokenIssuer     // userTokenIssuer mints internal user JWTs.
	externalIssuer   *WorkloadTokenIssuer // externalIssuer mints JWTs for the external OIDC issuer.
	webhookCallCount *atomic.Int64        // webhookCallCount counts requests that reached the webhook.
	webhookServer    *httptest.Server     // webhookServer is the stub webhook endpoint.
}

// newClaimOrderedChain builds the chain with fresh issuers and a stub webhook.
func newClaimOrderedChain(t *testing.T) *claimOrderedChain {
	t.Helper()
	webhookCallCount := &atomic.Int64{}
	webhookServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		webhookCallCount.Add(1)
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"authenticated": true, "principal": "user:webhook-user"})
	}))
	t.Cleanup(webhookServer.Close)

	localAuthenticator, localError := NewLocalTokenAuthenticator(t.TempDir())
	if localError != nil {
		t.Fatal(localError)
	}
	internalIssuer, _ := NewWorkloadTokenIssuer("https://cca.internal")
	externalKey, _ := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	externalIssuer := NewWorkloadTokenIssuerWithKey("https://idp.example.com", externalKey)

	chain := NewAuthenticatorChain(
		localAuthenticator,
		NewBearerTokenAuthenticator(NewOIDCAuthenticator(OIDCConfig{Issuer: "https://cca.internal", Audience: "ccattler"}, internalIssuer.PublicKey())),
		NewBearerTokenAuthenticator(NewOIDCAuthenticator(OIDCConfig{Issuer: "https://idp.example.com", Audience: "ccattler"}, externalKey.Public())),
		NewWebhookAuthenticator(WebhookAuthenticatorConfig{EndpointURL: webhookServer.URL}),
	)
	return &claimOrderedChain{
		chain:            chain,
		localToken:       localAuthenticator.expectedTokenHex,
		userTokenIssuer:  NewUserTokenIssuer(internalIssuer),
		externalIssuer:   externalIssuer,
		webhookCallCount: webhookCallCount,
		webhookServer:    webhookServer,
	}
}

// authenticateBearer runs the chain on a request carrying the bearer token.
func (fixture *claimOrderedChain) authenticateBearer(bearerToken string) (*AuthenticationResult, error) {
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	return fixture.chain.Authenticate(request)
}

func TestAuthenticatorChainLocalTokenNeverReachesWebhook(t *testing.T) {
	fixture := newClaimOrderedChain(t)

	result, authError := fixture.authenticateBearer(fixture.localToken)
	if authError != nil || result.Principal != localTokenPrincipal {
		t.Fatalf("local token: got %v, %v", result, authError)
	}
	if fixture.webhookCallCount.Load() != 0 {
		t.Fatal("the local admin token must never be sent to the webhook")
	}
}

func TestAuthenticatorChainInternalTokenWorksWithExternalOIDC(t *testing.T) {
	fixture := newClaimOrderedChain(t)
	userToken, mintError := fixture.userTokenIssuer.MintUserToken("alice", "", nil, time.Hour, 0)
	if mintError != nil {
		t.Fatal(mintError)
	}

	result, authError := fixture.authenticateBearer(userToken)
	if authError != nil || result.Principal != "user:alice" {
		t.Fatalf("internal user token with external OIDC configured: got %v, %v", result, authError)
	}
	if fixture.webhookCallCount.Load() != 0 {
		t.Fatal("internal user tokens must never be sent to the webhook")
	}
}

func TestAuthenticatorChainExternalTokenReachesItsIssuer(t *testing.T) {
	fixture := newClaimOrderedChain(t)
	externalToken, mintError := fixture.externalIssuer.MintWorkloadToken("bob", "ccattler", time.Hour)
	if mintError != nil {
		t.Fatal(mintError)
	}

	result, authError := fixture.authenticateBearer(externalToken)
	if authError != nil || result.Principal != "user:bob" {
		t.Fatalf("external OIDC token: got %v, %v", result, authError)
	}
}

func TestAuthenticatorChainUnclaimedTokenGoesToWebhook(t *testing.T) {
	fixture := newClaimOrderedChain(t)

	result, authError := fixture.authenticateBearer("opaque-webhook-token")
	if authError != nil || result.Principal != "user:webhook-user" {
		t.Fatalf("opaque token: got %v, %v", result, authError)
	}
	if fixture.webhookCallCount.Load() != 1 {
		t.Fatalf("opaque token must reach the webhook once, got %d calls", fixture.webhookCallCount.Load())
	}
}

func TestAuthenticatorChainRejectsWrongLocalToken(t *testing.T) {
	fixture := newClaimOrderedChain(t)
	wrongLocalToken := "00" + fixture.localToken[2:]
	if wrongLocalToken == fixture.localToken {
		wrongLocalToken = "11" + fixture.localToken[2:]
	}

	if _, authError := fixture.authenticateBearer(wrongLocalToken); authError == nil {
		t.Fatal("a wrong local-shaped token must be rejected")
	}
	if fixture.webhookCallCount.Load() != 0 {
		t.Fatal("a wrong local-shaped token must not be forwarded to the webhook")
	}
}
