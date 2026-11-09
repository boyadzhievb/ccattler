// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestCertificateAuthority creates a short-lived CA for unit tests.
func newTestCertificateAuthority(t *testing.T) *security.CertificateAuthority {
	t.Helper()
	certificateAuthority, err := security.NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("failed to create test CA: %v", err)
	}
	return certificateAuthority
}

// newTestRouteTLSController creates a route-tls controller with a test CA and
// a local secret key provider, returning the provider to open sealed keys.
func newTestRouteTLSController(t *testing.T) (*RouteTLSController, security.KeyProvider) {
	t.Helper()
	masterKey, keyError := security.GenerateMasterKey()
	if keyError != nil {
		t.Fatal(keyError)
	}
	secretKeyProvider, providerError := security.NewLocalKeyProvider(masterKey)
	if providerError != nil {
		t.Fatal(providerError)
	}
	return NewRouteTLSController(newTestCertificateAuthority(t), newTestKeyProviderSource(secretKeyProvider)), secretKeyProvider
}

// newTestKeyProviderSource wraps a key provider in a secret store, the
// KeyProviderSource the server passes to the controllers.
func newTestKeyProviderSource(secretKeyProvider security.KeyProvider) *security.SecretStore {
	return security.NewSecretStore(store.NewMemoryStore(), secretKeyProvider)
}

// openSealedTestSecret decodes and opens a value written by SealSecretValue.
func openSealedTestSecret(t *testing.T, secretKeyProvider security.KeyProvider, sealedValue string) []byte {
	t.Helper()
	envelopeBlob, decodeError := base64.StdEncoding.DecodeString(sealedValue)
	if decodeError != nil {
		t.Fatalf("sealed key is not base64: %v", decodeError)
	}
	plaintext, openError := security.OpenEnvelope(context.Background(), secretKeyProvider, envelopeBlob)
	if openError != nil {
		t.Fatalf("open sealed key: %v", openError)
	}
	return plaintext
}

func TestRouteTLSControllerIssuesCertForAutoMode(t *testing.T) {
	routeTLSController, secretKeyProvider := newTestRouteTLSController(t)

	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-public"), ""),
		kv(types.KeyDesiredRouteHost("web-public"), "app.example.com"),
		kv(types.KeyDesiredRouteService("web-public"), "frontend"),
		kv(types.KeyDesiredRouteTLSMode("web-public"), "auto"),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (cert, sealed key, not-after), got %d", len(changes))
	}

	changeMap := make(map[string]string, len(changes))
	for _, change := range changes {
		changeMap[change.Key] = string(change.Value)
		if change.Group != "route-tls-issue/web-public" {
			t.Errorf("change %s not in the issuance group: %q", change.Key, change.Group)
		}
		if strings.Contains(string(change.Value), "PRIVATE KEY") {
			t.Errorf("plaintext private key written at %s", change.Key)
		}
	}

	if _, hasPlaintextKey := changeMap[types.KeyDerivedRouteTLSKey("web-public")]; hasPlaintextKey {
		t.Error("derived plaintext key fact must not be written")
	}

	certPEM := changeMap[types.KeyDerivedRouteTLSCert("web-public")]
	if !strings.Contains(certPEM, "BEGIN CERTIFICATE") {
		t.Errorf("expected PEM certificate, got %q", certPEM[:min(len(certPEM), 40)])
	}

	sealedKey, hasSealedKey := changeMap["secrets/"+types.SecretNameRouteTLSKey("web-public")]
	if !hasSealedKey {
		t.Fatal("expected sealed key at secrets/route/web-public/tls/key")
	}
	keyPEM := openSealedTestSecret(t, secretKeyProvider, sealedKey)
	if _, pairError := tls.X509KeyPair([]byte(certPEM), keyPEM); pairError != nil {
		t.Errorf("sealed key does not match the certificate: %v", pairError)
	}

	notAfterValue := changeMap[types.KeyDerivedRouteTLSNotAfter("web-public")]
	notAfterTime, parseError := time.Parse(time.RFC3339, notAfterValue)
	if parseError != nil {
		t.Fatalf("not-after parse error: %v", parseError)
	}
	if time.Until(notAfterTime) < 89*24*time.Hour {
		t.Errorf("not-after too soon: %v (expected ~90 days)", notAfterTime)
	}
}

