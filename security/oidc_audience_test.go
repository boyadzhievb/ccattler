// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"strings"
	"testing"
	"time"
)

const (
	// oidcAudienceTestIssuer is the issuer of the tokens minted in these tests.
	oidcAudienceTestIssuer = "https://auth.example.com"
	// oidcAudienceTestClientID is the audience CCattler is configured to expect.
	oidcAudienceTestClientID = "ccattler-client"
)

// authenticateWithAudienceClaim mints a valid token whose "aud" claim is
// audienceClaim and authenticates it against an authenticator that expects
// configuredAudience.
func authenticateWithAudienceClaim(testHandle *testing.T, configuredAudience string, audienceClaim interface{}) error {
	testHandle.Helper()
	privateKey, keyError := GenerateOIDCKeyPair()
	if keyError != nil {
		testHandle.Fatalf("generate key: %v", keyError)
	}
	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer:       oidcAudienceTestIssuer,
		Audience:     configuredAudience,
		ClaimMapping: ClaimMapping{PrincipalClaim: "sub", TeamClaim: "team"},
	}, &privateKey.PublicKey)
	tokenClaims := map[string]interface{}{
		"iss": oidcAudienceTestIssuer,
		"sub": "alice",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	if audienceClaim != nil {
		tokenClaims["aud"] = audienceClaim
	}
	signedToken, mintError := CreateTestJWT(privateKey, tokenClaims)
	if mintError != nil {
		testHandle.Fatalf("create JWT: %v", mintError)
	}
	_, authenticateError := authenticator.Authenticate(signedToken)
	return authenticateError
}

// TestOIDCRejectsEveryTokenWithoutConfiguredAudience is the M228 bug at its
// source: an authenticator with no expected audience must accept nothing,
// not skip the audience check.
func TestOIDCRejectsEveryTokenWithoutConfiguredAudience(testHandle *testing.T) {
	for _, audienceClaim := range []interface{}{nil, "", "some-other-application", oidcAudienceTestClientID} {
		authenticateError := authenticateWithAudienceClaim(testHandle, "", audienceClaim)
		if authenticateError == nil || !strings.Contains(authenticateError.Error(), "audience") {
			testHandle.Errorf("no configured audience, token aud %v: got %v, want an audience error", audienceClaim, authenticateError)
		}
	}
}

// TestOIDCAudienceClaimAsStringOrList checks the two forms RFC 7519 allows for
// "aud": one string, or a list that must contain the expected audience.
func TestOIDCAudienceClaimAsStringOrList(testHandle *testing.T) {
	acceptedAudienceClaims := []interface{}{
		oidcAudienceTestClientID,
		[]interface{}{oidcAudienceTestClientID},
		[]interface{}{"some-other-application", oidcAudienceTestClientID},
	}
	for _, audienceClaim := range acceptedAudienceClaims {
		if authenticateError := authenticateWithAudienceClaim(testHandle, oidcAudienceTestClientID, audienceClaim); authenticateError != nil {
			testHandle.Errorf("token aud %v: got %v, want accepted", audienceClaim, authenticateError)
		}
	}
	refusedAudienceClaims := []interface{}{
		nil, "", "some-other-application",
		[]interface{}{}, []interface{}{"some-other-application"}, []interface{}{float64(1), true},
		float64(1), map[string]interface{}{oidcAudienceTestClientID: true},
	}
	for _, audienceClaim := range refusedAudienceClaims {
		authenticateError := authenticateWithAudienceClaim(testHandle, oidcAudienceTestClientID, audienceClaim)
		if authenticateError == nil || !strings.Contains(authenticateError.Error(), "audience") {
			testHandle.Errorf("token aud %v: got %v, want an audience error", audienceClaim, authenticateError)
		}
	}
}
