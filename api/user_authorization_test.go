// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
	"github.com/boyadzhievb/ccattler/types"
)

// newServicePrincipalServer builds the API server the way cca server does
// (Flow 25): store access as service:api-server through the api-server role,
// users authorized only by capability. withPolicyGate selects the apply path.
func newServicePrincipalServer(t *testing.T, withPolicyGate bool) (*Server, *security.APIAuthorizer, store.StateStore) {
	t.Helper()
	rawStore := store.NewMemoryStore()
	t.Cleanup(func() { rawStore.Close() })
	_, _ = rawStore.Put(context.Background(), types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))

	rbacAuthorizer := security.NewRBACAuthorizer()
	for _, builtinRole := range security.BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(security.RoleBinding{Principal: security.APIServerPrincipal, RoleName: "api-server"})
	authorizedStore := security.NewAuthorizedStore(rawStore, rbacAuthorizer, nil)
	authorizedStore.SetServicePrincipal(security.APIServerPrincipal)

	apiAuthorizer := security.NewAPIAuthorizer()
	apiServer := NewServer(authorizedStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	if withPolicyGate {
		tenantRegistry := tenant.NewTenantRegistry(rawStore)
		apiServer.SetPolicyGate(tenant.NewPolicyGate(rawStore, tenantRegistry, tenant.NewQuotaAdmission(rawStore, tenantRegistry),
			security.NewCapabilityFactAuthorizer(apiAuthorizer), nil))
	}
	return apiServer, apiAuthorizer, rawStore
}

// serveAsUser sends a request through the API handler as "user:<name>" with
// the given groups.
func serveAsUser(apiServer *Server, userName string, method string, requestPath string, body string, groups ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, requestPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "text/plain")
	userContext := security.WithPrincipalStruct(request.Context(), security.Principal{Kind: security.PrincipalKindUser, Name: userName, Groups: groups})
	userContext = security.WithPrincipal(userContext, "user:"+userName)
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, request.WithContext(userContext))
	return recorder
}