func TestRouteTLSControllerSkipsValidCert(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)

	futureExpiry := time.Now().Add(60 * 24 * time.Hour).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-public"), ""),
		kv(types.KeyDesiredRouteHost("web-public"), "app.example.com"),
		kv(types.KeyDesiredRouteService("web-public"), "frontend"),
		kv(types.KeyDesiredRouteTLSMode("web-public"), "auto"),
		kv(types.KeyDerivedRouteTLSCert("web-public"), "existing-cert"),
		kv(types.KeyDerivedRouteTLSKey("web-public"), "existing-key"),
		kv(types.KeyDerivedRouteTLSNotAfter("web-public"), futureExpiry),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for valid cert, got %d", len(changes))
	}
}

func TestRouteTLSControllerRenewsExpiringSoon(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)

	nearExpiry := time.Now().Add(15 * 24 * time.Hour).Format(time.RFC3339)

	facts := buildFacts(
		kv(types.KeyDesiredRoute("web-public"), ""),
		kv(types.KeyDesiredRouteHost("web-public"), "app.example.com"),
		kv(types.KeyDesiredRouteService("web-public"), "frontend"),
		kv(types.KeyDesiredRouteTLSMode("web-public"), "auto"),
		kv(types.KeyDerivedRouteTLSCert("web-public"), "old-cert"),
		kv(types.KeyDerivedRouteTLSKey("web-public"), "old-key"),
		kv(types.KeyDerivedRouteTLSNotAfter("web-public"), nearExpiry),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 3 {
		t.Fatalf("expected 3 changes for renewal, got %d", len(changes))
	}
	for _, change := range changes {
		if change.Key == types.KeyDerivedRouteTLSKey("web-public") {
			t.Errorf("renewal must neither write nor delete the pre-M176 plaintext key (op %v)", change.Type)
		}
	}
}

func TestRouteTLSControllerIgnoresManualMode(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)

	facts := buildFacts(
		kv(types.KeyDesiredRoute("internal-api"), ""),
		kv(types.KeyDesiredRouteHost("internal-api"), "api.internal"),
		kv(types.KeyDesiredRouteService("internal-api"), "api"),
		kv(types.KeyDesiredRouteTLSMode("internal-api"), "manual"),
		kv(types.KeyDesiredRouteTLSCert("internal-api"), "user-cert-pem"),
		kv(types.KeyDesiredRouteTLSKey("internal-api"), "user-key-pem"),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for manual mode, got %d", len(changes))
	}
}

func TestRouteTLSControllerCleansUpStaleDeriveds(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)

	facts := buildFacts(
		kv(types.KeyDerivedRouteTLSCert("deleted-gate"), "stale-cert"),
		kv(types.KeyDerivedRouteTLSKey("deleted-gate"), "stale-key"),
		kv(types.KeyDerivedRouteTLSNotAfter("deleted-gate"), "2027-01-01T00:00:00Z"),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 3 {
		t.Fatalf("expected 3 deletions, got %d", len(changes))
	}

	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("expected delete operation for %s, got %v", change.Key, change.Type)
		}
	}
}

func TestRouteTLSControllerNoTLSRoutesNoChanges(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)

	facts := buildFacts(
		kv(types.KeyDesiredRoute("plain-gate"), ""),
		kv(types.KeyDesiredRouteHost("plain-gate"), "app.example.com"),
		kv(types.KeyDesiredRouteService("plain-gate"), "web"),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for non-TLS gate, got %d", len(changes))
	}
}

// waitForStoreValue polls the store until the key holds a value accepted by
// the condition, failing the test after the timeout. Returns that value.
func waitForStoreValue(t *testing.T, factStore store.StateStore, key string, condition func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fact, getError := factStore.Get(context.Background(), key); getError == nil && condition(string(fact.Value)) {
			return string(fact.Value)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", key)
	return ""
}

func TestRouteTLSControllerRenewalOverwritesSealedKeyThroughRunner(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	routeTLSController, secretKeyProvider := newTestRouteTLSController(t)
	runner := NewRunner(factStore, routeTLSController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyDesiredRouteHost("web-public"), []byte("app.example.com"))
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("web-public"), []byte("auto"))

	sealedKeyPath := "secrets/" + types.SecretNameRouteTLSKey("web-public")
	nonEmpty := func(value string) bool { return value != "" }
	firstSealedKey := waitForStoreValue(t, factStore, sealedKeyPath, nonEmpty)

	// Force renewal: the sealed key now exists, so its put must pass the CAS guard.
	nearExpiry := time.Now().Add(15 * 24 * time.Hour).Format(time.RFC3339)
	factStore.Put(ctx, types.KeyDerivedRouteTLSNotAfter("web-public"), []byte(nearExpiry))

	renewedSealedKey := waitForStoreValue(t, factStore, sealedKeyPath, func(value string) bool {
		return value != "" && value != firstSealedKey
	})
	renewedCertificate := waitForStoreValue(t, factStore, types.KeyDerivedRouteTLSCert("web-public"), nonEmpty)
	keyPEM := openSealedTestSecret(t, secretKeyProvider, renewedSealedKey)
	if _, pairError := tls.X509KeyPair([]byte(renewedCertificate), keyPEM); pairError != nil {
		t.Errorf("renewed sealed key does not match the renewed certificate: %v", pairError)
	}
	if _, getError := factStore.Get(ctx, types.KeyDerivedRouteTLSKey("web-public")); getError == nil {
		t.Error("derived plaintext key fact must not exist")
	}
}

