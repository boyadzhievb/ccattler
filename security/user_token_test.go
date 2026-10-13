// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"testing"
	"time"
)

func TestMintUserTokenRoundTrip(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create workload issuer: %v", issuerError)
	}
	userIssuer := NewUserTokenIssuer(workloadIssuer)

	tokenString, mintError := userIssuer.MintUserToken("alice", "payments", []string{"developers"}, 8*time.Hour)
	if mintError != nil {
		t.Fatalf("mint user token: %v", mintError)
	}
	if tokenString == "" {
		t.Fatal("expected non-empty token")
	}

	oidcAuth := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.test",
		Audience: userTokenAudience,
		ClaimMapping: ClaimMapping{
			PrincipalClaim: "sub",
			TeamClaim:      "team",
		},
	}, userIssuer.PublicKey())

	authResult, validateError := oidcAuth.Authenticate(tokenString)
	if validateError != nil {
		t.Fatalf("validate user token: %v", validateError)
	}
	if authResult == nil {
		t.Fatal("expected non-nil auth result")
	}
	if authResult.Principal != "user:alice" {
		t.Errorf("principal = %q, want %q", authResult.Principal, "user:alice")
	}
	if authResult.PrincipalKind != PrincipalKindUser {
		t.Errorf("principal kind = %q, want %q", authResult.PrincipalKind, PrincipalKindUser)
	}

	foundTeam := false
	for _, attribute := range authResult.Attributes {
		if attribute.Key == "team" && attribute.Value == "payments" {
			foundTeam = true
		}
	}
	if !foundTeam {
		t.Errorf("expected team=payments attribute in %v", authResult.Attributes)
	}
}

func TestMintUserTokenWithoutTeam(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create workload issuer: %v", issuerError)
	}
	userIssuer := NewUserTokenIssuer(workloadIssuer)

	tokenString, mintError := userIssuer.MintUserToken("bob", "", nil, 0)
	if mintError != nil {
		t.Fatalf("mint user token: %v", mintError)
	}

	oidcAuth := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.test",
		Audience: userTokenAudience,
	}, userIssuer.PublicKey())

	authResult, validateError := oidcAuth.Authenticate(tokenString)
	if validateError != nil {
		t.Fatalf("validate user token: %v", validateError)
	}
	if authResult.Principal != "user:bob" {
		t.Errorf("principal = %q, want %q", authResult.Principal, "user:bob")
	}
}

func TestMintUserTokenEmptyUsername(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create workload issuer: %v", issuerError)
	}
	userIssuer := NewUserTokenIssuer(workloadIssuer)

	_, mintError := userIssuer.MintUserToken("", "payments", nil, time.Hour)
	if mintError == nil {
		t.Fatal("expected error for empty username")
	}
}

func TestMintUserTokenDefaultTTL(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create workload issuer: %v", issuerError)
	}
	userIssuer := NewUserTokenIssuer(workloadIssuer)

	tokenString, mintError := userIssuer.MintUserToken("carol", "frontend", nil, 0)
	if mintError != nil {
		t.Fatalf("mint user token: %v", mintError)
	}

	oidcAuth := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.test",
		Audience: userTokenAudience,
	}, userIssuer.PublicKey())

	authResult, validateError := oidcAuth.Authenticate(tokenString)
	if validateError != nil {
		t.Fatalf("validate user token: %v", validateError)
	}

	expectedExpiry := time.Now().Add(defaultUserTokenTTL)
	timeDiff := authResult.ExpiresAt.Sub(expectedExpiry)
	if timeDiff < -5*time.Second || timeDiff > 5*time.Second {
		t.Errorf("expiry off by %v from expected default TTL", timeDiff)
	}
}

func TestUserTokenIssuerWithExistingKey(t *testing.T) {
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://ccattler.test")
	if issuerError != nil {
		t.Fatalf("create workload issuer: %v", issuerError)
	}

	userIssuer := NewUserTokenIssuerWithKey("https://ccattler.test", workloadIssuer.signingKey)

	tokenString, mintError := userIssuer.MintUserToken("dave", "platform", []string{"admins", "ops"}, time.Hour)
	if mintError != nil {
		t.Fatalf("mint user token: %v", mintError)
	}

	oidcAuth := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.test",
		Audience: userTokenAudience,
	}, userIssuer.PublicKey())

	authResult, validateError := oidcAuth.Authenticate(tokenString)
	if validateError != nil {
		t.Fatalf("validate: %v", validateError)
	}
	if authResult.Principal != "user:dave" {
		t.Errorf("principal = %q, want %q", authResult.Principal, "user:dave")
	}
}
