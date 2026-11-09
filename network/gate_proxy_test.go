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
	"sync"
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

// testGateCertificateSource is an in-memory GateCertificateSource standing in
// for the agent's gate-key cache.
type testGateCertificateSource struct {
	mutex        sync.Mutex                  // mutex guards certificates.
	certificates map[string]*tls.Certificate // certificates maps gate name to its pair.
}

// GateCertificate returns the gate's pair, or nil.
func (certificateSource *testGateCertificateSource) GateCertificate(gateName string) *tls.Certificate {
	certificateSource.mutex.Lock()
	defer certificateSource.mutex.Unlock()
	return certificateSource.certificates[gateName]
}

// newTestGateCertificateSource returns an empty test certificate source.
func newTestGateCertificateSource() *testGateCertificateSource {
	return &testGateCertificateSource{certificates: make(map[string]*tls.Certificate)}
}

// writeTestGateCertificate marks the gate as auto TLS and installs the pair in
// the certificate source, as a gate-key delivery would.
func writeTestGateCertificate(t *testing.T, factStore store.StateStore, certificateSource *testGateCertificateSource, gateName string, gateCertificate testGateCertificate) {
	t.Helper()
	factStore.Put(context.Background(), types.KeyDesiredRouteTLSMode(gateName), []byte(types.RouteTLSModeAuto))
	certificate, pairError := tls.X509KeyPair([]byte(gateCertificate.certificatePEM), []byte(gateCertificate.keyPEM))
	if pairError != nil {
		t.Fatalf("test pair: %v", pairError)
	}
	certificateSource.mutex.Lock()
	certificateSource.certificates[gateName] = &certificate
	certificateSource.mutex.Unlock()
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

// TestBuildGateHostEntriesSkipsIncompleteGates verifies gates without a host
// are skipped and TLS gates are flagged even before a pair is delivered.
func TestBuildGateHostEntriesSkipsIncompleteGates(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()
	writeTestGateFacts(factStore, "pending", "pending.example.com", "store")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("pending"), []byte(types.RouteTLSModeAuto))
	writeTestGateFacts(factStore, "plain", "plain.example.com", "store")
	factStore.Put(ctx, types.KeyDesiredRouteService("no-host"), []byte("store"))

	desiredFacts, _ := factStore.Scan(ctx, types.ScanDesiredRoutes)
	gateHosts := buildGateHostEntries(desiredFacts)

	if len(gateHosts) != 2 {
		t.Fatalf("expected 2 gate hosts (no-host skipped), got %d", len(gateHosts))
	}
	if !gateHosts["pending.example.com"].tlsEnabled || gateHosts["plain.example.com"].tlsEnabled {
		t.Errorf("unexpected tls flags: %+v %+v", gateHosts["pending.example.com"], gateHosts["plain.example.com"])
	}
}

// TestGateCertificateIgnoresKeyFactsAndNeedsSource verifies the proxy never
// pairs a certificate fact with a key fact: without a delivered pair (or
// without a source) the handshake has no certificate.
func TestGateCertificateIgnoresKeyFactsAndNeedsSource(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()
	plaintextCertificate := generateTestGateCertificate(t, "shop.example.com")
	writeTestGateFacts(factStore, "shop", "shop.example.com", "store")
	factStore.Put(ctx, types.KeyDesiredRouteTLSMode("shop"), []byte(types.RouteTLSModeManual))
	factStore.Put(ctx, types.KeyDesiredRouteTLSCert("shop"), []byte(plaintextCertificate.certificatePEM))
	factStore.Put(ctx, types.KeyDesiredRouteTLSKey("shop"), []byte(plaintextCertificate.keyPEM))
	clientHello := &tls.ClientHelloInfo{ServerName: "shop.example.com"}

	proxy := NewUserSpaceProxy(&mockResolver{}, ":0", factStore)
	if served, _ := proxy.getGateCertificate(clientHello); served != nil {
		t.Fatal("served a certificate without a certificate source")
	}
	certificateSource := newTestGateCertificateSource()
	proxy.SetGateCertificateSource(certificateSource)
	if served, _ := proxy.getGateCertificate(clientHello); served != nil {
		t.Fatal("served a certificate built from key facts")
	}
	writeTestGateCertificate(t, factStore, certificateSource, "shop", plaintextCertificate)
	if served, _ := proxy.getGateCertificate(clientHello); served == nil {
		t.Fatal("delivered pair not served")
	}
}

// TestGateHTTPSServesCertificateBySNI verifies two gates on one service each
// present their own certificate selected by SNI and reach the backend.
func TestGateHTTPSServesCertificateBySNI(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	certificateSource := newTestGateCertificateSource()
	primaryCertificate := generateTestGateCertificate(t, "web.example.com")
	alternateCertificate := generateTestGateCertificate(t, "web-alt.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web", primaryCertificate)
	writeTestGateFacts(factStore, "web-alt", "web-alt.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web-alt", alternateCertificate)

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)
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
	certificateSource := newTestGateCertificateSource()
	gateCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web", gateCertificate)

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)
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
	certificateSource := newTestGateCertificateSource()
	webCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web", webCertificate)
	writeTestGateFacts(factStore, "admin", "admin.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "admin", generateTestGateCertificate(t, "admin.example.com"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)
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
	certificateSource := newTestGateCertificateSource()
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web", generateTestGateCertificate(t, "web.example.com"))
	writeTestGateFacts(factStore, "pending", "pending.example.com", "web")
	factStore.Put(context.Background(), types.KeyDesiredRouteTLSMode("pending"), []byte(types.RouteTLSModeAuto))
	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)

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
	certificateSource := newTestGateCertificateSource()
	gateCertificate := generateTestGateCertificate(t, "api.example.com")
	writeTestGateFacts(factStore, "api", "api.example.com", "api")
	writeTestGateCertificate(t, factStore, certificateSource, "api", gateCertificate)
	factStore.Put(context.Background(), types.KeyDesiredRouteSticky("api"), []byte("cookie"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)
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

// TestGateCertificateRotationIsServedImmediately verifies a pair replaced in
// the certificate source is served on the next handshake, without waiting for
// the gate table cache to expire.
func TestGateCertificateRotationIsServedImmediately(t *testing.T) {
	resolver, cleanupBackends := startTestBackends(t, "web", 1)
	defer cleanupBackends()
	factStore := store.NewMemoryStore()
	certificateSource := newTestGateCertificateSource()
	originalCertificate := generateTestGateCertificate(t, "web.example.com")
	rotatedCertificate := generateTestGateCertificate(t, "web.example.com")
	writeTestGateFacts(factStore, "web", "web.example.com", "web")
	writeTestGateCertificate(t, factStore, certificateSource, "web", originalCertificate)

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxy.SetGateCertificateSource(certificateSource)
	clientHello := &tls.ClientHelloInfo{ServerName: "web.example.com"}

	servedCertificate, _ := proxy.getGateCertificate(clientHello)
	if servedCertificate == nil || !servedCertificate.Leaf.Equal(originalCertificate.parsed) {
		t.Fatal("expected original certificate before rotation")
	}

	writeTestGateCertificate(t, factStore, certificateSource, "web", rotatedCertificate)

	servedCertificate, _ = proxy.getGateCertificate(clientHello)
	if servedCertificate == nil || !servedCertificate.Leaf.Equal(rotatedCertificate.parsed) {
		t.Error("expected rotated certificate on the next handshake")
	}
}
