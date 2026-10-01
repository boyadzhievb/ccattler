package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
	"github.com/boyadzhievb/ccattler/types"
)

// tenantVisibilityFactResponse mirrors the unexported api.factResponse for
// JSON unmarshaling in tests.
type tenantVisibilityFactResponse struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Revision int64  `json:"revision"`
}

// setupTwoTenantStore populates a fact store with services for two tenants
// (payments and frontend) plus observed instances, to exercise tenant
// visibility filtering. Uses flat service names with explicit owner facts
// since the status builder extracts service names at the first path segment.
func setupTwoTenantStore(t *testing.T, factStore store.StateStore) {
	t.Helper()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredService("checkout"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("checkout"), []byte("checkout:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("checkout"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))

	factStore.Put(ctx, types.KeyDesiredService("paydb"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("paydb"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("paydb"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner("paydb"), []byte("payments"))

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("5"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner("web"), []byte("frontend"))

	factStore.Put(ctx, types.KeyDesiredService("assets"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("assets"), []byte("cdn:latest"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("assets"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner("assets"), []byte("frontend"))

	factStore.Put(ctx, types.KeyObservedInstanceState("checkout-1"), []byte(string(types.InstanceRunning)))
	factStore.Put(ctx, types.KeyObservedInstanceService("checkout-1"), []byte("checkout"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("checkout-1"), []byte("node-1"))

	factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(string(types.InstanceRunning)))
	factStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("web"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("web-1"), []byte("node-1"))
}

// TestTenantVisibilityStatus verifies that the /api/status endpoint returns
// only services and instances belonging to the requesting principal's tenant.
func TestTenantVisibilityStatus(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	setupTwoTenantStore(t, factStore)

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"*": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)

	t.Run("payments tenant sees only payments services", func(t *testing.T) {
		paymentsPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "alice",
			Attributes: []security.Attribute{
				{Key: "team", Value: "payments"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), paymentsPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}

		var status api.ClusterStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("failed to unmarshal status: %v", err)
		}

		if len(status.Services) != 2 {
			t.Fatalf("expected 2 services for payments tenant, got %d: %+v", len(status.Services), status.Services)
		}
		for _, service := range status.Services {
			if service.Name != "checkout" && service.Name != "paydb" {
				t.Errorf("unexpected service visible to payments tenant: %s", service.Name)
			}
		}

		for _, instance := range status.Instances {
			if instance.ServiceName != "checkout" && instance.ServiceName != "paydb" {
				t.Errorf("unexpected instance visible to payments tenant: service=%s", instance.ServiceName)
			}
		}
	})

	t.Run("frontend tenant sees only frontend services", func(t *testing.T) {
		frontendPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "bob",
			Attributes: []security.Attribute{
				{Key: "team", Value: "frontend"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), frontendPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:bob")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}

		var status api.ClusterStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("failed to unmarshal status: %v", err)
		}

		if len(status.Services) != 2 {
			t.Fatalf("expected 2 services for frontend tenant, got %d: %+v", len(status.Services), status.Services)
		}
		for _, service := range status.Services {
			if service.Name != "web" && service.Name != "assets" {
				t.Errorf("unexpected service visible to frontend tenant: %s", service.Name)
			}
		}
	})

	t.Run("platform principal sees all services", func(t *testing.T) {
		platformPrincipal := security.Principal{
			Kind:   security.PrincipalKindUser,
			Name:   "admin",
			Groups: []string{"platform"},
			Attributes: []security.Attribute{
				{Key: "team", Value: "platform"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), platformPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:admin")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}

		var status api.ClusterStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("failed to unmarshal status: %v", err)
		}

		if len(status.Services) != 4 {
			t.Fatalf("expected 4 services for platform principal, got %d: %+v", len(status.Services), status.Services)
		}
	})

	t.Run("system principal sees all services", func(t *testing.T) {
		systemPrincipal := security.Principal{
			Kind: security.PrincipalKindSystem,
			Name: "scheduler",
		}

		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), systemPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "system:scheduler")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		var status api.ClusterStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("failed to unmarshal status: %v", err)
		}

		if len(status.Services) != 4 {
			t.Fatalf("expected 4 services for system principal, got %d", len(status.Services))
		}
	})

	t.Run("principal without team attribute sees nothing", func(t *testing.T) {
		noTeamPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "orphan",
		}

		request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), noTeamPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:orphan")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		var status api.ClusterStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatalf("failed to unmarshal status: %v", err)
		}

		if len(status.Services) != 0 {
			t.Fatalf("expected 0 services for principal without team, got %d", len(status.Services))
		}
	})
}

// TestTenantVisibilityScopedScan verifies that the /api/state?prefix= endpoint
// returns only facts visible to the requesting principal's tenant. Uses
// hierarchical service names since ScopedScan derives tenancy from path prefixes.
func TestTenantVisibilityScopedScan(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("payments/checkout"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("frontend/web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("frontend/web"), []byte("5"))

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"*": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)

	t.Run("payments tenant scoped scan of desired/service/", func(t *testing.T) {
		paymentsPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "alice",
			Attributes: []security.Attribute{
				{Key: "team", Value: "payments"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/state?prefix=desired/service/", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), paymentsPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}

		var facts []tenantVisibilityFactResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &facts); err != nil {
			t.Fatalf("failed to unmarshal facts: %v", err)
		}

		for _, fact := range facts {
			if fact.Key == "" {
				continue
			}
			if !containsSubstring(fact.Key, "payments/") {
				t.Errorf("payments tenant should not see fact: %s", fact.Key)
			}
		}
	})

	t.Run("platform principal scoped scan sees all", func(t *testing.T) {
		platformPrincipal := security.Principal{
			Kind:   security.PrincipalKindUser,
			Name:   "admin",
			Groups: []string{"platform"},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/state?prefix=desired/service/", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), platformPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:admin")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		var facts []tenantVisibilityFactResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &facts); err != nil {
			t.Fatalf("failed to unmarshal facts: %v", err)
		}

		hasPayments := false
		hasFrontend := false
		for _, fact := range facts {
			if containsSubstring(fact.Key, "payments/") {
				hasPayments = true
			}
			if containsSubstring(fact.Key, "frontend/") {
				hasFrontend = true
			}
		}
		if !hasPayments || !hasFrontend {
			t.Errorf("platform principal should see both tenants: payments=%v, frontend=%v", hasPayments, hasFrontend)
		}
	})
}

// TestTenantAuditEndpoint verifies the /api/audit endpoint returns
// tenant-scoped audit entries.
func TestTenantAuditEndpoint(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	auditLog := security.NewInMemoryAuditLog(100)
	auditLog.Log(security.AuditEntry{
		Principal: "user:alice",
		Action:    "workload.update",
		Target:    "payments/checkout",
		Decision:  "ALLOW",
	})
	auditLog.Log(security.AuditEntry{
		Principal: "user:bob",
		Action:    "workload.update",
		Target:    "frontend/web",
		Decision:  "ALLOW",
	})
	auditLog.Log(security.AuditEntry{
		Principal: "user:alice",
		Action:    "workload.delete",
		Target:    "payments/database",
		Decision:  "DENY",
	})

	tenantRegistry := tenant.NewTenantRegistry(factStore)
	auditView := tenant.NewTenantAuditView(auditLog, tenantRegistry)

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"*": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetTenantAuditView(auditView)

	t.Run("payments tenant sees only payments audit entries", func(t *testing.T) {
		paymentsPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "alice",
			Attributes: []security.Attribute{
				{Key: "team", Value: "payments"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), paymentsPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recorder.Code)
		}

		var entries []security.AuditEntry
		if err := json.Unmarshal(recorder.Body.Bytes(), &entries); err != nil {
			t.Fatalf("failed to unmarshal audit entries: %v", err)
		}

		if len(entries) != 2 {
			t.Fatalf("expected 2 audit entries for payments tenant, got %d", len(entries))
		}
		for _, entry := range entries {
			if !containsSubstring(entry.Target, "payments") {
				t.Errorf("payments tenant should not see entry with target: %s", entry.Target)
			}
		}
	})

	t.Run("platform principal sees all entries", func(t *testing.T) {
		platformPrincipal := security.Principal{
			Kind:   security.PrincipalKindUser,
			Name:   "admin",
			Groups: []string{"platform"},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), platformPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:admin")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		var entries []security.AuditEntry
		if err := json.Unmarshal(recorder.Body.Bytes(), &entries); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if len(entries) != 3 {
			t.Fatalf("expected 3 audit entries for platform principal, got %d", len(entries))
		}
	})

	t.Run("denied filter returns only DENY entries", func(t *testing.T) {
		paymentsPrincipal := security.Principal{
			Kind: security.PrincipalKindUser,
			Name: "alice",
			Attributes: []security.Attribute{
				{Key: "team", Value: "payments"},
			},
		}

		request := httptest.NewRequest(http.MethodGet, "/api/audit?denied=true", nil)
		requestCtx := security.WithPrincipalStruct(request.Context(), paymentsPrincipal)
		requestCtx = security.WithPrincipal(requestCtx, "user:alice")
		request = request.WithContext(requestCtx)
		recorder := httptest.NewRecorder()

		apiServer.Handler().ServeHTTP(recorder, request)

		var entries []security.AuditEntry
		if err := json.Unmarshal(recorder.Body.Bytes(), &entries); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if len(entries) != 1 {
			t.Fatalf("expected 1 denied entry, got %d", len(entries))
		}
		if entries[0].Decision != "DENY" {
			t.Errorf("expected DENY decision, got %s", entries[0].Decision)
		}
	})
}

// containsSubstring checks whether s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsIndex(s, substr))
}

// containsIndex checks if substr appears anywhere in s.
func containsIndex(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
