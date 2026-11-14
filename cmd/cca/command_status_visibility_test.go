// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// bearerNameTestAuthenticator authenticates "Bearer <name>" as user:<name>,
// so one test server can serve several identities.
type bearerNameTestAuthenticator struct{}

// Authenticate maps the bearer token to a user principal of the same name.
func (authenticator *bearerNameTestAuthenticator) Authenticate(request *http.Request) (*security.AuthenticationResult, error) {
	userName := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	return &security.AuthenticationResult{Principal: "user:" + userName, PrincipalKind: security.PrincipalKindUser}, nil
}

// putTeamServiceFixture writes one declared service with an image and count.
func putTeamServiceFixture(t *testing.T, factStore store.StateStore, serviceName string) {
	t.Helper()
	ctx := context.Background()
	for factKey, factValue := range map[string]string{
		types.KeyDesiredService(serviceName):          "",
		types.KeyDesiredServiceImage(serviceName):     "nginx:1.28",
		types.KeyDesiredServiceInstances(serviceName): "1",
	} {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
}

// TestStatusCommandShowsOnlyVisibleTeams runs the cca binary's `status`
// against the API server as a reader granted workload.read at team/frontend
// and as a user without grants (spec api-route-authorization, "No legacy
// status endpoint").
func TestStatusCommandShowsOnlyVisibleTeams(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	putTeamServiceFixture(t, factStore, "frontend/web")
	putTeamServiceFixture(t, factStore, "payments/checkout")

	apiServer := api.NewServer(factStore)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&bearerNameTestAuthenticator{}))
	apiServer.SetRequirePrincipal(true)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:frontend-reader", security.CapabilityWorkloadRead, security.Scope("team/frontend"))
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiTestServer := httptest.NewServer(apiServer.Handler())
	defer apiTestServer.Close()

	// HOME is a fresh directory so no user context or credential file leaks in.
	cliEnvironment := func(token string) []string {
		return []string{"CCA_API=" + strings.TrimPrefix(apiTestServer.URL, "http://"), "CCA_TOKEN=" + token, "HOME=" + t.TempDir()}
	}

	teamStdout, teamStderr, teamExit := runCCAWithEnv(t, cliEnvironment("frontend-reader"), "status")
	if teamExit != 0 || !strings.Contains(teamStdout, "frontend/web") {
		t.Errorf("team reader: exit %d, want 0 and frontend/web listed\nstdout: %s\nstderr: %s", teamExit, teamStdout, teamStderr)
	}
	if strings.Contains(teamStdout, "payments/checkout") {
		t.Errorf("team reader sees another team's service payments/checkout:\n%s", teamStdout)
	}

	ungrantedStdout, ungrantedStderr, ungrantedExit := runCCAWithEnv(t, cliEnvironment("nogrants"), "status")
	if ungrantedExit == 0 || strings.Contains(ungrantedStdout, "CLUSTER STATUS") {
		t.Errorf("user without grants: exit %d, want non-zero and no cluster data\nstdout: %s", ungrantedExit, ungrantedStdout)
	}
	if !strings.Contains(ungrantedStderr, "workload.read") {
		t.Errorf("user without grants: stderr %q does not name the missing workload.read grant", ungrantedStderr)
	}
}
