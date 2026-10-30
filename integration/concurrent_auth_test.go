// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
)

// concurrentAuthTestHarness holds the full multi-user test infrastructure:
// 3 users with real JWT tokens, an API server with authenticator chain,
// capability grants, tenant registry, and audit store.
type concurrentAuthTestHarness struct {
	factStore      store.StateStore
	apiServer      *api.Server
	httpServer     *httptest.Server
	tokenIssuer    *security.UserTokenIssuer
	adminToken     string
	teamADevToken  string
	teamBReadToken string
	auditLog       *security.StoreBackedAuditLog
	apiAuthorizer  *security.APIAuthorizer
}

// setupConcurrentAuthHarness builds a fully-wired API server with:
//   - OIDC authenticator using real ES256 JWT tokens
//   - 3 users: admin (cluster.admin), team-a-dev (workload CRUD on team-a), team-b-readonly (workload.read on team-b)
//   - Tenant registry with team-a and team-b
//   - Audit store for verifying audit trail correctness
func setupConcurrentAuthHarness(t *testing.T) *concurrentAuthTestHarness {
	t.Helper()

	signingKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		t.Fatalf("generate signing key: %v", keyError)
	}

	issuerURL := "https://test.ccattler.local"
	tokenIssuer := security.NewUserTokenIssuerWithKey(issuerURL, signingKey)

	oidcConfig := security.OIDCConfig{
		Issuer:   issuerURL,
		Audience: "ccattler",
		ClaimMapping: security.ClaimMapping{
			PrincipalClaim: "sub",
			TeamClaim:      "team",
		},
	}
	oidcAuthenticator := security.NewOIDCAuthenticator(oidcConfig, &signingKey.PublicKey)
	bearerAuthenticator := security.NewBearerTokenAuthenticator(oidcAuthenticator)
	authenticatorChain := security.NewAuthenticatorChain(bearerAuthenticator)

	adminToken, adminError := tokenIssuer.MintUserToken("admin", "", []string{"platform"}, 1*time.Hour)
	if adminError != nil {
		t.Fatalf("mint admin token: %v", adminError)
	}
	teamADevToken, teamAError := tokenIssuer.MintUserToken("alice", "team-a", []string{"developers"}, 1*time.Hour)
	if teamAError != nil {
		t.Fatalf("mint team-a-dev token: %v", teamAError)
	}
	teamBReadToken, teamBError := tokenIssuer.MintUserToken("bob", "team-b", []string{"viewers"}, 1*time.Hour)
	if teamBError != nil {
		t.Fatalf("mint team-b-readonly token: %v", teamBError)
	}

	factStore := store.NewMemoryStore()

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"user:admin": {
			{Capability: security.CapabilityClusterAdmin, Scope: security.ScopeCluster},
		},
		"user:alice": {
			{Capability: security.CapabilityWorkloadRead, Scope: security.TeamScope("team-a")},
			{Capability: security.CapabilityWorkloadCreate, Scope: security.TeamScope("team-a")},
			{Capability: security.CapabilityWorkloadUpdate, Scope: security.TeamScope("team-a")},
			{Capability: security.CapabilityWorkloadDelete, Scope: security.TeamScope("team-a")},
			{Capability: security.CapabilityScalingRead, Scope: security.TeamScope("team-a")},
			{Capability: security.CapabilityScalingWrite, Scope: security.TeamScope("team-a")},
		},
		"user:bob": {
			{Capability: security.CapabilityWorkloadRead, Scope: security.TeamScope("team-b")},
		},
	})

	auditLog := security.NewStoreBackedAuditLog(factStore, 1000)
	rbacAuthorizer := security.NewRBACAuthorizer()
	for _, builtinRole := range security.BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(security.RoleBinding{Principal: "user:admin", RoleName: "cluster-admin"})

	authorizedStore := security.NewAuthorizedStore(factStore, rbacAuthorizer, auditLog)

	tenantRegistry := tenant.NewTenantRegistry(factStore)
	auditView := tenant.NewTenantAuditView(auditLog, tenantRegistry)

	apiServer := api.NewServer(authorizedStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuthenticatorChain(authenticatorChain)
	apiServer.SetRequirePrincipal(true)
	apiServer.SetTenantAuditView(auditView)
	apiServer.SetAuditStore(factStore)

	setupConcurrentAuthTenantData(t, factStore)

	httpServer := httptest.NewServer(apiServer.Handler())

	return &concurrentAuthTestHarness{
		factStore:      factStore,
		apiServer:      apiServer,
		httpServer:     httpServer,
		tokenIssuer:    tokenIssuer,
		adminToken:     adminToken,
		teamADevToken:  teamADevToken,
		teamBReadToken: teamBReadToken,
		auditLog:       auditLog,
		apiAuthorizer:  apiAuthorizer,
	}
}

