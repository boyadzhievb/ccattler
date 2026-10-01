package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// TestAuthDSLRoundTrip verifies the full authorization pipeline:
// DSL source → parse → compile → apply to store → auth controller reconcile → authorization enforced.
func TestAuthDSLRoundTrip(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `
role developer {
    allow workload.read
    allow workload.update
    scope team/payments
}

role operator {
    allow workload.read
    allow workload.create
    allow workload.update
    allow workload.delete
    allow node.read
    allow node.manage
}

group developers {
    member "alice@example.com"
    member "bob@example.com"
}

grant developer to group developers
grant operator to user "carol@example.com"
`

	err := lang.Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	apiAuthorizer := security.NewAPIAuthorizer()
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	alice := security.Principal{Kind: security.PrincipalKindUser, Name: "alice@example.com"}
	bob := security.Principal{Kind: security.PrincipalKindUser, Name: "bob@example.com"}
	carol := security.Principal{Kind: security.PrincipalKindUser, Name: "carol@example.com"}
	dave := security.Principal{Kind: security.PrincipalKindUser, Name: "dave@example.com"}

	waitFor(t, 2*time.Second, "alice gets workload.read at team/payments", func() bool {
		return apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.TeamScope("payments"), nil) == nil
	})

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadUpdate, security.TeamScope("payments"), nil); err != nil {
		t.Errorf("alice should have workload.update at team/payments: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
		t.Error("alice should NOT have workload.read at cluster scope (scoped to team/payments)")
	}
	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadDelete, security.TeamScope("payments"), nil); err == nil {
		t.Error("alice should NOT have workload.delete (developer role lacks it)")
	}

	if err := apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadRead, security.TeamScope("payments"), nil); err != nil {
		t.Errorf("bob (via group) should have workload.read at team/payments: %v", err)
	}

	if err := apiAuthorizer.AuthorizeAPI(carol, security.CapabilityWorkloadDelete, security.ScopeCluster, nil); err != nil {
		t.Errorf("carol (operator) should have workload.delete at cluster: %v", err)
	}
	if err := apiAuthorizer.AuthorizeAPI(carol, security.CapabilityNodeManage, security.ScopeCluster, nil); err != nil {
		t.Errorf("carol (operator) should have node.manage at cluster: %v", err)
	}

	if err := apiAuthorizer.AuthorizeAPI(dave, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err == nil {
		t.Error("dave should NOT have any capabilities (no grant)")
	}
}

// TestAuthDSLAdditionalGrantTakesEffect verifies that applying additional auth
// DSL facts and re-reconciling expands the authorization state.
func TestAuthDSLAdditionalGrantTakesEffect(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	initialInput := `
role viewer {
    allow workload.read
}

grant viewer to user "alice@example.com"
`
	err := lang.Apply(context.Background(), factStore, initialInput)
	if err != nil {
		t.Fatalf("initial Apply failed: %v", err)
	}

	apiAuthorizer := security.NewAPIAuthorizer()
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	alice := security.Principal{Kind: security.PrincipalKindUser, Name: "alice@example.com"}

	waitFor(t, 2*time.Second, "alice gets workload.read", func() bool {
		return apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil) == nil
	})

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityNodeRead, security.ScopeCluster, nil); err == nil {
		t.Error("alice should NOT have node.read before additional grant")
	}

	additionalInput := `
role node-viewer {
    allow node.read
}

grant node-viewer to user "alice@example.com"
`
	err = lang.Apply(context.Background(), factStore, additionalInput)
	if err != nil {
		t.Fatalf("additional Apply failed: %v", err)
	}

	waitFor(t, 2*time.Second, "alice gets node.read after additional grant", func() bool {
		return apiAuthorizer.AuthorizeAPI(alice, security.CapabilityNodeRead, security.ScopeCluster, nil) == nil
	})

	if err := apiAuthorizer.AuthorizeAPI(alice, security.CapabilityWorkloadRead, security.ScopeCluster, nil); err != nil {
		t.Errorf("alice should still have workload.read (additive apply): %v", err)
	}
}
