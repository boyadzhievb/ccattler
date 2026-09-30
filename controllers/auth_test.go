package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// sortedFacts sorts a fact slice in place and returns it, for test convenience.
func sortedFacts(facts []store.Fact) []store.Fact {
	store.SortFacts(facts)
	return facts
}

func TestAuthControllerName(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)
	if authController.Name() != "auth" {
		t.Errorf("expected name 'auth', got %q", authController.Name())
	}
}

func TestAuthControllerWatch(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)
	watchPrefixes := authController.Watch()
	if len(watchPrefixes) != 3 {
		t.Fatalf("expected 3 watch prefixes, got %d", len(watchPrefixes))
	}
	expectedPrefixes := []string{types.ScanAuthRoles, types.ScanAuthGrants, types.ScanAuthGroups}
	for prefixIndex, expectedPrefix := range expectedPrefixes {
		if watchPrefixes[prefixIndex] != expectedPrefix {
			t.Errorf("watch[%d]: got %q, want %q", prefixIndex, watchPrefixes[prefixIndex], expectedPrefix)
		}
	}
}

func TestAuthControllerReconcileRoleGrant(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	facts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("developer", "workload.read"), Value: []byte("true")},
		{Key: types.KeyAuthRoleCapability("developer", "workload.update"), Value: []byte("true")},
		{Key: types.KeyAuthRoleScope("developer", "team/payments"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("user", "alice@example.com", "developer"), Value: []byte("true")},
	})

	changes, reconcileErr := authController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	if len(changes) != 0 {
		t.Errorf("expected 0 store changes, got %d", len(changes))
	}

	alice := security.Principal{Kind: security.PrincipalKindUser, Name: "alice@example.com"}

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.TeamScope("payments")); err != nil {
		t.Errorf("alice should have workload.read at team/payments: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadUpdate, security.TeamScope("payments")); err != nil {
		t.Errorf("alice should have workload.update at team/payments: %v", err)
	}

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster); err == nil {
		t.Error("alice should NOT have workload.read at cluster scope")
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadDelete, security.TeamScope("payments")); err == nil {
		t.Error("alice should NOT have workload.delete")
	}
}

func TestAuthControllerReconcileClusterWideRole(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	facts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("admin", "cluster.admin"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("user", "superadmin", "admin"), Value: []byte("true")},
	})

	_, reconcileErr := authController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	admin := security.Principal{Kind: security.PrincipalKindUser, Name: "superadmin"}

	for _, capability := range security.AllCapabilities {
		if err := apiAuthorizer.AuthorizeAPI(admin, capability, security.ScopeCluster); err != nil {
			t.Errorf("admin should have %q at cluster scope: %v", capability, err)
		}
	}
}

func TestAuthControllerReconcileGroupGrant(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	facts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("developer", "workload.read"), Value: []byte("true")},
		{Key: types.KeyAuthGroupMember("developers", "alice@example.com"), Value: []byte("true")},
		{Key: types.KeyAuthGroupMember("developers", "bob@example.com"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("group", "developers", "developer"), Value: []byte("true")},
	})

	_, reconcileErr := authController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	alice := security.Principal{Kind: security.PrincipalKindUser, Name: "alice@example.com"}
	bob := security.Principal{Kind: security.PrincipalKindUser, Name: "bob@example.com"}

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster); err != nil {
		t.Errorf("alice (via group) should have workload.read: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadRead, security.ScopeCluster); err != nil {
		t.Errorf("bob (via group) should have workload.read: %v", err)
	}

	carol := security.Principal{Kind: security.PrincipalKindUser, Name: "carol@example.com"}
	if err := apiAuthorizer.AuthorizeAPI(carol, security.CapabilityWorkloadRead, security.ScopeCluster); err == nil {
		t.Error("carol should NOT have workload.read (not in group)")
	}
}

func TestAuthControllerReconcileReplacesOldGrants(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	initialFacts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("developer", "workload.read"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("user", "alice@example.com", "developer"), Value: []byte("true")},
	})
	_, _ = authController.Reconcile(context.Background(), initialFacts)

	alice := security.Principal{Kind: security.PrincipalKindUser, Name: "alice@example.com"}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster); err != nil {
		t.Fatalf("alice should have workload.read after first reconcile: %v", err)
	}

	updatedFacts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("viewer", "node.read"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("user", "alice@example.com", "viewer"), Value: []byte("true")},
	})
	_, _ = authController.Reconcile(context.Background(), updatedFacts)

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster); err == nil {
		t.Error("alice should NOT have workload.read after second reconcile (role removed)")
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityNodeRead, security.ScopeCluster); err != nil {
		t.Errorf("alice should have node.read after second reconcile: %v", err)
	}
}

func TestAuthControllerReconcileEmptyFacts(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:leftover", security.CapabilityWorkloadRead, security.ScopeCluster)
	authController := NewAuthController(apiAuthorizer)

	_, reconcileErr := authController.Reconcile(context.Background(), nil)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	leftover := security.Principal{Kind: security.PrincipalKindUser, Name: "leftover"}
	if err := apiAuthorizer.AuthorizeAPI(leftover, security.CapabilityWorkloadRead, security.ScopeCluster); err == nil {
		t.Error("previous grants should be cleared when auth facts are empty")
	}
}