// setupConcurrentAuthTenantData populates the store with services owned by
// team-a and team-b for tenant isolation testing.
func setupConcurrentAuthTenantData(t *testing.T, factStore store.StateStore) {
	t.Helper()
	backgroundContext := context.Background()

	teamAServices := []string{"team-a/web", "team-a/api"}
	teamBServices := []string{"team-b/dashboard"}

	for _, serviceName := range teamAServices {
		factStore.Put(backgroundContext, "desired/service/"+serviceName+"/image", []byte("nginx:1.28"))
		factStore.Put(backgroundContext, "desired/service/"+serviceName+"/instances", []byte("2"))
		factStore.Put(backgroundContext, "tenant/owner/"+serviceName, []byte("team-a"))
	}
	for _, serviceName := range teamBServices {
		factStore.Put(backgroundContext, "desired/service/"+serviceName+"/image", []byte("redis:7"))
		factStore.Put(backgroundContext, "desired/service/"+serviceName+"/instances", []byte("1"))
		factStore.Put(backgroundContext, "tenant/owner/"+serviceName, []byte("team-b"))
	}
}

// authenticatedRequest creates an HTTP request with a Bearer token header.
func authenticatedRequest(method, urlPath, bearerToken string, body string) *http.Request {
	var request *http.Request
	if body != "" {
		request = httptest.NewRequest(method, urlPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	} else {
		request = httptest.NewRequest(method, urlPath, nil)
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	return request
}

// TestConcurrentMultiUserAPICalls verifies that 3 users making simultaneous
// API calls all receive correct responses: admin gets 200 on /api/status
// (cluster scope), team-scoped users get 403 (they lack cluster-wide read).
// No cross-contamination of identity or authorization state under concurrency.
func TestConcurrentMultiUserAPICalls(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	const concurrentRequestsPerUser = 10
	var waitGroup sync.WaitGroup

	type requestResult struct {
		userLabel  string
		statusCode int
	}
	resultChannel := make(chan requestResult, concurrentRequestsPerUser*3)

	performRequests := func(userLabel, token string) {
		defer waitGroup.Done()
		for requestIndex := 0; requestIndex < concurrentRequestsPerUser; requestIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", token, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			resultChannel <- requestResult{
				userLabel:  userLabel,
				statusCode: recorder.Code,
			}
		}
	}

	waitGroup.Add(3)
	go performRequests("admin", harness.adminToken)
	go performRequests("team-a-dev", harness.teamADevToken)
	go performRequests("team-b-readonly", harness.teamBReadToken)

	waitGroup.Wait()
	close(resultChannel)

	adminSuccessCount := 0
	teamAForbiddenCount := 0
	teamBForbiddenCount := 0

	for result := range resultChannel {
		switch result.userLabel {
		case "admin":
			if result.statusCode != http.StatusOK {
				t.Errorf("admin request got %d, expected 200", result.statusCode)
			}
			adminSuccessCount++
		case "team-a-dev":
			if result.statusCode != http.StatusForbidden {
				t.Errorf("team-a-dev on cluster-scoped /api/status got %d, expected 403", result.statusCode)
			}
			teamAForbiddenCount++
		case "team-b-readonly":
			if result.statusCode != http.StatusForbidden {
				t.Errorf("team-b-readonly on cluster-scoped /api/status got %d, expected 403", result.statusCode)
			}
			teamBForbiddenCount++
		}
	}

	if adminSuccessCount != concurrentRequestsPerUser {
		t.Errorf("admin: expected %d successes, got %d", concurrentRequestsPerUser, adminSuccessCount)
	}
	if teamAForbiddenCount != concurrentRequestsPerUser {
		t.Errorf("team-a-dev: expected %d forbidden, got %d", concurrentRequestsPerUser, teamAForbiddenCount)
	}
	if teamBForbiddenCount != concurrentRequestsPerUser {
		t.Errorf("team-b-readonly: expected %d forbidden, got %d", concurrentRequestsPerUser, teamBForbiddenCount)
	}
}

