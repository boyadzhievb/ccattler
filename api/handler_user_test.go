// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/types"
)

// TestUserDeleteRemovesGrantsAndInlineRole verifies that deleting a user also
// deletes every role grant it holds and its inline capability role, so a user
// re-created later with the same name inherits nothing (M216, found in M214
// run 1: grants survived `cca user delete`). Another user's grant stays.
func TestUserDeleteRemovesGrantsAndInlineRole(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)
	ctx := context.Background()
	seededFacts := map[string]string{
		types.KeyAuthUser("alice"):                                       `{"team":"payments"}`,
		types.KeyAuthGrant("user", "alice", "api-writer"):                "true",
		types.KeyAuthGrant("user", "alice", "inline-user-alice"):         "true",
		types.KeyAuthRoleCapability("inline-user-alice", "secret.read"):  "true",
		types.KeyAuthRoleScope("inline-user-alice", "team/payments"):     "true",
		types.KeyAuthUser("alice-2"):                                     `{}`,
		types.KeyAuthGrant("user", "alice-2", "api-reader"):              "true",
		types.KeyAuthRoleCapability("inline-user-alice-2", "secret.use"): "true",
	}
	for factKey, factValue := range seededFacts {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			testingContext.Fatal(putError)
		}
	}

	request := requestWithAdminPrincipal(httptest.NewRequest(http.MethodDelete, "/api/user?name=alice", nil))
	recorder := httptest.NewRecorder()
	testServer.handleUser(recorder, request)
	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("delete: status %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, getError := factStore.Get(ctx, types.KeyAuthUser("alice")); getError == nil {
		testingContext.Error("user record still present after delete")
	}
	for _, removedPrefix := range []string{types.ScanAuthGrantsForPrincipal("user", "alice"), types.ScanAuthRoles + "inline-user-alice/"} {
		remaining, scanError := factStore.Scan(ctx, removedPrefix)
		if scanError != nil {
			testingContext.Fatal(scanError)
		}
		for _, fact := range remaining {
			testingContext.Errorf("still present after delete: %s", fact.Key)
		}
	}
	for _, keptKey := range []string{types.KeyAuthUser("alice-2"), types.KeyAuthGrant("user", "alice-2", "api-reader"), types.KeyAuthRoleCapability("inline-user-alice-2", "secret.use")} {
		if _, getError := factStore.Get(ctx, keptKey); getError != nil {
			testingContext.Errorf("another user's fact removed: %s", keptKey)
		}
	}
}

// TestGCRequiresClusterAdmin verifies that a principal with only the built-in
// api-reader role is refused garbage collection, which deletes facts (M216;
// open to any authenticated user in M214 run 1).
func TestGCRequiresClusterAdmin(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)
	testServer.apiAuthorizer.GrantRole("user:reader", "api-reader")
	readerContext := security.WithPrincipalStruct(context.Background(), security.Principal{Kind: security.PrincipalKindUser, Name: "reader"})

	gcRecorder := httptest.NewRecorder()
	testServer.handleGC(gcRecorder, httptest.NewRequest(http.MethodPost, "/api/gc", nil).WithContext(readerContext))
	if gcRecorder.Code != http.StatusForbidden {
		testingContext.Errorf("reader POST /api/gc: status %d, want 403", gcRecorder.Code)
	}
}

// auditTestRequest runs GET /api/audit as a principal with the given team
// (empty = none) and returns the status and the entries' principals.
func auditTestRequest(testingContext *testing.T, testServer *Server, principalName string, team string) (int, []string) {
	testingContext.Helper()
	principal := security.Principal{Kind: security.PrincipalKindUser, Name: principalName}
	if team != "" {
		principal.Attributes = []security.Attribute{{Key: "team", Value: team}}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	testServer.handleAudit(recorder, request.WithContext(security.WithPrincipalStruct(request.Context(), principal)))
	var entries []security.AuditEntry
	_ = json.Unmarshal(recorder.Body.Bytes(), &entries)
	principals := make([]string, 0, len(entries))
	for _, entry := range entries {
		principals = append(principals, entry.Principal)
	}
	return recorder.Code, principals
}

// TestAuditTenantFilterFailsClosedAndMatchesWholeSegments verifies that a
// reader without a team is refused the audit log instead of seeing every
// entry, and that team "pay" does not match principal "user:paynter" (M216
// security review: both leaked other tenants' audit entries).
func TestAuditTenantFilterFailsClosedAndMatchesWholeSegments(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)
	testServer.auditStore = factStore
	for _, readerName := range []string{"teamless", "pay-reader", "payments-reader"} {
		testServer.apiAuthorizer.GrantRole("user:"+readerName, "api-reader")
	}
	for entryIndex, entry := range []security.AuditEntry{
		{Principal: "user:paynter", Action: "get", Target: "desired/service/web", Decision: "allow"},
		{Principal: "service:payments/api", Action: "get", Target: "payments/checkout", Decision: "allow"},
	} {
		entryJSON, _ := json.Marshal(entry)
		if _, putError := factStore.Put(context.Background(), fmt.Sprintf("%s/%020d", types.PrefixAudit, entryIndex+1), entryJSON); putError != nil {
			testingContext.Fatal(putError)
		}
	}

	if status, _ := auditTestRequest(testingContext, testServer, "teamless", ""); status != http.StatusForbidden {
		testingContext.Errorf("reader without a team: status %d, want 403", status)
	}
	if _, principals := auditTestRequest(testingContext, testServer, "pay-reader", "pay"); len(principals) != 0 {
		testingContext.Errorf("team pay sees %v, want nothing (paynter is not team pay)", principals)
	}
	if _, principals := auditTestRequest(testingContext, testServer, "payments-reader", "payments"); len(principals) != 1 || principals[0] != "service:payments/api" {
		testingContext.Errorf("team payments sees %v, want only service:payments/api", principals)
	}
}
