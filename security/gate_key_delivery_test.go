// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// gateKeyTestCertificateTTL is the validity of certificates issued in tests.
const gateKeyTestCertificateTTL = time.Hour

// newGateKeyDeliveryTestFixture extends the delivery fixture: node-1 runs the
// gate proxy, and returns a gate-key delivery service over the same stores.
func newGateKeyDeliveryTestFixture(testContext *testing.T) (*deliveryTestFixture, *GateKeyDeliveryService) {
	testContext.Helper()
	fixture := newDeliveryTestFixture(testContext)
	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeGateProxy("node-1"), string(types.GateProxyEnabled))
	return fixture, NewGateKeyDeliveryService(fixture.deliveryStore, fixture.secretStore, fixture.auditLog)
}

// issueTestGatePair issues a certificate and key for host from a fresh CA.
func issueTestGatePair(testContext *testing.T, host string) *IssuedCertificate {
	testContext.Helper()
	certificateAuthority, caError := NewCertificateAuthority(gateKeyTestCertificateTTL)
	if caError != nil {
		testContext.Fatalf("NewCertificateAuthority: %v", caError)
	}
	issued, issueError := certificateAuthority.IssueCertificate(IssueCertificateRequest{CommonName: host, DNSNames: []string{host}, TTL: gateKeyTestCertificateTTL})
	if issueError != nil {
		testContext.Fatalf("IssueCertificate: %v", issueError)
	}
	return issued
}

// putTestGate writes a gate's host and TLS mode facts.
func putTestGate(testContext *testing.T, fixture *deliveryTestFixture, gateName string, host string, tlsMode types.RouteTLSMode) {
	testContext.Helper()
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteHost(gateName), host)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteService(gateName), "web")
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSMode(gateName), string(tlsMode))
}

func TestGateKeyDeliveryReturnsMatchingPairsAndListsPendingGates(testContext *testing.T) {
	ctx := context.Background()
	fixture, gateKeyService := newGateKeyDeliveryTestFixture(testContext)

	autoPair := issueTestGatePair(testContext, "auto.example.com")
	putTestGate(testContext, fixture, "auto-gate", "auto.example.com", types.RouteTLSModeAuto)
	putTestFact(testContext, fixture.rawStore, types.KeyDerivedRouteTLSCert("auto-gate"), string(autoPair.CertificatePEM))
	if putError := fixture.secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey("auto-gate"), autoPair.PrivateKeyPEM); putError != nil {
		testContext.Fatal(putError)
	}

	manualPair := issueTestGatePair(testContext, "manual.example.com")
	putTestGate(testContext, fixture, "manual-gate", "manual.example.com", types.RouteTLSModeManual)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSCert("manual-gate"), string(manualPair.CertificatePEM))
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSKeySecret("manual-gate"), "platform/web-gate-key")
	if putError := fixture.secretStore.PutSecret(ctx, "platform/web-gate-key", manualPair.PrivateKeyPEM); putError != nil {
		testContext.Fatal(putError)
	}

	// Mismatched pair: certificate of one CA, key of another.
	otherPair := issueTestGatePair(testContext, "mismatch.example.com")
	putTestGate(testContext, fixture, "mismatch-gate", "mismatch.example.com", types.RouteTLSModeAuto)
	putTestFact(testContext, fixture.rawStore, types.KeyDerivedRouteTLSCert("mismatch-gate"), string(otherPair.CertificatePEM))
	if putError := fixture.secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey("mismatch-gate"), autoPair.PrivateKeyPEM); putError != nil {
		testContext.Fatal(putError)
	}

	putTestGate(testContext, fixture, "waiting-gate", "waiting.example.com", types.RouteTLSModeAuto)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteHost("plain-gate"), "plain.example.com")

	delivered, deliveryError := gateKeyService.DeliverGateKeys(ctx, "node-1")
	if deliveryError != nil {
		testContext.Fatalf("DeliverGateKeys: %v", deliveryError)
	}
	if len(delivered.Gates) != 2 || delivered.Gates[0].Gate != "auto-gate" || delivered.Gates[1].Gate != "manual-gate" {
		testContext.Fatalf("delivered gates = %+v", delivered.Gates)
	}
	for _, deliveredGate := range delivered.Gates {
		if _, pairError := tls.X509KeyPair([]byte(deliveredGate.CertificatePEM), []byte(deliveredGate.PrivateKeyPEM)); pairError != nil {
			testContext.Errorf("gate %s pair invalid: %v", deliveredGate.Gate, pairError)
		}
	}
	if delivered.Gates[1].Host != "manual.example.com" {
		testContext.Errorf("manual gate host = %q", delivered.Gates[1].Host)
	}
	if strings.Join(delivered.Pending, ",") != "mismatch-gate,waiting-gate" {
		testContext.Errorf("pending = %v, want mismatch-gate,waiting-gate", delivered.Pending)
	}
	for _, auditEntry := range fixture.auditLog.Entries() {
		if strings.Contains(auditEntry.Target, "PRIVATE KEY") || strings.Contains(auditEntry.Policy, "PRIVATE KEY") {
			testContext.Fatal("audit entry contains key material")
		}
		if auditEntry.Action == GateKeyDeliveryAuditAction && !strings.Contains(auditEntry.Target, "gates=auto-gate,manual-gate") {
			testContext.Errorf("audit target = %q", auditEntry.Target)
		}
	}
}