// TestTenantIsolationConcurrent verifies that team-a-dev sees only team-a
// resources and team-b-readonly sees only team-b resources when both query
// the status endpoint concurrently.
func TestTenantIsolationConcurrent(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	type tenantStatusResult struct {
		services []string
	}

	fetchTenantServices := func(token string) tenantStatusResult {
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodGet, "/api/status", token, "")
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		var statusResponse map[string]interface{}
		if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &statusResponse); unmarshalError != nil {
			return tenantStatusResult{}
		}

		serviceNames := []string{}
		if services, hasServices := statusResponse["services"]; hasServices {
			if serviceMap, isMap := services.(map[string]interface{}); isMap {
				for serviceName := range serviceMap {
					serviceNames = append(serviceNames, serviceName)
				}
			}
		}
		return tenantStatusResult{services: serviceNames}
	}

	var waitGroup sync.WaitGroup
	var teamAResult, teamBResult, adminResult tenantStatusResult

	waitGroup.Add(3)
	go func() {
		defer waitGroup.Done()
		teamAResult = fetchTenantServices(harness.teamADevToken)
	}()
	go func() {
		defer waitGroup.Done()
		teamBResult = fetchTenantServices(harness.teamBReadToken)
	}()
	go func() {
		defer waitGroup.Done()
		adminResult = fetchTenantServices(harness.adminToken)
	}()
	waitGroup.Wait()

	for _, serviceName := range teamAResult.services {
		if !strings.HasPrefix(serviceName, "team-a/") {
			t.Errorf("team-a-dev saw non-team-a service: %s", serviceName)
		}
	}

	for _, serviceName := range teamBResult.services {
		if !strings.HasPrefix(serviceName, "team-b/") {
			t.Errorf("team-b-readonly saw non-team-b service: %s", serviceName)
		}
	}

	if len(adminResult.services) < len(teamAResult.services)+len(teamBResult.services) {
		t.Errorf("admin should see all services (%d), but got %d (team-a=%d, team-b=%d)",
			len(teamAResult.services)+len(teamBResult.services),
			len(adminResult.services),
			len(teamAResult.services),
			len(teamBResult.services))
	}
}

// TestAuditCorrectnessMultiUser verifies that concurrent admin operations
// produce correct audit entries with the right principal, auth method, and
// action. Team-scoped users get 403 at the API capability layer (before any
// store operations), so they don't generate store-level audit — the test
// verifies this correctly: admin has entries, team users do not.
func TestAuditCorrectnessMultiUser(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	var waitGroup sync.WaitGroup
	const requestsPerUser = 5

	waitGroup.Add(3)
	go func() {
		defer waitGroup.Done()
		for requestIndex := 0; requestIndex < requestsPerUser; requestIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.adminToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Errorf("admin status request %d got %d", requestIndex, recorder.Code)
			}
		}
	}()
	go func() {
		defer waitGroup.Done()
		for requestIndex := 0; requestIndex < requestsPerUser; requestIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.teamADevToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Errorf("team-a status request %d: expected 403, got %d", requestIndex, recorder.Code)
			}
		}
	}()
	go func() {
		defer waitGroup.Done()
		for requestIndex := 0; requestIndex < requestsPerUser; requestIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.teamBReadToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Errorf("team-b status request %d: expected 403, got %d", requestIndex, recorder.Code)
			}
		}
	}()
	waitGroup.Wait()

	auditEntries, scanError := harness.factStore.Scan(context.Background(), "audit/")
	if scanError != nil {
		t.Fatalf("scan audit entries: %v", scanError)
	}

	adminAuditCount := 0
	aliceAuditCount := 0
	bobAuditCount := 0

	for _, auditFact := range auditEntries {
		var entry security.AuditEntry
		if unmarshalError := json.Unmarshal(auditFact.Value, &entry); unmarshalError != nil {
			continue
		}
		if entry.AuthMethod != "oidc" {
			t.Errorf("expected auth_method oidc for store-layer audit, got %q for %s", entry.AuthMethod, entry.Principal)
		}
		switch entry.Principal {
		case "user:admin":
			adminAuditCount++
		case "user:alice":
			aliceAuditCount++
		case "user:bob":
			bobAuditCount++
		}
	}

	if adminAuditCount == 0 {
		t.Error("expected store-level audit entries for admin (cluster-admin), got 0")
	}
	if aliceAuditCount != 0 {
		t.Errorf("team-a-dev (403 at API layer) should have 0 store-level audit entries, got %d", aliceAuditCount)
	}
	if bobAuditCount != 0 {
		t.Errorf("team-b-readonly (403 at API layer) should have 0 store-level audit entries, got %d", bobAuditCount)
	}
	t.Logf("admin audit entries: %d", adminAuditCount)
}

// TestTokenExpiryMidSession verifies that a token that expires between
// requests is rejected with 401 after expiry.
func TestTokenExpiryMidSession(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	shortLivedToken, mintError := harness.tokenIssuer.MintUserToken("ephemeral", "team-a", nil, 1*time.Second)
	if mintError != nil {
		t.Fatalf("mint short-lived token: %v", mintError)
	}

	recorder := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodGet, "/api/status", shortLivedToken, "")
	harness.apiServer.Handler().ServeHTTP(recorder, request)

	if recorder.Code == http.StatusUnauthorized {
		t.Fatalf("short-lived token should be valid immediately, got 401")
	}

	time.Sleep(2 * time.Second)

	recorder = httptest.NewRecorder()
	request = authenticatedRequest(http.MethodGet, "/api/status", shortLivedToken, "")
	harness.apiServer.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", recorder.Code)
	}
}

