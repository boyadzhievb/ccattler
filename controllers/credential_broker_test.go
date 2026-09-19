package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func newTestBroker(t *testing.T) *CredentialBrokerController {
	t.Helper()
	tokenIssuer, err := security.NewWorkloadTokenIssuer("https://ccattler.test")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	brokerController := NewCredentialBrokerController(tokenIssuer)
	simulatorAdapter := security.NewSimulatorCloudAdapter("aws", &security.CloudCredential{
		Provider:     "aws",
		AccessKeyID:  "AKIATEST",
		SecretKey:    "secret-test",
		SessionToken: "session-test",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	brokerController.RegisterAdapter(simulatorAdapter)
	return brokerController
}

func TestCredentialBrokerControllerName(t *testing.T) {
	brokerController := newTestBroker(t)
	if brokerController.Name() != "credential-broker" {
		t.Fatalf("expected name 'credential-broker', got %q", brokerController.Name())
	}
}

func TestCredentialBrokerIssuesCredentialForRunningInstance(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)

	facts := []store.Fact{
		{Key: "desired/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "desired/cloud_identity/payments_s3/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/payments_s3/role", Value: []byte("arn:aws:iam::123456789012:role/test")},
		{Key: "desired/service/payments/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("payments")},
		{Key: "observed/instance/inst-1/state", Value: []byte("running")},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	changeMap := changesToMap(changes)
	stateKey := types.KeyDerivedCredentialState("inst-1", "payments_s3")
	if changeMap[stateKey] != "active" {
		t.Errorf("expected credential state 'active', got %q", changeMap[stateKey])
	}

	issuedKey := types.KeyDerivedCredentialIssuedAt("inst-1", "payments_s3")
	if _, exists := changeMap[issuedKey]; !exists {
		t.Error("expected issued_at fact to be set")
	}

	expiresKey := types.KeyDerivedCredentialExpiresAt("inst-1", "payments_s3")
	if _, exists := changeMap[expiresKey]; !exists {
		t.Error("expected expires_at fact to be set")
	}
}

func TestCredentialBrokerSkipsStoppedInstances(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)

	facts := []store.Fact{
		{Key: "desired/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "desired/cloud_identity/payments_s3/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/payments_s3/role", Value: []byte("arn:aws:iam::123456789012:role/test")},
		{Key: "desired/service/payments/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("payments")},
		{Key: "observed/instance/inst-1/state", Value: []byte("stopped")},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected no changes for stopped instance, got %d", len(changes))
	}
}

func TestCredentialBrokerSkipsActiveNonExpiredCredential(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)

	futureExpiry := time.Now().Add(2 * time.Hour).Format(time.RFC3339)

	facts := []store.Fact{
		{Key: "desired/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "desired/cloud_identity/payments_s3/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/payments_s3/role", Value: []byte("arn:aws:iam::123456789012:role/test")},
		{Key: "desired/service/payments/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("payments")},
		{Key: "observed/instance/inst-1/state", Value: []byte("running")},
		{Key: types.KeyDerivedCredentialState("inst-1", "payments_s3"), Value: []byte("active")},
		{Key: types.KeyDerivedCredentialExpiresAt("inst-1", "payments_s3"), Value: []byte(futureExpiry)},
		{Key: types.KeyDerivedCredentialIssuedAt("inst-1", "payments_s3"), Value: []byte(time.Now().Format(time.RFC3339))},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected no changes for active non-expired credential, got %d", len(changes))
	}
}

func TestCredentialBrokerRefreshesNearExpiry(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)
	brokerController.SetRefreshBefore(30 * time.Minute)

	nearExpiry := time.Now().Add(10 * time.Minute).Format(time.RFC3339)

	facts := []store.Fact{
		{Key: "desired/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "desired/cloud_identity/payments_s3/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/payments_s3/role", Value: []byte("arn:aws:iam::123456789012:role/test")},
		{Key: "desired/service/payments/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("payments")},
		{Key: "observed/instance/inst-1/state", Value: []byte("running")},
		{Key: types.KeyDerivedCredentialState("inst-1", "payments_s3"), Value: []byte("active")},
		{Key: types.KeyDerivedCredentialExpiresAt("inst-1", "payments_s3"), Value: []byte(nearExpiry)},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	changeMap := changesToMap(changes)
	if changeMap[types.KeyDerivedCredentialState("inst-1", "payments_s3")] != "active" {
		t.Error("expected credential to be refreshed")
	}
}

func TestCredentialBrokerGarbageCollectsStoppedInstance(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)

	facts := []store.Fact{
		{Key: "desired/cloud_identity/payments_s3", Value: []byte("")},
		{Key: "desired/cloud_identity/payments_s3/provider", Value: []byte("aws")},
		{Key: types.KeyDerivedCredentialState("inst-gone", "payments_s3"), Value: []byte("active")},
		{Key: types.KeyDerivedCredentialExpiresAt("inst-gone", "payments_s3"), Value: []byte(time.Now().Add(1 * time.Hour).Format(time.RFC3339))},
		{Key: types.KeyDerivedCredentialIssuedAt("inst-gone", "payments_s3"), Value: []byte(time.Now().Format(time.RFC3339))},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	deleteCount := 0
	for _, change := range changes {
		if change.Type == store.OpDelete {
			deleteCount++
		}
	}
	if deleteCount != 3 {
		t.Errorf("expected 3 deletes for garbage collection, got %d", deleteCount)
	}
}

func TestCredentialBrokerErrorOnMissingAdapter(t *testing.T) {
	testContext := context.Background()
	tokenIssuer, _ := security.NewWorkloadTokenIssuer("https://ccattler.test")
	brokerController := NewCredentialBrokerController(tokenIssuer)
	// No adapters registered

	facts := []store.Fact{
		{Key: "desired/cloud_identity/test_id", Value: []byte("")},
		{Key: "desired/cloud_identity/test_id/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/test_id/role", Value: []byte("arn:aws:iam::123:role/test")},
		{Key: "desired/service/web/cloud_identity/test_id", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("web")},
		{Key: "observed/instance/inst-1/state", Value: []byte("running")},
	}

	changes, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	changeMap := changesToMap(changes)
	if changeMap[types.KeyDerivedCredentialState("inst-1", "test_id")] != "error" {
		t.Error("expected credential state 'error' when no adapter registered")
	}
	errorKey := types.KeyDerivedCredentialError("inst-1", "test_id")
	if _, exists := changeMap[errorKey]; !exists {
		t.Error("expected error message fact")
	}
}

func TestCredentialBrokerReadsConfigFromFacts(t *testing.T) {
	testContext := context.Background()
	brokerController := newTestBroker(t)

	facts := []store.Fact{
		{Key: "desired/credential_broker/credential_ttl", Value: []byte("2h")},
		{Key: "desired/credential_broker/refresh_before", Value: []byte("30m")},
		{Key: "desired/cloud_identity/test_id", Value: []byte("")},
		{Key: "desired/cloud_identity/test_id/provider", Value: []byte("aws")},
		{Key: "desired/cloud_identity/test_id/role", Value: []byte("arn:aws:iam::123:role/test")},
		{Key: "desired/service/web/cloud_identity/test_id", Value: []byte("")},
		{Key: "observed/instance/inst-1/service", Value: []byte("web")},
		{Key: "observed/instance/inst-1/state", Value: []byte("running")},
	}

	_, err := brokerController.Reconcile(testContext, facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if brokerController.credentialTTL != 2*time.Hour {
		t.Errorf("expected credentialTTL 2h, got %v", brokerController.credentialTTL)
	}
	if brokerController.refreshBefore != 30*time.Minute {
		t.Errorf("expected refreshBefore 30m, got %v", brokerController.refreshBefore)
	}
}

func changesToMap(changes []Change) map[string]string {
	result := make(map[string]string)
	for _, change := range changes {
		if change.Type == store.OpPut {
			result[change.Key] = string(change.Value)
		}
	}
	return result
}