func TestGateKeyDeliveryRefusesNodesWithoutGateProxyDisabledOrUnenrolled(testContext *testing.T) {
	ctx := context.Background()
	fixture, gateKeyService := newGateKeyDeliveryTestFixture(testContext)

	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeGateProxy("node-1"), string(types.GateProxyDisabled))
	_, deliveryError := gateKeyService.DeliverGateKeys(ctx, "node-1")
	expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)

	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeGateProxy("node-1"), string(types.GateProxyEnabled))
	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeState("node-1"), string(types.NodeDisabled))
	_, deliveryError = gateKeyService.DeliverGateKeys(ctx, "node-1")
	expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)

	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeGateProxy("node-2"), string(types.GateProxyEnabled))
	_, deliveryError = gateKeyService.DeliverGateKeys(ctx, "node-2")
	expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)

	_, deliveryError = gateKeyService.DeliverGateKeys(ctx, "")
	expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)

	denyCount := 0
	for _, auditEntry := range fixture.auditLog.Entries() {
		if auditEntry.Action == GateKeyDeliveryAuditAction && auditEntry.Decision == AuditDecisionDeny {
			denyCount++
		}
	}
	if denyCount != 4 {
		testContext.Errorf("deny audit entries = %d, want 4", denyCount)
	}
}

func TestGateKeyDeliveryRefusalAuditIsRateLimited(testContext *testing.T) {
	fixture, gateKeyService := newGateKeyDeliveryTestFixture(testContext)
	putTestFact(testContext, fixture.rawStore, types.KeyObservedNodeGateProxy("node-1"), string(types.GateProxyDisabled))
	for attempt := 0; attempt < 5; attempt++ {
		_, _ = gateKeyService.DeliverGateKeys(context.Background(), "node-1")
	}
	denyCount := 0
	for _, auditEntry := range fixture.auditLog.Entries() {
		if auditEntry.Action == GateKeyDeliveryAuditAction && auditEntry.Decision == AuditDecisionDeny {
			denyCount++
		}
	}
	if denyCount != 1 {
		testContext.Errorf("deny audit entries = %d, want 1", denyCount)
	}
}

func TestGateKeySecretNamePrefersKeySecret(testContext *testing.T) {
	if name := GateKeySecretName("web", ""); name != "route/web/tls/key" {
		testContext.Errorf("system key name = %q", name)
	}
	if name := GateKeySecretName("web", "platform/web-gate-key"); name != "platform/web-gate-key" {
		testContext.Errorf("key_secret name = %q", name)
	}
}

