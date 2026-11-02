// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// generateTestGateCertificatePEM returns a self-signed PEM certificate for
// hostname that expires at notAfter.
func generateTestGateCertificatePEM(t *testing.T, hostname string, notAfter time.Time) string {
	t.Helper()
	privateKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		t.Fatal(keyError)
	}
	certificateTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    notAfter.Add(-time.Hour),
		NotAfter:     notAfter,
	}
	certificateDER, createError := x509.CreateCertificate(rand.Reader, certificateTemplate, certificateTemplate, &privateKey.PublicKey, privateKey)
	if createError != nil {
		t.Fatal(createError)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}))
}

// writeTestGate writes the core desired facts for a gate.
func writeTestGate(ctx context.Context, factStore store.StateStore, gateName, host, serviceName string) {
	factStore.Put(ctx, types.KeyDesiredRoute(gateName), []byte(""))
	factStore.Put(ctx, types.KeyDesiredRouteHost(gateName), []byte(host))
	factStore.Put(ctx, types.KeyDesiredRouteService(gateName), []byte(serviceName))
}

// TestCollectGateStatusPlainHTTP verifies a gate without TLS reports routing
// fields, backends, and a nil TLS section.
func TestCollectGateStatusPlainHTTP(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	writeTestGate(ctx, factStore, "web", "web.example.com", "frontend")
	factStore.Put(ctx, types.KeyDesiredRoutePath("web"), []byte("/app"))
	factStore.Put(ctx, types.KeyDesiredRoutePort("web"), []byte("8080"))
	factStore.Put(ctx, types.KeyDesiredRouteSticky("web"), []byte("cookie"))
	factStore.Put(ctx, types.KeyDerivedRouteBackends("web"), []byte("10.100.1.2:8080,10.100.2.3:8080"))

	gates := collectGateStatusFromStore(ctx, factStore)

	if len(gates) != 1 {
		t.Fatalf("expected 1 gate, got %d", len(gates))
	}
	gate := gates[0]
	if gate.Name != "web" || gate.Host != "web.example.com" || gate.Path != "/app" {
		t.Errorf("routing fields: got name=%q host=%q path=%q", gate.Name, gate.Host, gate.Path)
	}
	if gate.Service != "frontend" || gate.Port != 8080 || gate.Sticky != "cookie" {
		t.Errorf("target fields: got service=%q port=%d sticky=%q", gate.Service, gate.Port, gate.Sticky)
	}
	if len(gate.Backends) != 2 || gate.Backends[1] != "10.100.2.3:8080" {
		t.Errorf("backends: got %v", gate.Backends)
	}
	if gate.TLS != nil {
		t.Errorf("expected nil TLS, got %+v", gate.TLS)
	}
}

// TestCollectGateStatusAutoTLSPending verifies an auto TLS gate with no issued
// certificate reports mode auto and CertificateSet false.
func TestCollectGateStatusAutoTLSPending(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	writeTestGate(ctx, factStore, "api", "api.example.com", "backend")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("api"), []byte("auto"))

	gates := collectGateStatusFromStore(ctx, factStore)

	if len(gates) != 1 || gates[0].TLS == nil {
		t.Fatalf("expected 1 gate with TLS, got %+v", gates)
	}
	if gates[0].TLS.Mode != "auto" || gates[0].TLS.CertificateSet {
		t.Errorf("expected pending auto TLS, got %+v", gates[0].TLS)
	}
}

// TestCollectGateStatusAutoTLSIssued verifies an auto TLS gate reads expiry
// and hostname from the CA-issued certificate in derived facts.
func TestCollectGateStatusAutoTLSIssued(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()
	certificateExpiry := time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)

	writeTestGate(ctx, factStore, "api", "api.example.com", "backend")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("api"), []byte("auto"))
	factStore.Put(ctx, types.KeyDerivedRouteTLSCert("api"), []byte(generateTestGateCertificatePEM(t, "api.example.com", certificateExpiry)))
	factStore.Put(ctx, types.KeyDerivedRouteTLSKey("api"), []byte("secret-key-material"))
	factStore.Put(ctx, types.KeyDerivedRouteTLSNotAfter("api"), []byte(certificateExpiry.Format(time.RFC3339)))

	gates := collectGateStatusFromStore(ctx, factStore)

	gateTLS := gates[0].TLS
	if gateTLS == nil || !gateTLS.CertificateSet {
		t.Fatalf("expected issued TLS, got %+v", gateTLS)
	}
	if gateTLS.NotAfter != "2027-01-31T12:00:00Z" {
		t.Errorf("not after: got %q", gateTLS.NotAfter)
	}
	if gateTLS.CertificateHost != "api.example.com" {
		t.Errorf("certificate host: got %q", gateTLS.CertificateHost)
	}
	encodedStatus, _ := json.Marshal(gates[0])
	if strings.Contains(string(encodedStatus), "secret-key-material") || strings.Contains(string(encodedStatus), "BEGIN") {
		t.Errorf("gate status must not expose PEM material: %s", encodedStatus)
	}
}

