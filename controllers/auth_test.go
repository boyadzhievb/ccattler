// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	if len(watchPrefixes) != 4 {
		t.Fatalf("expected 4 watch prefixes, got %d", len(watchPrefixes))
	}
	expectedPrefixes := []string{types.ScanAuthRoles, types.ScanAuthGrants, types.ScanAuthGroups, types.ScanAuthPolicies}
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

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.TeamScope("payments"), nil); err != nil {
		t.Errorf("alice should have workload.read at team/payments: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadUpdate, security.TeamScope("payments"), nil); err != nil {
		t.Errorf("alice should have workload.update at team/payments: %v", err)
	}

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
		t.Error("alice should NOT have workload.read at cluster scope")
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadDelete, security.TeamScope("payments"), nil); err == nil {
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
		if err := apiAuthorizer.AuthorizeAPI(admin, capability, security.ScopeCluster, nil); err != nil {
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

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err != nil {
		t.Errorf("alice (via group) should have workload.read: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err != nil {
		t.Errorf("bob (via group) should have workload.read: %v", err)
	}

	carol := security.Principal{Kind: security.PrincipalKindUser, Name: "carol@example.com"}
	if err := apiAuthorizer.AuthorizeAPI(carol, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
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
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err != nil {
		t.Fatalf("alice should have workload.read after first reconcile: %v", err)
	}

	updatedFacts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthRoleCapability("viewer", "node.read"), Value: []byte("true")},
		{Key: types.KeyAuthGrant("user", "alice@example.com", "viewer"), Value: []byte("true")},
	})
	_, _ = authController.Reconcile(context.Background(), updatedFacts)

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
		t.Error("alice should NOT have workload.read after second reconcile (role removed)")
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityNodeRead, security.ScopeCluster, nil); err != nil {
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
	if err := apiAuthorizer.AuthorizeAPI(leftover, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
		t.Error("previous grants should be cleared when auth facts are empty")
	}
}

func TestAuthControllerBuiltinRoleWithoutCapabilityFacts(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	facts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthGrant("user", "bob", "api-reader"), Value: []byte("true")},
	})
	if _, reconcileErr := authController.Reconcile(context.Background(), facts); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	bob := security.Principal{Kind: security.PrincipalKindUser, Name: "bob"}
	if authError := apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadRead, security.ScopeCluster, nil); authError != nil {
		t.Fatalf("api-reader grant without role facts must use the builtin role: %v", authError)
	}
	if apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadCreate, security.ScopeCluster, nil) == nil {
		t.Fatal("api-reader must not grant workload.create")
	}
}

func TestAuthControllerIgnoresPolicyWithoutConditions(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	authController := NewAuthController(apiAuthorizer)

	facts := sortedFacts([]store.Fact{
		{Key: types.KeyAuthPolicyCapability("open-exec"), Value: []byte("workload.exec")},
	})
	if _, reconcileErr := authController.Reconcile(context.Background(), facts); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	anyone := security.Principal{Kind: security.PrincipalKindUser, Name: "anyone"}
	if apiAuthorizer.AuthorizeAPI(anyone, security.CapabilityWorkloadExec, security.ScopeCluster, nil) == nil {
		t.Fatal("a policy without conditions must not grant its capability to every principal")
	}
}

func TestAuthControllerKeepsBootstrapGrants(t *testing.T) {
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:local-admin", "cluster-admin")
	authController := NewAuthController(apiAuthorizer)

	if _, reconcileErr := authController.Reconcile(context.Background(), nil); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	localAdmin := security.Principal{Kind: security.PrincipalKindUser, Name: "local-admin"}
	if authError := apiAuthorizer.AuthorizeAPI(localAdmin, security.CapabilitySecretWrite, security.ScopeCluster, nil); authError != nil {
		t.Fatalf("bootstrap grant must survive a reconcile with no auth facts: %v", authError)
	}
}
