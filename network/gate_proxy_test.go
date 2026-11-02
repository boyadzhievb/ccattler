// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// testGateCertificate is a self-signed certificate for one gate host.
type testGateCertificate struct {
	certificatePEM string            // certificatePEM is the PEM-encoded certificate.
	keyPEM         string            // keyPEM is the PEM-encoded private key.
	parsed         *x509.Certificate // parsed is the certificate, for client trust pools.
}

// generateTestGateCertificate creates a self-signed certificate whose only DNS
// name is hostname.
func generateTestGateCertificate(t *testing.T, hostname string) testGateCertificate {
	t.Helper()
	privateKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		t.Fatal(keyError)
	}
	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	certificateTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificateDER, createError := x509.CreateCertificate(rand.Reader, certificateTemplate, certificateTemplate, &privateKey.PublicKey, privateKey)
	if createError != nil {
		t.Fatal(createError)
	}
	keyDER, marshalError := x509.MarshalECPrivateKey(privateKey)
	if marshalError != nil {
		t.Fatal(marshalError)
	}
	parsedCertificate, _ := x509.ParseCertificate(certificateDER)
	return testGateCertificate{
		certificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})),
		keyPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		parsed:         parsedCertificate,
	}
}

// writeTestGateFacts writes the desired host and service facts for a gate.
func writeTestGateFacts(factStore store.StateStore, gateName, hostname, serviceName string) {
	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredRoute(gateName), []byte(""))
	factStore.Put(ctx, types.KeyDesiredRouteHost(gateName), []byte(hostname))
	factStore.Put(ctx, types.KeyDesiredRouteService(gateName), []byte(serviceName))
}

// writeTestGateCertificate stores a gate certificate as derived (auto) facts.
func writeTestGateCertificate(factStore store.StateStore, gateName string, gateCertificate testGateCertificate) {
	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode(gateName), []byte(types.RouteTLSModeAuto))
	factStore.Put(ctx, types.KeyDerivedRouteTLSCert(gateName), []byte(gateCertificate.certificatePEM))
	factStore.Put(ctx, types.KeyDerivedRouteTLSKey(gateName), []byte(gateCertificate.keyPEM))
}

// startTestBackends starts HTTP backends that echo their index, and returns
// a resolver mapping serviceName to them plus a cleanup function.
func startTestBackends(t *testing.T, serviceName string, backendCount int) (*mockResolver, func()) {
	t.Helper()
	var endpoints []types.Endpoint
	var servers []*httptest.Server
	for backendIndex := 0; backendIndex < backendCount; backendIndex++ {
		backendName := fmt.Sprintf("backend-%d", backendIndex)
		backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(responseWriter, backendName)
		}))
		servers = append(servers, backendServer)
		backendHost, backendPort := parseHostPort(backendServer.URL)
		endpoints = append(endpoints, types.Endpoint{Service: serviceName, InstanceID: backendName, IP: backendHost, Port: backendPort})
	}
	resolver := &mockResolver{endpoints: map[string][]types.Endpoint{serviceName: endpoints}}
	return resolver, func() {
		for _, backendServer := range servers {
			backendServer.Close()
		}
	}
}

// startTestGateHTTPSServer serves proxy over TLS using the gate certificate
// callback, mirroring StartTLS without binding a fixed port.
func startTestGateHTTPSServer(proxy *UserSpaceProxy) *httptest.Server {
	httpsServer := httptest.NewUnstartedServer(proxy)
	httpsServer.TLS = &tls.Config{GetCertificate: proxy.getGateCertificate, MinVersion: tls.VersionTLS12}
	httpsServer.StartTLS()
	return httpsServer
}

// newTestGateClient returns an HTTPS client that trusts the given certificates,
// sends serverName as SNI, and does not follow redirects.
func newTestGateClient(httpsServer *httptest.Server, serverName string, trustedCertificates ...testGateCertificate) *http.Client {
	trustPool := x509.NewCertPool()
	for _, trustedCertificate := range trustedCertificates {
		trustPool.AddCert(trustedCertificate.parsed)
	}
	transport := httpsServer.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: trustPool, ServerName: serverName, MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// sendGateRequest issues GET / to the HTTPS server with the given Host header.
func sendGateRequest(t *testing.T, client *http.Client, httpsServer *httptest.Server, hostHeader string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, httpsServer.URL+"/", nil)
	request.Host = hostHeader
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response, requestError := client.Do(request)
	if requestError != nil {
		t.Fatalf("request for host %q failed: %v", hostHeader, requestError)
	}
	return response
}