func TestRouteTLSControllerDeletesSealedKeysNoGateNeeds(t *testing.T) {
	routeTLSController, _ := newTestRouteTLSController(t)
	futureExpiry := time.Now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	sealedKey := func(gateName string) string { return "secrets/" + types.SecretNameRouteTLSKey(gateName) }

	facts := buildFacts(
		kv(types.KeyDesiredRouteHost("auto-gate"), "auto.example.com"),
		kv(types.KeyDesiredRouteTLSMode("auto-gate"), "auto"),
		kv(types.KeyDerivedRouteTLSCert("auto-gate"), "cert"),
		kv(types.KeyDerivedRouteTLSNotAfter("auto-gate"), futureExpiry),
		kv(sealedKey("auto-gate"), "sealed"),
		kv(types.KeyDesiredRouteHost("inline-gate"), "inline.example.com"),
		kv(types.KeyDesiredRouteTLSMode("inline-gate"), "manual"),
		kv(sealedKey("inline-gate"), "sealed"),
		kv(types.KeyDesiredRouteHost("secret-gate"), "secret.example.com"),
		kv(types.KeyDesiredRouteTLSMode("secret-gate"), "manual"),
		kv(types.KeyDesiredRouteTLSKeySecret("secret-gate"), "platform/key"),
		kv(sealedKey("secret-gate"), "sealed"),
		kv(types.KeyDesiredRouteHost("plain-gate"), "plain.example.com"),
		kv(sealedKey("plain-gate"), "sealed"),
		kv(sealedKey("deleted-gate"), "sealed"),
	)

	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	deletedKeys := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("unexpected put of %s", change.Key)
			continue
		}
		deletedKeys = append(deletedKeys, change.Key)
	}
	expectedDeletions := []string{sealedKey("deleted-gate"), sealedKey("plain-gate"), sealedKey("secret-gate")}
	if strings.Join(deletedKeys, ",") != strings.Join(expectedDeletions, ",") {
		t.Errorf("deleted %v, want %v", deletedKeys, expectedDeletions)
	}
}

func TestRouteTLSControllerSealsWithRotatedKeyAndReissuesLostSealedKey(t *testing.T) {
	_, originalProvider := newTestRouteTLSController(t)
	secretStore := newTestKeyProviderSource(originalProvider)
	routeTLSController := NewRouteTLSController(newTestCertificateAuthority(t), secretStore)
	rotatedMasterKey, _ := security.GenerateMasterKey()
	rotatedProvider, _ := security.NewLocalKeyProvider(rotatedMasterKey)
	if rotateError := secretStore.RotateKeyProvider(context.Background(), rotatedProvider); rotateError != nil {
		t.Fatal(rotateError)
	}
	futureExpiry := time.Now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	// Valid certificate, but the sealed key is gone and no plaintext key waits.
	facts := buildFacts(
		kv(types.KeyDesiredRouteHost("web-public"), "app.example.com"),
		kv(types.KeyDesiredRouteTLSMode("web-public"), "auto"),
		kv(types.KeyDerivedRouteTLSCert("web-public"), "existing-cert"),
		kv(types.KeyDerivedRouteTLSNotAfter("web-public"), futureExpiry),
	)
	changes, err := routeTLSController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	var sealedValue string
	for _, change := range changes {
		if change.Key == "secrets/"+types.SecretNameRouteTLSKey("web-public") {
			sealedValue = string(change.Value)
		}
	}
	if sealedValue == "" {
		t.Fatalf("lost sealed key not re-issued: %d changes", len(changes))
	}
	if opened := openSealedTestSecret(t, rotatedProvider, sealedValue); !strings.Contains(string(opened), "PRIVATE KEY") {
		t.Error("key not sealed with the rotated provider")
	}
}
