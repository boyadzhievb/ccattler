// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// lifecycleAdminAuthenticator authenticates requests without a bearer token
// as the test cluster admin (the operator running cca user/role commands).
type lifecycleAdminAuthenticator struct{}

// Authenticate returns the admin principal for every request it sees.
func (adminAuthenticator *lifecycleAdminAuthenticator) Authenticate(_ *http.Request) (*security.AuthenticationResult, error) {
	return &security.AuthenticationResult{Principal: "user:lifecycle-admin", PrincipalKind: security.PrincipalKindUser}, nil
}

// userLifecycleHarness wires the real user handlers, user-token issuer, the
// LocalUserRecordAuthenticator and the AuthController over one store.
type userLifecycleHarness struct {
	t              *testing.T
	factStore      store.StateStore             // factStore holds users, grants and group facts.
	apiServer      *api.Server                  // apiServer serves /api/user and /api/user/token.
	apiAuthorizer  *security.APIAuthorizer      // apiAuthorizer holds the grants the AuthController builds.
	authController *controllers.AuthController  // authController expands group grants to members.
	authenticator  *security.AuthenticatorChain // authenticator is user tokens first, then the admin fallback.
}

// newUserLifecycleHarness builds the harness; the admin is a bootstrap grant
// that survives the AuthController's grant replacement.
func newUserLifecycleHarness(t *testing.T) *userLifecycleHarness {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	workloadIssuer, issuerError := security.NewWorkloadTokenIssuer("https://cca.lifecycle.test")
	if issuerError != nil {
		t.Fatal(issuerError)
	}
	internalConfig := security.OIDCConfig{Issuer: workloadIssuer.IssuerURL(), Audience: "ccattler", ClaimMapping: security.ClaimMapping{PrincipalClaim: "sub", TeamClaim: "team"}}
	userTokenAuthenticator := security.NewLocalUserRecordAuthenticator(security.NewBearerTokenAuthenticator(security.NewOIDCAuthenticator(internalConfig, workloadIssuer.PublicKey())), factStore)
	authenticatorChain := security.NewAuthenticatorChain(userTokenAuthenticator, &lifecycleAdminAuthenticator{})

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:lifecycle-admin", "cluster-admin")
	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuthenticatorChain(authenticatorChain)
	apiServer.SetRequirePrincipal(true)
	apiServer.SetUserTokenIssuer(security.NewUserTokenIssuer(workloadIssuer))
	return &userLifecycleHarness{t: t, factStore: factStore, apiServer: apiServer, apiAuthorizer: apiAuthorizer, authController: controllers.NewAuthController(apiAuthorizer), authenticator: authenticatorChain}
}

// adminRequest sends an admin request to the API and fails on a non-200.
func (harness *userLifecycleHarness) adminRequest(method string, path string, body string) []byte {
	harness.t.Helper()
	recorder := httptest.NewRecorder()
	harness.apiServer.Handler().ServeHTTP(recorder, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	if recorder.Code != http.StatusOK {
		harness.t.Fatalf("%s %s: status %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

// mintToken mints a user token through POST /api/user/token.
func (harness *userLifecycleHarness) mintToken(userName string) string {
	harness.t.Helper()
	var tokenResponse struct {
		Token string `json:"token"` // Token is the minted JWT.
	}
	_ = json.Unmarshal(harness.adminRequest(http.MethodPost, "/api/user/token", `{"username":"`+userName+`","ttl":"1h"}`), &tokenResponse)
	if tokenResponse.Token == "" {
		harness.t.Fatalf("no token minted for %s", userName)
	}
	return tokenResponse.Token
}

// applyAuthConfig writes the configuration-owned auth facts (as cca apply
// would) and runs the AuthController over every auth/ fact.
func (harness *userLifecycleHarness) applyAuthConfig(aliceInDevelopers bool) {
	harness.t.Helper()
	ctx := context.Background()
	_, _ = harness.factStore.Put(ctx, types.KeyAuthGrant("group", "developers", "api-writer"), []byte("true"))
	if aliceInDevelopers {
		_, _ = harness.factStore.Put(ctx, types.KeyAuthGroupMember("developers", "alice"), []byte("true"))
	} else {
		_ = harness.factStore.Delete(ctx, types.KeyAuthGroupMember("developers", "alice"))
	}
	harness.reconcileAuth()
}

// reconcileAuth runs the AuthController once over the auth/ facts.
func (harness *userLifecycleHarness) reconcileAuth() {
	harness.t.Helper()
	authFacts, scanError := harness.factStore.Scan(context.Background(), types.PrefixAuth+"/")
	if scanError != nil {
		harness.t.Fatal(scanError)
	}
	if _, reconcileError := harness.authController.Reconcile(context.Background(), authFacts); reconcileError != nil {
		harness.t.Fatal(reconcileError)
	}
}

// tokenGrantsWorkloadCreate authenticates token and reports whether it is
// accepted and whether its principal holds workload.create (developer rights).
func (harness *userLifecycleHarness) tokenGrantsWorkloadCreate(token string) (bool, bool) {
	harness.t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	result, authError := harness.authenticator.Authenticate(request)
	if authError != nil || result == nil || result.Principal != "user:alice" {
		return false, false
	}
	principal := security.Principal{Kind: security.PrincipalKindUser, Name: "alice"}
	return true, harness.apiAuthorizer.AuthorizeAPI(principal, security.CapabilityWorkloadCreate, security.ScopeCluster, nil) == nil
}

// TestUserLifecycleWithConfigOwnedGroupMembership walks the M216 lifecycle:
// deleting a user revokes its identity (old tokens refused) but not its
// configuration-owned group membership; re-creating the name reattaches that
// membership for new tokens only; removing the membership from the
// configuration removes the group's rights.
func TestUserLifecycleWithConfigOwnedGroupMembership(t *testing.T) {
	harness := newUserLifecycleHarness(t)
	harness.applyAuthConfig(true)

	harness.adminRequest(http.MethodPost, "/api/user?name=alice", `{"team":"payments"}`)
	firstToken := harness.mintToken("alice")
	if accepted, developer := harness.tokenGrantsWorkloadCreate(firstToken); !accepted || !developer {
		t.Fatalf("alice in developers: accepted=%v developer=%v, want both", accepted, developer)
	}

	harness.adminRequest(http.MethodDelete, "/api/user?name=alice", "")
	harness.reconcileAuth()
	if accepted, _ := harness.tokenGrantsWorkloadCreate(firstToken); accepted {
		t.Fatal("deleted user's token still accepted")
	}
	if _, getError := harness.factStore.Get(context.Background(), types.KeyAuthGroupMember("developers", "alice")); getError != nil {
		t.Fatalf("configured group membership was removed by user delete: %v", getError)
	}

	harness.adminRequest(http.MethodPost, "/api/user?name=alice", `{"team":"payments"}`)
	secondToken := harness.mintToken("alice")
	if accepted, developer := harness.tokenGrantsWorkloadCreate(secondToken); !accepted || !developer {
		t.Fatalf("re-created alice: accepted=%v developer=%v, want both (membership still configured)", accepted, developer)
	}
	if accepted, _ := harness.tokenGrantsWorkloadCreate(firstToken); accepted {
		t.Fatal("token from before the deletion accepted after re-creation")
	}

	harness.applyAuthConfig(false)
	if accepted, developer := harness.tokenGrantsWorkloadCreate(secondToken); !accepted || developer {
		t.Fatalf("after removing alice from developers: accepted=%v developer=%v, want accepted without developer rights", accepted, developer)
	}
}