// TestBuildGateHostEntriesCertificatePrecedence verifies a manual (desired)
// certificate wins over a derived one, and pending TLS has no certificate.
func TestBuildGateHostEntriesCertificatePrecedence(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()
	manualCertificate := generateTestGateCertificate(t, "shop.example.com")
	staleCertificate := generateTestGateCertificate(t, "stale.example.com")

	writeTestGateFacts(factStore, "shop", "shop.example.com", "store")
	writeTestGateCertificate(factStore, "shop", staleCertificate)
	factStore.Put(ctx, types.KeyDesiredRouteTLSCert("shop"), []byte(manualCertificate.certificatePEM))
	factStore.Put(ctx, types.KeyDesiredRouteTLSKey("shop"), []byte(manualCertificate.keyPEM))
	writeTestGateFacts(factStore, "pending", "pending.example.com", "store")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("pending"), []byte(types.RouteTLSModeAuto))
	factStore.Put(ctx, types.KeyDesiredRouteService("no-host"), []byte("store"))

	desiredFacts, _ := factStore.Scan(ctx, types.ScanDesiredRoutes)
	derivedFacts, _ := factStore.Scan(ctx, types.ScanDerivedRoutes)
	gateHosts := buildGateHostEntries(desiredFacts, derivedFacts)

	if len(gateHosts) != 2 {
		t.Fatalf("expected 2 gate hosts (no-host skipped), got %d", len(gateHosts))
	}
	shopCertificate := gateHosts["shop.example.com"].certificate
	if shopCertificate == nil || shopCertificate.Leaf == nil || shopCertificate.Leaf.DNSNames[0] != "shop.example.com" {
		t.Errorf("expected manual certificate for shop.example.com, got %+v", shopCertificate)
	}
	pendingGate := gateHosts["pending.example.com"]
	if !pendingGate.tlsEnabled || pendingGate.certificate != nil {
		t.Errorf("expected pending TLS gate without certificate, got %+v", pendingGate)
	}
}

// TestBuildGateHostEntriesInvalidCertificate verifies malformed PEM yields no
// certificate instead of an error or panic.
func TestBuildGateHostEntriesInvalidCertificate(t *testing.T) {
	factStore := store.NewMemoryStore()
	writeTestGateFacts(factStore, "broken", "broken.example.com", "web")
	writeTestGateCertificate(factStore, "broken", testGateCertificate{certificatePEM: "not pem", keyPEM: "not pem"})

	desiredFacts, _ := factStore.Scan(context.Background(), types.ScanDesiredRoutes)
	derivedFacts, _ := factStore.Scan(context.Background(), types.ScanDerivedRoutes)
	gateHosts := buildGateHostEntries(desiredFacts, derivedFacts)

	if gateHosts["broken.example.com"].certificate != nil {
		t.Error("expected nil certificate for malformed PEM")
	}
}

