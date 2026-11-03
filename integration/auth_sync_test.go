// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestAuthSyncAppliesAndRevokesStoreGrants runs the AuthController the way
// cca server does (its own runner per API process) and verifies that grant
// facts written to the store reach the APIAuthorizer and that deleting them
// revokes access, while bootstrap grants stay.
func TestAuthSyncAppliesAndRevokesStoreGrants(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:local-admin", "cluster-admin")
	authSyncRunner := controllers.NewRunner(factStore, controllers.NewAuthController(apiAuthorizer))
	go func() { _ = authSyncRunner.Run(ctx) }()

	bob := security.Principal{Kind: security.PrincipalKindUser, Name: "bob"}
	localAdmin := security.Principal{Kind: security.PrincipalKindUser, Name: "local-admin"}
	grantKey := types.KeyAuthGrant("user", "bob", "api-writer")
	if _, putError := factStore.Put(ctx, grantKey, []byte("true")); putError != nil {
		t.Fatal(putError)
	}

	waitFor(t, 5*time.Second, "bob gains workload.create from the api-writer grant", func() bool {
		return apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadCreate, security.ScopeCluster, nil) == nil
	})

	if deleteError := factStore.Delete(ctx, grantKey); deleteError != nil {
		t.Fatal(deleteError)
	}
	waitFor(t, 5*time.Second, "bob loses workload.create after the grant is deleted, local-admin keeps access", func() bool {
		return apiAuthorizer.AuthorizeAPI(bob, security.CapabilityWorkloadCreate, security.ScopeCluster, nil) != nil &&
			apiAuthorizer.AuthorizeAPI(localAdmin, security.CapabilityWorkloadCreate, security.ScopeCluster, nil) == nil
	})
}
