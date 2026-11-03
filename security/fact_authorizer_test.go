// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

func TestRequiredCapabilityForFactWrite(t *testing.T) {
	expectations := map[string]Capability{
		"desired/service/web/image":                    CapabilityWorkloadCreate,
		"desired/route/web/host":                       CapabilityWorkloadCreate,
		"effective/service/web/instances":              CapabilityWorkloadCreate,
		"intent/user/service/web/instances":            CapabilityWorkloadCreate,
		"derived/service/web/qos_class":                CapabilityWorkloadCreate,
		"desired/service/web/secret/db-password":       CapabilitySecretUse,
		"desired/service/web/image_pull_secret":        CapabilitySecretUse,
		"desired/service/web/cloud_identity/s3-writer": CapabilityClusterAdmin,
		"desired/cloud_identity/s3-writer/role":        CapabilityClusterAdmin,
		"auth/grant/user/bob/role/cluster-admin":       CapabilityClusterAdmin,
		"auth/role/developer/capability/workload.read": CapabilityClusterAdmin,
		"policy/network/deny-all":                      CapabilityClusterAdmin,
		"secrets/db-password":                          CapabilityClusterAdmin,
		"placement/instance/abc":                       CapabilityClusterAdmin,
	}
	for factKey, wantCapability := range expectations {
		if gotCapability := RequiredCapabilityForFactWrite(factKey); gotCapability != wantCapability {
			t.Errorf("RequiredCapabilityForFactWrite(%q) = %q, want %q", factKey, gotCapability, wantCapability)
		}
	}
}

func TestCapabilityFactAuthorizerBlocksGrantEscalation(t *testing.T) {
	apiAuthorizer := NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:writer", "api-writer")
	apiAuthorizer.GrantRole("user:admin", "cluster-admin")
	factAuthorizer := NewCapabilityFactAuthorizer(apiAuthorizer)

	if authError := factAuthorizer.Authorize("user:writer", PermissionWrite, "desired/service/web/image"); authError != nil {
		t.Fatalf("api-writer must be able to write service facts: %v", authError)
	}
	for _, privilegedKey := range []string{"auth/grant/user/writer/role/cluster-admin", "desired/service/web/secret/db-password", "policy/network/allow-all"} {
		if factAuthorizer.Authorize("user:writer", PermissionWrite, privilegedKey) == nil {
			t.Errorf("api-writer must not write %q through apply", privilegedKey)
		}
	}
	if authError := factAuthorizer.Authorize("user:admin", PermissionWrite, "auth/grant/user/bob/role/api-reader"); authError != nil {
		t.Fatalf("cluster-admin must be able to write grants: %v", authError)
	}
	if factAuthorizer.Authorize("user:admin", PermissionRead, "desired/service/web/image") == nil {
		t.Fatal("only writes are checked; other permissions must be denied")
	}
}

func TestAuthorizedStoreServicePrincipalAuthorizesAsAPIServer(t *testing.T) {
	rawStore := store.NewMemoryStore()
	defer rawStore.Close()
	ctx := context.Background()
	_, _ = rawStore.Put(ctx, "desired/service/web/image", []byte("nginx"))
	_, _ = rawStore.Put(ctx, "secrets/db-password", []byte("ciphertext"))

	rbacAuthorizer := NewRBACAuthorizer()
	for _, builtinRole := range BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(RoleBinding{Principal: APIServerPrincipal, RoleName: "api-server"})
	auditLog := NewInMemoryAuditLog(100)
	authorizedStore := NewAuthorizedStore(rawStore, rbacAuthorizer, auditLog)
	authorizedStore.SetServicePrincipal(APIServerPrincipal)

	userContext := WithPrincipal(ctx, "user:bob")
	facts, scanError := authorizedStore.Scan(userContext, "desired")
	if scanError != nil || len(facts) != 1 {
		t.Fatalf("a user without store bindings must read through the api-server role: %v, %d facts", scanError, len(facts))
	}
	if _, getError := authorizedStore.Get(userContext, "secrets/db-password"); getError == nil {
		t.Fatal("the api-server role must not read secrets/ through the generic store")
	}
	if _, putError := authorizedStore.Put(userContext, "enrollment/node/x", []byte("{}")); putError == nil {
		t.Fatal("the api-server role must not write enrollment/")
	}

	auditEntries := auditLog.Entries()
	lastEntry := auditEntries[len(auditEntries)-1]
	if lastEntry.Principal != APIServerPrincipal || lastEntry.OnBehalfOf != "user:bob" {
		t.Fatalf("audit must record the service principal acting for the user, got %+v", lastEntry)
	}
}

func TestAuditLogsSanitizeEveryWriter(t *testing.T) {
	forgedPrincipal := "user:mallory\n2026-11-03 allow user:admin"
	for _, auditLog := range []AuditLogger{NewInMemoryAuditLog(10), NewStoreBackedAuditLog(store.NewMemoryStore(), 10)} {
		auditLog.Log(AuditEntry{Principal: forgedPrincipal, Action: "apply", Target: "desired/service/web\r\nx", Decision: "deny", Policy: "team:condition_failed\n"})
		loggedEntry := auditLog.Entries()[0]
		for fieldName, fieldValue := range map[string]string{"principal": loggedEntry.Principal, "target": loggedEntry.Target, "policy": loggedEntry.Policy} {
			if strings.ContainsAny(fieldValue, "\n\r") {
				t.Errorf("%T %s keeps a raw line break: %q", auditLog, fieldName, fieldValue)
			}
		}
	}
}