// TestCollectGateStatusManualTLSPrecedence verifies a manual certificate in
// desired facts takes precedence over a derived certificate.
func TestCollectGateStatusManualTLSPrecedence(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()
	manualExpiry := time.Date(2028, 6, 1, 0, 0, 0, 0, time.UTC)
	derivedExpiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	writeTestGate(ctx, factStore, "shop", "shop.example.com", "store")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("shop"), []byte("manual"))
	factStore.Put(ctx, types.KeyDesiredRouteTLSCert("shop"), []byte(generateTestGateCertificatePEM(t, "shop.example.com", manualExpiry)))
	factStore.Put(ctx, types.KeyDerivedRouteTLSCert("shop"), []byte(generateTestGateCertificatePEM(t, "stale.example.com", derivedExpiry)))

	gates := collectGateStatusFromStore(ctx, factStore)

	gateTLS := gates[0].TLS
	if gateTLS == nil || gateTLS.Mode != "manual" || !gateTLS.CertificateSet {
		t.Fatalf("expected manual TLS with certificate, got %+v", gateTLS)
	}
	if gateTLS.NotAfter != "2028-06-01T00:00:00Z" || gateTLS.CertificateHost != "shop.example.com" {
		t.Errorf("expected manual cert details, got %+v", gateTLS)
	}
}

// TestBuildGateDescribeIgnoresPrefixSibling verifies describing "web" does not
// merge facts from a gate named "web2" that shares the key prefix.
func TestBuildGateDescribeIgnoresPrefixSibling(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	writeTestGate(ctx, factStore, "web", "web.example.com", "frontend")
	writeTestGate(ctx, factStore, "web2", "web2.example.com", "frontend-v2")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("web2"), []byte("auto"))

	gateDetail, describeError := buildGateDescribe(ctx, factStore, "web")
	if describeError != nil {
		t.Fatal(describeError)
	}
	if gateDetail.Host != "web.example.com" || gateDetail.Service != "frontend" || gateDetail.TLS != nil {
		t.Errorf("expected only web facts, got %+v", gateDetail)
	}
	if _, missingError := buildGateDescribe(ctx, factStore, "we"); missingError == nil {
		t.Error("expected not-found error for gate \"we\"")
	}
}

// TestDescribeGateEndpoint verifies GET /api/describe?type=gate returns the
// gate detail and 404 for an unknown gate.
func TestDescribeGateEndpoint(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()
	ctx := context.Background()

	writeTestGate(ctx, factStore, "web", "web.example.com", "frontend")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("web"), []byte("auto"))

	resp, err := http.Get(baseURL + "/api/describe?type=gate&name=web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	var gateDetail GateStatus
	if decodeError := json.NewDecoder(resp.Body).Decode(&gateDetail); decodeError != nil {
		t.Fatal(decodeError)
	}
	if gateDetail.Name != "web" || gateDetail.TLS == nil || gateDetail.TLS.Mode != "auto" {
		t.Errorf("unexpected gate detail: %+v", gateDetail)
	}

	missingResp, err := http.Get(baseURL + "/api/describe?type=gate&name=nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer missingResp.Body.Close()
	if missingResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown gate, got %d", missingResp.StatusCode)
	}
}

// TestBuildStatusFiltersGatesByTenant verifies a tenant principal sees only
// gates targeting services owned by its tenant.
func TestBuildStatusFiltersGatesByTenant(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	for _, serviceName := range []string{"checkout", "search"} {
		factStore.Put(ctx, types.KeyDesiredService(serviceName), []byte(""))
	}
	factStore.Put(ctx, types.KeyDesiredServiceOwner("checkout"), []byte("payments"))
	factStore.Put(ctx, types.KeyDesiredServiceOwner("search"), []byte("discovery"))
	writeTestGate(ctx, factStore, "pay", "pay.example.com", "checkout")
	writeTestGate(ctx, factStore, "find", "find.example.com", "search")

	paymentsPrincipal := security.Principal{
		Kind: security.PrincipalKindUser, Name: "alice",
		Attributes: []security.Attribute{{Key: "team", Value: "payments"}},
	}
	tenantStatus := buildStatusFromStore(ctx, factStore, &paymentsPrincipal, false)
	if len(tenantStatus.Gates) != 1 || tenantStatus.Gates[0].Name != "pay" {
		t.Errorf("tenant should see only gate pay, got %+v", tenantStatus.Gates)
	}

	platformStatus := buildStatusFromStore(ctx, factStore, nil, false)
	if len(platformStatus.Gates) != 2 {
		t.Errorf("platform should see 2 gates, got %d", len(platformStatus.Gates))
	}
}
