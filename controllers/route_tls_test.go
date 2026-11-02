// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
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

func TestRouteTLSControllerIssuesCertForAutoMode(t *testing.T) {
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
		t.Fatalf("expected 3 changes (cert, key, not-after), got %d", len(changes))
	}

	changeMap := make(map[string]string, len(changes))
	for _, change := range changes {
		changeMap[change.Key] = string(change.Value)
	}

	certPEM := changeMap[types.KeyDerivedRouteTLSCert("web-public")]
	if !strings.Contains(certPEM, "BEGIN CERTIFICATE") {
		t.Errorf("expected PEM certificate, got %q", certPEM[:min(len(certPEM), 40)])
	}

	keyPEM := changeMap[types.KeyDerivedRouteTLSKey("web-public")]
	if !strings.Contains(keyPEM, "BEGIN EC PRIVATE KEY") {
		t.Errorf("expected PEM private key, got %q", keyPEM[:min(len(keyPEM), 40)])
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
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
}

func TestRouteTLSControllerIgnoresManualMode(t *testing.T) {
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
	certificateAuthority := newTestCertificateAuthority(t)
	routeTLSController := NewRouteTLSController(certificateAuthority)

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
