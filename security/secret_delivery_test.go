// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// deliveryTestAuditCapacity is the in-memory audit capacity of the fixture.
const deliveryTestAuditCapacity = 100

// deliveryTestFixture holds a delivery service over a memory store with one
// enrolled node, one placed instance of service "checkout" and one secret.
type deliveryTestFixture struct {
	rawStore        *store.MemoryStore     // rawStore is the unrestricted store used to set up facts.
	secretStore     *SecretStore           // secretStore writes encrypted secrets.
	auditLog        *InMemoryAuditLog      // auditLog captures delivery decisions.
	deliveryService *SecretDeliveryService // deliveryService is the system under test.
}

// newDeliveryTestFixture builds the standard fixture: node-1 enrolled,
// instance inst-1 of checkout placed on node-1, secret db.password granted.
func newDeliveryTestFixture(testContext *testing.T) *deliveryTestFixture {
	testContext.Helper()
	ctx := context.Background()
	rawStore := store.NewMemoryStore()
	masterKey, keyError := GenerateMasterKey()
	if keyError != nil {
		testContext.Fatalf("GenerateMasterKey: %v", keyError)
	}
	secretStore, storeError := NewSecretStoreWithMasterKey(rawStore, masterKey)
	if storeError != nil {
		testContext.Fatalf("NewSecretStoreWithMasterKey: %v", storeError)
	}
	rbacAuthorizer := NewRBACAuthorizer()
	for _, builtinRole := range BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(RoleBinding{Principal: SecretDeliveryPrincipal, RoleName: "secret-delivery"})
	auditLog := NewInMemoryAuditLog(deliveryTestAuditCapacity)
	deliveryStore := NewAuthorizedStore(rawStore, rbacAuthorizer, nil)
	deliveryStore.SetServicePrincipal(SecretDeliveryPrincipal)

	putTestFact(testContext, rawStore, EnrolledNodePrefix+"node-1", "{}")
	putTestFact(testContext, rawStore, types.KeyObservedNodeState("node-1"), string(types.NodeAlive))
	putTestFact(testContext, rawStore, types.KeyPlacementInstance("inst-1"), "node-1")
	putTestFact(testContext, rawStore, types.KeyObservedInstanceService("inst-1"), "checkout")
	putTestFact(testContext, rawStore, types.ScanDesiredServiceSecrets("checkout")+"db.password", "/run/secrets/db")
	if putError := secretStore.PutSecret(ctx, "db.password", []byte("s3cret")); putError != nil {
		testContext.Fatalf("PutSecret: %v", putError)
	}
	return &deliveryTestFixture{
		rawStore:        rawStore,
		secretStore:     secretStore,
		auditLog:        auditLog,
		deliveryService: NewSecretDeliveryService(deliveryStore, secretStore, auditLog),
	}
}

// putTestFact writes a fact to the raw store or fails the test.
func putTestFact(testContext *testing.T, rawStore store.StateStore, key string, value string) {
	testContext.Helper()
	if _, putError := rawStore.Put(context.Background(), key, []byte(value)); putError != nil {
		testContext.Fatalf("put %s: %v", key, putError)
	}
}

// expectRefusal asserts that deliveryError is a refusal with the given reason
// and returns it.
func expectRefusal(testContext *testing.T, deliveryError error, expectedReason SecretDeliveryRefusalReason) *SecretDeliveryRefusal {
	testContext.Helper()
	var refusal *SecretDeliveryRefusal
	if !errors.As(deliveryError, &refusal) {
		testContext.Fatalf("expected SecretDeliveryRefusal, got %v", deliveryError)
	}
	if refusal.Reason != expectedReason {
		testContext.Fatalf("reason = %s, want %s (%v)", refusal.Reason, expectedReason, refusal)
	}
	return refusal
}

func TestSecretDeliveryReturnsGrantedSecretsForPlacedInstance(testContext *testing.T) {
	fixture := newDeliveryTestFixture(testContext)
	delivered, deliveryError := fixture.deliveryService.DeliverInstanceSecrets(context.Background(), "node-1", "inst-1")
	if deliveryError != nil {
		testContext.Fatalf("DeliverInstanceSecrets: %v", deliveryError)
	}
	if delivered.Service != "checkout" || string(delivered.Secrets["db.password"]) != "s3cret" || len(delivered.Secrets) != 1 {
		testContext.Fatalf("unexpected delivery: %+v", delivered)
	}
	auditEntries := fixture.auditLog.Entries()
	if len(auditEntries) != 1 || auditEntries[0].Decision != AuditDecisionAllow || strings.Contains(auditEntries[0].Target, "s3cret") {
		testContext.Fatalf("expected one allow audit entry without the value, got %+v", auditEntries)
	}
}