func TestStoreInlineGateKeySealsMatchingKeyAndRejectsOthers(testContext *testing.T) {
	ctx := context.Background()
	fixture, gateKeyService := newGateKeyDeliveryTestFixture(testContext)
	manualPair := issueTestGatePair(testContext, "manual.example.com")
	otherPair := issueTestGatePair(testContext, "manual.example.com")
	putTestGate(testContext, fixture, "manual-gate", "manual.example.com", types.RouteTLSModeManual)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSCert("manual-gate"), string(manualPair.CertificatePEM))

	if storeError := gateKeyService.StoreInlineGateKey(ctx, "user:admin", "manual-gate", manualPair.PrivateKeyPEM); storeError != nil {
		testContext.Fatalf("StoreInlineGateKey: %v", storeError)
	}
	sealedKey, openError := fixture.secretStore.GetSecret(ctx, types.SecretNameRouteTLSKey("manual-gate"))
	if openError != nil || string(sealedKey) != string(manualPair.PrivateKeyPEM) {
		testContext.Fatalf("sealed key not stored: %v", openError)
	}
	rawSealed, _ := fixture.rawStore.Get(ctx, "secrets/"+types.SecretNameRouteTLSKey("manual-gate"))
	if strings.Contains(string(rawSealed.Value), "PRIVATE KEY") {
		testContext.Fatal("stored value is plaintext")
	}

	putTestGate(testContext, fixture, "auto-gate", "auto.example.com", types.RouteTLSModeAuto)
	putTestGate(testContext, fixture, "secret-gate", "secret.example.com", types.RouteTLSModeManual)
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSCert("secret-gate"), string(manualPair.CertificatePEM))
	putTestFact(testContext, fixture.rawStore, types.KeyDesiredRouteTLSKeySecret("secret-gate"), "platform/key")
	rejectedCases := map[string][]byte{
		"manual-gate": otherPair.PrivateKeyPEM,  // key does not match the certificate
		"auto-gate":   manualPair.PrivateKeyPEM, // not manual tls
		"secret-gate": manualPair.PrivateKeyPEM, // uses a key secret
		"absent-gate": manualPair.PrivateKeyPEM, // not applied
		"../escape":   manualPair.PrivateKeyPEM, // invalid name
	}
	for gateName, privateKeyPEM := range rejectedCases {
		storeError := gateKeyService.StoreInlineGateKey(ctx, "user:admin", gateName, privateKeyPEM)
		var rejection *GateKeyRejection
		if !errors.As(storeError, &rejection) || strings.Contains(rejection.Error(), "PRIVATE KEY") {
			testContext.Errorf("%s: expected rejection, got %v", gateName, storeError)
		}
	}
	sealedKey, _ = fixture.secretStore.GetSecret(ctx, types.SecretNameRouteTLSKey("manual-gate"))
	if string(sealedKey) != string(manualPair.PrivateKeyPEM) {
		testContext.Error("a rejected key overwrote the sealed key")
	}
	storeAuditCount := 0
	for _, auditEntry := range fixture.auditLog.Entries() {
		if auditEntry.Action == GateKeyStoreAuditAction {
			storeAuditCount++
		}
	}
	if storeAuditCount != 1+len(rejectedCases) {
		testContext.Errorf("gate-key.store audit entries = %d, want %d", storeAuditCount, 1+len(rejectedCases))
	}
}

func TestSecretDeliveryNeverDeliversSystemOwnedGateKeys(testContext *testing.T) {
	ctx := context.Background()
	fixture, _ := newGateKeyDeliveryTestFixture(testContext)
	gatePair := issueTestGatePair(testContext, "web.example.com")
	if putError := fixture.secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey("web"), gatePair.PrivateKeyPEM); putError != nil {
		testContext.Fatal(putError)
	}
	// A service granting itself the gate key (forged or declared) is refused.
	putTestFact(testContext, fixture.rawStore, types.ScanDesiredServiceSecrets("checkout")+types.SecretNameRouteTLSKey("web"), "/run/secrets/key")
	delivered, deliveryError := fixture.deliveryService.DeliverInstanceSecrets(ctx, "node-1", "inst-1")
	refusal := expectRefusal(testContext, deliveryError, SecretDeliveryNotAuthorized)
	if delivered != nil || strings.Join(refusal.SecretNames, ",") != types.SecretNameRouteTLSKey("web") {
		testContext.Fatalf("refusal names = %v", refusal.SecretNames)
	}
}