func TestGrantedUserReadsStateThroughAPIServerPrincipal(t *testing.T) {
	apiServer, apiAuthorizer, _ := newServicePrincipalServer(t, true)
	apiAuthorizer.GrantRole("user:bob", "api-reader")
	apiAuthorizer.GrantRole("user:carol", "api-reader")

	stateResponse := serveAsUser(apiServer, "bob", http.MethodGet, "/api/state?prefix=desired/service/", "", "platform")
	if stateResponse.Code != http.StatusOK || !strings.Contains(stateResponse.Body.String(), "nginx:1.28") {
		t.Fatalf("platform api-reader state read: got HTTP %d %s, want 200 with the service image", stateResponse.Code, stateResponse.Body.String())
	}
	if statusResponse := serveAsUser(apiServer, "bob", http.MethodGet, "/api/status", "", "platform"); statusResponse.Code != http.StatusOK {
		t.Fatalf("platform api-reader status: got HTTP %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	// A cluster-scope grant gives cluster-wide visibility without team
	// membership or the platform group (Flow 25).
	tenantlessResponse := serveAsUser(apiServer, "carol", http.MethodGet, "/api/state?prefix=desired/service/", "")
	if tenantlessResponse.Code != http.StatusOK || !strings.Contains(tenantlessResponse.Body.String(), "nginx:1.28") {
		t.Fatalf("cluster-scope api-reader without a team: got HTTP %d %s, want 200 with the service", tenantlessResponse.Code, tenantlessResponse.Body.String())
	}
}

func TestWorkloadWriterCannotApplyGrants(t *testing.T) {
	escalationDSL := `grant cluster-admin to user "mallory"`
	for _, withPolicyGate := range []bool{true, false} {
		apiServer, apiAuthorizer, rawStore := newServicePrincipalServer(t, withPolicyGate)
		apiAuthorizer.GrantRole("user:mallory", "api-writer")

		applyResponse := serveAsUser(apiServer, "mallory", http.MethodPost, "/api/apply", escalationDSL)
		if applyResponse.Code != http.StatusForbidden {
			t.Fatalf("policy gate %v: grant apply by api-writer got HTTP %d %s, want 403", withPolicyGate, applyResponse.Code, applyResponse.Body.String())
		}
		grantFacts, _ := rawStore.Scan(context.Background(), types.ScanAuthGrants)
		if len(grantFacts) != 0 {
			t.Fatalf("policy gate %v: denied apply must not write grants, found %d", withPolicyGate, len(grantFacts))
		}

		serviceDSL := `service api { image nginx:1.28  instances 1  expose 8080 }`
		if serviceResponse := serveAsUser(apiServer, "mallory", http.MethodPost, "/api/apply", serviceDSL); serviceResponse.Code != http.StatusOK {
			t.Fatalf("policy gate %v: service apply by api-writer got HTTP %d %s, want 200", withPolicyGate, serviceResponse.Code, serviceResponse.Body.String())
		}
	}
}

// newTwoTeamServer returns a service-principal API server holding one payments
// and one frontend service.
func newTwoTeamServer(t *testing.T) (*Server, *security.APIAuthorizer) {
	t.Helper()
	apiServer, apiAuthorizer, rawStore := newServicePrincipalServer(t, false)
	_, _ = rawStore.Put(context.Background(), types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	_, _ = rawStore.Put(context.Background(), types.KeyDesiredServiceImage("frontend/site"), []byte("site:v1"))
	for _, serviceName := range []string{"payments/checkout", "frontend/site"} {
		_, _ = rawStore.Put(context.Background(), types.KeyDesiredService(serviceName), []byte(""))
	}
	return apiServer, apiAuthorizer
}

func TestVisibilityFollowsGrantScopeNotMembership(t *testing.T) {
	apiServer, apiAuthorizer := newTwoTeamServer(t)
	apiAuthorizer.Grant("user:pat", security.CapabilityWorkloadRead, security.TeamScope("payments"))
	apiAuthorizer.Grant("user:deep", security.CapabilityWorkloadRead, security.Scope("team/payments/checkout"))

	teamScoped := serveAsUser(apiServer, "pat", http.MethodGet, "/api/state?prefix=desired/service/", "")
	if teamScoped.Code != http.StatusOK || !strings.Contains(teamScoped.Body.String(), "checkout:v1") || strings.Contains(teamScoped.Body.String(), "site:v1") {
		t.Fatalf("team/payments grant must see payments only: HTTP %d %s", teamScoped.Code, teamScoped.Body.String())
	}
	hiddenKey := serveAsUser(apiServer, "pat", http.MethodGet, "/api/state?key="+types.KeyDesiredServiceImage("frontend/site"), "")
	if hiddenKey.Code != http.StatusNotFound {
		t.Fatalf("another team's key must answer like a missing key: got HTTP %d", hiddenKey.Code)
	}

	// Membership alone grants nothing, even for the team it names.
	memberOnly := httptest.NewRequest(http.MethodGet, "/api/state?prefix=desired/service/", nil)
	memberContext := security.WithPrincipalStruct(memberOnly.Context(), security.Principal{
		Kind: security.PrincipalKindUser, Name: "member", Groups: []string{"platform"},
		Attributes: []security.Attribute{{Key: "team", Value: "payments"}},
	})
	memberContext = security.WithPrincipal(memberContext, "user:member")
	memberRecorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(memberRecorder, memberOnly.WithContext(memberContext))
	if memberRecorder.Code != http.StatusForbidden {
		t.Fatalf("team attribute and platform group without a grant must be 403, got %d", memberRecorder.Code)
	}

	if deepScope := serveAsUser(apiServer, "deep", http.MethodGet, "/api/state?prefix=desired/service/", ""); deepScope.Code != http.StatusForbidden {
		t.Fatalf("a scope deeper than team/<name> must not grant team visibility, got HTTP %d", deepScope.Code)
	}
}

func TestStatusUsesTheSameVisibility(t *testing.T) {
	apiServer, apiAuthorizer := newTwoTeamServer(t)
	apiAuthorizer.Grant("user:pat", security.CapabilityWorkloadRead, security.TeamScope("payments"))
	apiAuthorizer.Grant("user:ops", security.CapabilityWorkloadRead, security.ScopeCluster)

	teamStatus := serveAsUser(apiServer, "pat", http.MethodGet, "/api/status", "")
	if teamStatus.Code != http.StatusOK || !strings.Contains(teamStatus.Body.String(), "payments/checkout") || strings.Contains(teamStatus.Body.String(), "frontend/site") {
		t.Fatalf("team-scoped status must list payments only: HTTP %d %s", teamStatus.Code, teamStatus.Body.String())
	}
	clusterStatus := serveAsUser(apiServer, "ops", http.MethodGet, "/api/status", "")
	if clusterStatus.Code != http.StatusOK || !strings.Contains(clusterStatus.Body.String(), "frontend/site") {
		t.Fatalf("cluster-scoped status must list every team: HTTP %d %s", clusterStatus.Code, clusterStatus.Body.String())
	}
}

func TestWatchUsesTheSameVisibility(t *testing.T) {
	apiServer, apiAuthorizer := newTwoTeamServer(t)
	apiAuthorizer.Grant("user:pat", security.CapabilityWorkloadRead, security.TeamScope("payments"))
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		userContext := security.WithPrincipalStruct(request.Context(), security.Principal{Kind: security.PrincipalKindUser, Name: "pat"})
		apiServer.Handler().ServeHTTP(responseWriter, request.WithContext(security.WithPrincipal(userContext, "user:pat")))
	}))
	defer testServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	watchRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/watch?prefix=desired/service/", nil)
	watchResponse, watchError := http.DefaultClient.Do(watchRequest)
	if watchError != nil {
		t.Fatal(watchError)
	}
	defer watchResponse.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = apiServer.factStore.Put(context.Background(), types.KeyDesiredServiceImage("frontend/site"), []byte("site:v2"))
		_, _ = apiServer.factStore.Put(context.Background(), types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v2"))
	}()
	readBuffer := make([]byte, 4096)
	readCount, _ := watchResponse.Body.Read(readBuffer)
	firstEvent := string(readBuffer[:readCount])
	if strings.Contains(firstEvent, "site:v2") || !strings.Contains(firstEvent, "checkout:v2") {
		t.Fatalf("team-scoped watch must deliver payments events only, first event: %s", firstEvent)
	}
}