// TestGateHTTPSServesCertificateBySNI verifies two gates on one service each
// present their own certificate selected by SNI and reach the backend.
func TestGateHTTPSServesCertificateBySNI(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	primaryCertificate := generateTestGateCertificate(t, "web.example.com")
	alternateCertificate := generateTestGateCertificate(t, "web-alt.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(factStore, "web", primaryCertificate)
	writeTestGateFacts(factStore, "web-alt", "web-alt.example.com", "web")
	writeTestGateCertificate(factStore, "web-alt", alternateCertificate)

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	httpsServer := startTestGateHTTPSServer(proxy)
	defer httpsServer.Close()

	for hostname, expectedCertificate := range map[string]testGateCertificate{
		"web.example.com": primaryCertificate, "web-alt.example.com": alternateCertificate,
	} {
		response := sendGateRequest(t, newTestGateClient(httpsServer, hostname, expectedCertificate), httpsServer, hostname)
		responseBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || string(responseBody) != "backend-0" {
			t.Errorf("%s: expected 200 from backend-0, got %d %q", hostname, response.StatusCode, responseBody)
		}
		if served := response.TLS.PeerCertificates[0]; !served.Equal(expectedCertificate.parsed) {
			t.Errorf("%s: served certificate for %v", hostname, served.DNSNames)
		}
	}
}

// TestGateHTTPSRejectsUnknownServerName verifies the handshake fails for an
// SNI that names no gate (no default certificate).
func TestGateHTTPSRejectsUnknownServerName(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	gateCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(factStore, "web", gateCertificate)

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	httpsServer := startTestGateHTTPSServer(proxy)
	defer httpsServer.Close()

	request, _ := http.NewRequest(http.MethodGet, httpsServer.URL+"/", nil)
	request.Host = "unknown.example.com"
	_, requestError := newTestGateClient(httpsServer, "unknown.example.com", gateCertificate).Do(request)
	if requestError == nil {
		t.Fatal("expected TLS handshake failure for unknown SNI")
	}
}

// TestGateHTTPSMisdirectedRequest verifies an HTTPS request whose Host names a
// different gate than its SNI gets 421.
func TestGateHTTPSMisdirectedRequest(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	webCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(factStore, "web", webCertificate)
	writeTestGateFacts(factStore, "admin", "admin.example.com", "web")
	writeTestGateCertificate(factStore, "admin", generateTestGateCertificate(t, "admin.example.com"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	httpsServer := startTestGateHTTPSServer(proxy)
	defer httpsServer.Close()

	client := newTestGateClient(httpsServer, "web.example.com", webCertificate)
	for _, hostHeader := range []string{"admin.example.com", "web"} {
		response := sendGateRequest(t, client, httpsServer, hostHeader)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("Host %q with SNI web.example.com: expected 421, got %d", hostHeader, response.StatusCode)
		}
	}
}

// TestGateHTTPRedirectsToHTTPS verifies the plain listener redirects a gate
// with an available certificate (308) and serves a pending TLS gate directly.
func TestGateHTTPRedirectsToHTTPS(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(factStore, "web", generateTestGateCertificate(t, "web.example.com"))
	writeTestGateFacts(factStore, "pending", "pending.example.com", "web")
	factStore.Put(context.Background(), types.KeyDesiredRouteTLSMode("pending"), []byte(types.RouteTLSModeAuto))
	proxy := NewUserSpaceProxy(resolver, ":0", factStore)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/path?query=1", nil)
	request.Host = "web.example.com:8080"
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPermanentRedirect || recorder.Header().Get("Location") != "https://web.example.com/path?query=1" {
		t.Errorf("expected 308 to https://web.example.com/path?query=1, got %d %q", recorder.Code, recorder.Header().Get("Location"))
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "pending.example.com"
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("pending TLS gate over HTTP: expected 200, got %d", recorder.Code)
	}
}

// TestGateHTTPSStickyCookie verifies a cookie-sticky gate over HTTPS sets a
// Secure cookie with the default per-service name and pins later requests.
func TestGateHTTPSStickyCookie(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "api", 3)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	gateCertificate := generateTestGateCertificate(t, "api.example.com")
	writeTestGateFacts(factStore, "api", "api.example.com", "api")
	writeTestGateCertificate(factStore, "api", gateCertificate)
	factStore.Put(context.Background(), types.KeyDesiredRouteSticky("api"), []byte("cookie"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	httpsServer := startTestGateHTTPSServer(proxy)
	defer httpsServer.Close()
	client := newTestGateClient(httpsServer, "api.example.com", gateCertificate)

	firstResponse := sendGateRequest(t, client, httpsServer, "api.example.com")
	firstBody, _ := io.ReadAll(firstResponse.Body)
	_ = firstResponse.Body.Close()
	setCookieHeader := firstResponse.Header.Get("Set-Cookie")
	if !strings.HasPrefix(setCookieHeader, "__CCA_STICKY_api=") || !strings.Contains(setCookieHeader, "; Secure") {
		t.Fatalf("expected Secure __CCA_STICKY_api cookie, got %q", setCookieHeader)
	}

	stickyCookie := firstResponse.Cookies()[0]
	for requestIndex := 0; requestIndex < 10; requestIndex++ {
		response := sendGateRequest(t, client, httpsServer, "api.example.com", stickyCookie)
		responseBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if string(responseBody) != string(firstBody) {
			t.Fatalf("request %d reached %q, expected sticky %q", requestIndex, responseBody, firstBody)
		}
	}
}

// TestGateCertificateRotationAfterCacheExpiry verifies a rotated certificate
// is served once the gate table cache expires.
func TestGateCertificateRotationAfterCacheExpiry(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	originalCertificate := generateTestGateCertificate(t, "web.example.com")
	rotatedCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(factStore, "web", originalCertificate)

	currentTime := time.Now()
	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.timeNow = func() time.Time { return currentTime }
	clientHello := &tls.ClientHelloInfo{ServerName: "web.example.com"}

	servedCertificate, _ := proxy.getGateCertificate(clientHello)
	if servedCertificate == nil || !servedCertificate.Leaf.Equal(originalCertificate.parsed) {
		t.Fatal("expected original certificate before rotation")
	}

	writeTestGateCertificate(factStore, "web", rotatedCertificate)
	currentTime = currentTime.Add(routeHostCacheTTL + time.Second)

	servedCertificate, _ = proxy.getGateCertificate(clientHello)
	if servedCertificate == nil || !servedCertificate.Leaf.Equal(rotatedCertificate.parsed) {
		t.Error("expected rotated certificate after cache expiry")
	}
}
