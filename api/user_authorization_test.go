// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	// A user outside the platform group with no tenant passes authorization
	// but tenant visibility (ScopedScan) shows nothing: 200 with an empty list.
	tenantlessResponse := serveAsUser(apiServer, "carol", http.MethodGet, "/api/state?prefix=desired/service/", "")
	if tenantlessResponse.Code != http.StatusOK || strings.Contains(tenantlessResponse.Body.String(), "nginx") {
		t.Fatalf("tenantless api-reader: got HTTP %d %s, want 200 with no facts", tenantlessResponse.Code, tenantlessResponse.Body.String())
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