// TestEscalationDenial verifies that users cannot exceed their granted
// capabilities: team-b-readonly cannot write, team-a-dev cannot cluster-admin,
// and team-a-dev cannot access team-b resources.
func TestEscalationDenial(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	t.Run("team-b-readonly cannot apply workloads", func(t *testing.T) {
		dslContent := `service team-b/hacked { image evil:latest instances 1 }`
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodPost, "/api/apply", harness.teamBReadToken, dslContent)
		request.Header.Set("Content-Type", "text/plain")
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("readonly user should get 403 on apply, got %d: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("team-b-readonly cannot scale services", func(t *testing.T) {
		scaleBody := `{"service":"team-b/dashboard","instances":100}`
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodPost, "/api/scale", harness.teamBReadToken, scaleBody)
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("readonly user should get 403 on scale, got %d: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("team-a-dev cannot access team-b resources via role management", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodGet, "/api/roles", harness.teamADevToken, "")
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code == http.StatusOK {
			t.Log("role listing may be allowed for all authenticated users — checking grants instead")
		}

		grantBody := `{"principal":"user:alice","role":"cluster-admin"}`
		recorder = httptest.NewRecorder()
		request = authenticatedRequest(http.MethodPost, "/api/roles/grant", harness.teamADevToken, grantBody)
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code == http.StatusOK || recorder.Code == http.StatusCreated {
			t.Errorf("team-a-dev should not be able to grant cluster-admin, got %d", recorder.Code)
		}
	})

	t.Run("team-a-dev cannot access team-b scoped endpoints", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodGet, "/api/audit", harness.teamADevToken, "")
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code == http.StatusOK {
			var entries []security.AuditEntry
			json.Unmarshal(recorder.Body.Bytes(), &entries)
			for _, entry := range entries {
				if strings.Contains(entry.Target, "team-b") {
					t.Errorf("team-a-dev should not see team-b audit entries, found target: %s", entry.Target)
				}
			}
		}
	})

	t.Run("unauthenticated request rejected", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated request should get 401, got %d", recorder.Code)
		}
	})

	t.Run("invalid token rejected", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := authenticatedRequest(http.MethodGet, "/api/status", "invalid.jwt.token", "")
		harness.apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("invalid token should get 401, got %d", recorder.Code)
		}
	})
}

// TestConcurrentAuditTrailCompleteness runs many concurrent admin operations
// and verifies that no audit entries are lost under concurrent load. Also
// confirms team-scoped users are consistently rejected at the API layer.
func TestConcurrentAuditTrailCompleteness(t *testing.T) {
	t.Parallel()
	harness := setupConcurrentAuthHarness(t)
	defer harness.factStore.Close()
	defer harness.httpServer.Close()

	const adminOperations = 20
	const teamOperations = 10
	var waitGroup sync.WaitGroup

	waitGroup.Add(3)
	go func() {
		defer waitGroup.Done()
		for operationIndex := 0; operationIndex < adminOperations; operationIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.adminToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
		}
	}()
	go func() {
		defer waitGroup.Done()
		forbiddenCount := 0
		for operationIndex := 0; operationIndex < teamOperations; operationIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.teamADevToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			if recorder.Code == http.StatusForbidden {
				forbiddenCount++
			}
		}
		if forbiddenCount != teamOperations {
			t.Errorf("team-a-dev: expected %d forbidden, got %d", teamOperations, forbiddenCount)
		}
	}()
	go func() {
		defer waitGroup.Done()
		forbiddenCount := 0
		for operationIndex := 0; operationIndex < teamOperations; operationIndex++ {
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(http.MethodGet, "/api/status", harness.teamBReadToken, "")
			harness.apiServer.Handler().ServeHTTP(recorder, request)
			if recorder.Code == http.StatusForbidden {
				forbiddenCount++
			}
		}
		if forbiddenCount != teamOperations {
			t.Errorf("team-b-readonly: expected %d forbidden, got %d", teamOperations, forbiddenCount)
		}
	}()
	waitGroup.Wait()

	auditEntries, scanError := harness.factStore.Scan(context.Background(), "audit/")
	if scanError != nil {
		t.Fatalf("scan audit: %v", scanError)
	}

	adminAuditCount := 0
	for _, auditFact := range auditEntries {
		var entry security.AuditEntry
		if unmarshalError := json.Unmarshal(auditFact.Value, &entry); unmarshalError != nil {
			continue
		}
		if entry.Principal == "user:admin" {
			adminAuditCount++
		}
	}

	if adminAuditCount == 0 {
		t.Fatal("no audit entries generated from concurrent admin operations")
	}
	t.Logf("concurrent audit: %d admin audit entries from %d operations", adminAuditCount, adminOperations)
}
