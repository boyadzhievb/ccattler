// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newLocalUserTestAuthenticator returns the internal user-token authenticator
// wrapped by the user-record check over a memory store, and the issuer.
func newLocalUserTestAuthenticator(t *testing.T) (Authenticator, *UserTokenIssuer, store.StateStore) {
	t.Helper()
	workloadIssuer, issuerError := NewWorkloadTokenIssuer("https://cca.test")
	if issuerError != nil {
		t.Fatal(issuerError)
	}
	internalConfig := OIDCConfig{Issuer: workloadIssuer.IssuerURL(), Audience: "ccattler", ClaimMapping: ClaimMapping{PrincipalClaim: "sub", TeamClaim: "team"}}
	internalAuthenticator := NewBearerTokenAuthenticator(NewOIDCAuthenticator(internalConfig, workloadIssuer.PublicKey()))
	factStore := store.NewMemoryStore()
	return NewLocalUserRecordAuthenticator(internalAuthenticator, factStore), NewUserTokenIssuer(workloadIssuer), factStore
}

// authenticateUserToken runs the authenticator on a request carrying token.
func authenticateUserToken(authenticator Authenticator, token string) (*AuthenticationResult, error) {
	request := httptest.NewRequest("GET", "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return authenticator.Authenticate(request)
}

// putUserRecord writes a user record and returns its store revision, which a
// token minted for it carries.
func putUserRecord(t *testing.T, factStore store.StateStore, userName string, recordJSON string) int64 {
	t.Helper()
	revision, putError := factStore.Put(context.Background(), types.KeyAuthUser(userName), []byte(recordJSON))
	if putError != nil {
		t.Fatal(putError)
	}
	return revision
}

// TestLocalUserTokenRefusedAfterUserDeleted verifies an internal user token
// stops authenticating once the user record is deleted, stays refused after
// the user is re-created under the same name (in the same second too: no
// clock is compared), and a token without the revision claim is refused (M216,
// found in M214 run 1: a deleted user's token still authenticated).
func TestLocalUserTokenRefusedAfterUserDeleted(t *testing.T) {
	authenticator, userIssuer, factStore := newLocalUserTestAuthenticator(t)
	firstRevision := putUserRecord(t, factStore, "alice", `{"team":"payments"}`)
	oldToken, _ := userIssuer.MintUserToken("alice", "payments", nil, time.Hour, firstRevision)

	if result, authError := authenticateUserToken(authenticator, oldToken); authError != nil || result == nil || result.Principal != "user:alice" {
		t.Fatalf("existing user: result %+v error %v", result, authError)
	}

	_ = factStore.Delete(context.Background(), types.KeyAuthUser("alice"))
	if result, authError := authenticateUserToken(authenticator, oldToken); authError == nil {
		t.Fatalf("deleted user still authenticates: %+v", result)
	}

	secondRevision := putUserRecord(t, factStore, "alice", `{"team":"payments"}`)
	if result, authError := authenticateUserToken(authenticator, oldToken); authError == nil {
		t.Fatalf("token from before the re-creation still authenticates: %+v", result)
	}
	newToken, _ := userIssuer.MintUserToken("alice", "payments", nil, time.Hour, secondRevision)
	if result, authError := authenticateUserToken(authenticator, newToken); authError != nil || result == nil {
		t.Fatalf("token for the re-created user: result %+v error %v", result, authError)
	}
	claimlessToken, _ := userIssuer.MintUserToken("alice", "payments", nil, time.Hour, 0)
	if result, authError := authenticateUserToken(authenticator, claimlessToken); authError == nil {
		t.Fatalf("token without the record revision claim accepted: %+v", result)
	}
}

// TestLocalUserRecordCheckFailsClosedAndPassesOtherCredentials verifies a
// store error refuses the token, and a request without a bearer token is
// left to the next authenticator (nil, nil).
func TestLocalUserRecordCheckFailsClosedAndPassesOtherCredentials(t *testing.T) {
	authenticator, userIssuer, factStore := newLocalUserTestAuthenticator(t)
	bobRevision := putUserRecord(t, factStore, "bob", `{}`)
	token, _ := userIssuer.MintUserToken("bob", "", nil, time.Hour, bobRevision)
	_ = factStore.Close()
	if result, authError := authenticateUserToken(authenticator, token); authError == nil {
		t.Fatalf("store error: token accepted: %+v", result)
	}
	if result, authError := authenticator.Authenticate(httptest.NewRequest("GET", "/api/status", nil)); result != nil || authError != nil {
		t.Fatalf("no credential: result %+v error %v, want nil, nil", result, authError)
	}
}
