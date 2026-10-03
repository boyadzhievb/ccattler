package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// TestCrossTenantAuthorizationDenied verifies that a principal with capabilities
// scoped to one team cannot access cluster-scoped API endpoints. This validates
// that hierarchical scoping prevents cross-tenant privilege escalation.
func TestCrossTenantAuthorizationDenied(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	setupTwoTenantStore(t, factStore)

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"user:alice": {
			{Capability: security.CapabilityWorkloadRead, Scope: security.TeamScope("payments")},
			{Capability: security.CapabilityWorkloadCreate, Scope: security.TeamScope("payments")},
			{Capability: security.CapabilityScalingWrite, Scope: security.TeamScope("payments")},
		},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetRequirePrincipal(true)

	alicePrincipal := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "alice",
		Attributes: []security.Attribute{
			{Key: "team", Value: "payments"},
		},
	}

	t.Run("team-scoped principal denied cluster-scoped status", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), alicePrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("expected 403 for team-scoped principal at cluster scope, got %d", recorder.Code)
		}
	})

	t.Run("team-scoped principal denied cluster-scoped scale", func(t *testing.T) {
		scaleBody := `{"service":"web","instances":10}`
		request := httptest.NewRequest(http.MethodPost, "/api/scale", strings.NewReader(scaleBody))
		request.Header.Set("Content-Type", "application/json")
		requestCtx := security.WithPrincipalStruct(request.Context(), alicePrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("expected 403 for team-scoped principal scaling cluster resource, got %d", recorder.Code)
		}
	})

	t.Run("team-scoped principal denied cluster-scoped apply", func(t *testing.T) {
		dslContent := `service web { image nginx:1.28 instances 2 }`
		request := httptest.NewRequest(http.MethodPost, "/api/apply", strings.NewReader(dslContent))
		request.Header.Set("Content-Type", "text/plain")
		requestCtx := security.WithPrincipalStruct(request.Context(), alicePrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("expected 403 for team-scoped principal applying to cluster scope, got %d", recorder.Code)
		}
	})
}

// TestClusterAdminBypassesTeamScoping verifies that a principal with
// cluster.admin capability at cluster scope can access all endpoints regardless
// of which tenant owns the resources.
func TestClusterAdminBypassesTeamScoping(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	setupTwoTenantStore(t, factStore)

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"user:admin": {
			{Capability: security.CapabilityClusterAdmin, Scope: security.ScopeCluster},
		},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetRequirePrincipal(true)

	adminPrincipal := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "admin",
	}
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	requestCtx := security.WithPrincipalStruct(request.Context(), adminPrincipal)
	requestCtx = security.WithPrincipal(requestCtx, "user:admin")
	request = request.WithContext(requestCtx)
	recorder := httptest.NewRecorder()

	apiServer.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("expected 200 for cluster-admin, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestUnauthenticatedRequestDenied verifies that requests without a principal
// are rejected with 401 when requirePrincipal is enabled.
func TestUnauthenticatedRequestDenied(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	apiAuthorizer := security.NewAPIAuthorizer()
	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetRequirePrincipal(true)

	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	recorder := httptest.NewRecorder()

	apiServer.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", recorder.Code)
	}
}