func TestSecretDeliveryRefusesWrongNodeUnplacedDisabledAndUnenrolled(testContext *testing.T) {
	testCases := []struct {
		name       string                     // name describes the case.
		nodeID     string                     // nodeID is the calling node.
		instanceID string                     // instanceID is the requested instance.
		setUp      func(*deliveryTestFixture) // setUp mutates the fixture before the request.
	}{
		{name: "wrong node", nodeID: "node-2", instanceID: "inst-1", setUp: func(fixture *deliveryTestFixture) {
			putTestFact(testContext, fixture.rawStore, EnrolledNodePrefix+"node-2", "{}")
		}},
		{name: "unplaced instance", nodeID: "node-1", instanceID: "inst-unplaced", setUp: func(*deliveryTestFixture) {}},
		{name: "disabled node", nodeID: "node-1", instanceID: "inst-1", setUp: func(fixture *deliveryTestFixture) {
			putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeState("node-1"), string(types.NodeDisabled))
		}},
		{name: "unenrolled node", nodeID: "node-1", instanceID: "inst-1", setUp: func(fixture *deliveryTestFixture) {
			_ = fixture.rawStore.Delete(context.Background(), EnrolledNodePrefix+"node-1")
		}},
		{name: "path in instance id", nodeID: "node-1", instanceID: "inst-1/../x", setUp: func(*deliveryTestFixture) {}},
	}
	for _, testCase := range testCases {
		testContext.Run(testCase.name, func(subTest *testing.T) {
			fixture := newDeliveryTestFixture(subTest)
			testCase.setUp(fixture)
			_, deliveryError := fixture.deliveryService.DeliverInstanceSecrets(context.Background(), testCase.nodeID, testCase.instanceID)
			expectRefusal(subTest, deliveryError, SecretDeliveryNotAuthorized)
			auditEntries := fixture.auditLog.Entries()
			if len(auditEntries) != 1 || auditEntries[0].Decision != AuditDecisionDeny {
				subTest.Fatalf("expected one deny audit entry, got %+v", auditEntries)
			}
		})
	}
}

func TestSecretDeliveryRefusesUngrantedConnectionAndPullSecrets(testContext *testing.T) {
	fixture := newDeliveryTestFixture(testContext)
	ctx := context.Background()
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredServiceConnection("checkout", "db"), "/run/connections/db")
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredConnection("db"), `{"driver":"postgresql","user":"app","secret":"db.admin","host":"db","port":5432}`)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredServiceImagePullSecret("checkout"), "registry.creds")
	_, deliveryError := fixture.deliveryService.DeliverInstanceSecrets(ctx, "node-1", "inst-1")
	refusal := expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)
	if strings.Join(refusal.SecretNames, ",") != "db.admin,registry.creds" {
		testContext.Fatalf("secret names = %v", refusal.SecretNames)
	}
}

func TestSecretDeliveryReportsMissingGrantedSecret(testContext *testing.T) {
	fixture := newDeliveryTestFixture(testContext)
	putTestFact(testContext, fixture.rawStore, types.ScanDesiredServiceSecrets("checkout")+"api.key", "/run/secrets/api")
	_, deliveryError := fixture.deliveryService.DeliverInstanceSecrets(context.Background(), "node-1", "inst-1")
	refusal := expectRefusal(testContext, deliveryError, SecretDeliverySecretMissing)
	if len(refusal.SecretNames) != 1 || refusal.SecretNames[0] != "api.key" {
		testContext.Fatalf("secret names = %v", refusal.SecretNames)
	}
}

func TestSecretDeliveryRateLimitsRefusalAudit(testContext *testing.T) {
	fixture := newDeliveryTestFixture(testContext)
	for attempt := 0; attempt < 5; attempt++ {
		_, _ = fixture.deliveryService.DeliverInstanceSecrets(context.Background(), "node-1", "inst-unplaced")
	}
	if entryCount := len(fixture.auditLog.Entries()); entryCount != 1 {
		testContext.Fatalf("expected 1 rate-limited refusal entry, got %d", entryCount)
	}
}

func TestSecretDeliveryRoleIsReadOnlyAndAPIServerRoleHasNoSecrets(testContext *testing.T) {
	rbacAuthorizer := NewRBACAuthorizer()
	for _, builtinRole := range BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(RoleBinding{Principal: SecretDeliveryPrincipal, RoleName: "secret-delivery"})
	rbacAuthorizer.BindRole(RoleBinding{Principal: APIServerPrincipal, RoleName: "api-server"})
	if readError := rbacAuthorizer.Authorize(SecretDeliveryPrincipal, PermissionRead, "secrets/db.password"); readError != nil {
		testContext.Fatalf("secret-delivery must read secrets/: %v", readError)
	}
	for _, writeKey := range []string{"secrets/db.password", "desired/service/checkout/secret/x", "audit/entry", "placement/instance/i"} {
		if writeError := rbacAuthorizer.Authorize(SecretDeliveryPrincipal, PermissionWrite, writeKey); writeError == nil {
			testContext.Fatalf("secret-delivery must not write %s", writeKey)
		}
	}
	for _, deniedKey := range []string{"secrets/db.password", "credentials/i/x", "keyring/active"} {
		if readError := rbacAuthorizer.Authorize(APIServerPrincipal, PermissionRead, deniedKey); readError == nil {
			testContext.Fatalf("api-server must not read %s", deniedKey)
		}
	}
}
