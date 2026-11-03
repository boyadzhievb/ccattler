// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// observedAgentRequest records what the stub agent received.
type observedAgentRequest struct {
	path          string // path is the request path seen by the agent.
	rawQuery      string // rawQuery is the query string seen by the agent.
	authorization string // authorization is the Authorization header seen by the agent.
	clientCN      string // clientCN is the CN of the client certificate the agent saw.
}

// debugProxyFixture is an API server wired to a stub mTLS agent.
type debugProxyFixture struct {
	apiServer      *Server              // apiServer is the server under test.
	factStore      store.StateStore     // factStore holds debug addresses.
	agentURL       string               // agentURL is the stub agent's https:// base URL.
	observedMutex  sync.Mutex           // observedMutex guards lastObserved.
	lastObserved   observedAgentRequest // lastObserved is the most recent request at the stub agent.
	apiAuthorizer  *security.APIAuthorizer
	agentCallCount int // agentCallCount counts requests that reached the stub agent.
}

// issueProxyTestKeyPair issues a CA-signed key pair for commonName.
func issueProxyTestKeyPair(t *testing.T, certificateAuthority *security.CertificateAuthority, commonName string) tls.Certificate {
	t.Helper()
	issuedCertificate, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{
		CommonName:  commonName,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		TTL:         time.Hour,
	})
	if issueError != nil {
		t.Fatalf("issue certificate: %v", issueError)
	}
	keyPair, keyPairError := tls.X509KeyPair(issuedCertificate.CertificatePEM, issuedCertificate.PrivateKeyPEM)
	if keyPairError != nil {
		t.Fatalf("key pair: %v", keyPairError)
	}
	return keyPair
}

// newDebugProxyFixture starts a stub agent serving TLS as agentCommonName and
// requiring a cluster-CA client certificate, and an API server whose client
// TLS uses a "ccattler-server" certificate. node "worker-1" points at the stub.
func newDebugProxyFixture(t *testing.T, agentCommonName string) *debugProxyFixture {
	t.Helper()
	certificateAuthority, caError := security.NewCertificateAuthority(24 * time.Hour)
	if caError != nil {
		t.Fatalf("create CA: %v", caError)
	}
	clusterRoots := x509.NewCertPool()
	clusterRoots.AppendCertsFromPEM(certificateAuthority.CACertificatePEM())

	fixture := &debugProxyFixture{}
	stubAgent := httptest.NewUnstartedServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		fixture.observedMutex.Lock()
		fixture.agentCallCount++
		fixture.lastObserved = observedAgentRequest{
			path:          request.URL.Path,
			rawQuery:      request.URL.RawQuery,
			authorization: request.Header.Get("Authorization"),
			clientCN:      request.TLS.PeerCertificates[0].Subject.CommonName,
		}
		fixture.observedMutex.Unlock()
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"node_id":"worker-1"}`))
	}))
	stubAgent.TLS = &tls.Config{
		Certificates: []tls.Certificate{issueProxyTestKeyPair(t, certificateAuthority, agentCommonName)},
		ClientCAs:    clusterRoots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	stubAgent.StartTLS()
	t.Cleanup(stubAgent.Close)
	fixture.agentURL = stubAgent.URL

	fixture.factStore = store.NewMemoryStore()
	t.Cleanup(func() { fixture.factStore.Close() })
	fixture.publishDebugAddress(t, "worker-1", stubAgent.URL)

	fixture.apiAuthorizer = security.NewAPIAuthorizer()
	fixture.apiServer = NewServer(fixture.factStore)
	fixture.apiServer.SetAPIAuthorizer(fixture.apiAuthorizer)
	fixture.apiServer.SetAgentDebugClientTLS(&tls.Config{
		Certificates: []tls.Certificate{issueProxyTestKeyPair(t, certificateAuthority, "ccattler-server")},
		RootCAs:      clusterRoots,
		MinVersion:   tls.VersionTLS13,
	})
	return fixture
}

// publishDebugAddress writes a node's debug address fact.
func (fixture *debugProxyFixture) publishDebugAddress(t *testing.T, nodeID string, debugAddress string) {
	t.Helper()
	if _, putError := fixture.factStore.Put(context.Background(), types.KeyObservedNodeDebugAddress(nodeID), []byte(debugAddress)); putError != nil {
		t.Fatalf("publish debug address: %v", putError)
	}
}

// serveAs sends a request through the API handler as user principalName with
// a bearer token, and returns the recorded response.
func (fixture *debugProxyFixture) serveAs(principalName string, method string, requestPath string) *httptest.ResponseRecorder {
	var requestBody *strings.Reader
	if method == http.MethodPost {
		requestBody = strings.NewReader(`{"instance_id":"inst-1","args":["hostname"]}`)
	} else {
		requestBody = strings.NewReader("")
	}
	request := httptest.NewRequest(method, requestPath, requestBody)
	request.Header.Set("Authorization", "Bearer user-secret-token")
	principalContext := security.WithPrincipalStruct(request.Context(), security.Principal{Kind: security.PrincipalKindUser, Name: principalName})
	principalContext = security.WithPrincipal(principalContext, "user:"+principalName)
	recorder := httptest.NewRecorder()
	fixture.apiServer.Handler().ServeHTTP(recorder, request.WithContext(principalContext))
	return recorder
}

func TestNodeDebugProxyForwardsWithServerIdentityAndStripsCredentials(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.GrantRole("user:admin", "cluster-admin")

	recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/worker-1/logs?instance=inst-1&tail=5")
	if recorder.Code != http.StatusOK {
		t.Fatalf("proxied request: got HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	observed := fixture.lastObserved
	if observed.path != "/debug/logs" || observed.rawQuery != "instance=inst-1&tail=5" {
		t.Fatalf("agent saw %q?%q, want /debug/logs?instance=inst-1&tail=5", observed.path, observed.rawQuery)
	}
	if observed.authorization != "" {
		t.Fatalf("caller's Authorization header must not reach the agent, got %q", observed.authorization)
	}
	if observed.clientCN != "ccattler-server" {
		t.Fatalf("agent must see the API server certificate, got CN %q", observed.clientCN)
	}
}

func TestNodeDebugProxyChecksCapabilityPerEndpoint(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.Grant("user:viewer", security.CapabilityNodeRead, security.ScopeCluster)

	if recorder := fixture.serveAs("viewer", http.MethodGet, "/api/node/debug/worker-1/stats"); recorder.Code != http.StatusOK {
		t.Fatalf("node.read on stats: got HTTP %d, want 200", recorder.Code)
	}
	for _, deniedEndpoint := range []string{"exec", "images/pull"} {
		if recorder := fixture.serveAs("viewer", http.MethodPost, "/api/node/debug/worker-1/"+deniedEndpoint); recorder.Code != http.StatusForbidden {
			t.Fatalf("node.read on %s: got HTTP %d, want 403", deniedEndpoint, recorder.Code)
		}
	}
	if recorder := fixture.serveAs("viewer", http.MethodGet, "/api/node/debug/worker-1/logs?instance=x"); recorder.Code != http.StatusForbidden {
		t.Fatalf("node.read on logs: got HTTP %d, want 403 (logs need workload.read)", recorder.Code)
	}

	fixture.apiAuthorizer.Grant("user:operator", security.CapabilityWorkloadExec, security.ScopeCluster)
	if recorder := fixture.serveAs("operator", http.MethodPost, "/api/node/debug/worker-1/exec"); recorder.Code != http.StatusOK {
		t.Fatalf("workload.exec on exec: got HTTP %d, want 200", recorder.Code)
	}
}

func TestNodeDebugProxyRejectsAgentWithWrongNodeIdentity(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.GrantRole("user:admin", "cluster-admin")
	fixture.publishDebugAddress(t, "worker-2", fixture.agentURL)

	recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/worker-2/containers")
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("agent certificate for worker-1 used as worker-2: got HTTP %d, want 502", recorder.Code)
	}
	if fixture.agentCallCount != 0 {
		t.Fatal("request must not reach an agent whose certificate names another node")
	}
}

func TestNodeDebugProxyRejectsUnknownEndpointsAndMethods(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.GrantRole("user:admin", "cluster-admin")

	for _, requestPath := range []string{"/api/node/debug/worker-1/pprof", "/api/node/debug/worker-1", "/api/node/debug/worker-1/images/list"} {
		if recorder := fixture.serveAs("admin", http.MethodGet, requestPath); recorder.Code != http.StatusNotFound {
			t.Errorf("%s: got HTTP %d, want 404", requestPath, recorder.Code)
		}
	}
	// ServeMux cleans dot segments with a redirect before the handler runs.
	if recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/worker-1/../../secrets"); recorder.Code == http.StatusOK {
		t.Error("dot-segment path must not be proxied")
	}
	if recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/worker-1/exec"); recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET exec: got HTTP %d, want 405", recorder.Code)
	}
	if fixture.agentCallCount != 0 {
		t.Fatalf("rejected requests reached the agent %d times", fixture.agentCallCount)
	}
}

func TestNodeDebugProxyRefusesPlainHTTPOnNonLoopback(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.GrantRole("user:admin", "cluster-admin")
	fixture.publishDebugAddress(t, "worker-3", "http://10.0.0.5:9771")
	fixture.publishDebugAddress(t, "worker-4", "10.0.0.6:9771")

	for _, nodeID := range []string{"worker-3", "worker-4"} {
		if recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/"+nodeID+"/containers"); recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s plain non-loopback address: got HTTP %d, want 503", nodeID, recorder.Code)
		}
	}
}

func TestNodeDebugProxyReportsMissingDebugAddress(t *testing.T) {
	fixture := newDebugProxyFixture(t, "worker-1")
	fixture.apiAuthorizer.GrantRole("user:admin", "cluster-admin")

	recorder := fixture.serveAs("admin", http.MethodGet, "/api/node/debug/worker-9/containers")
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "--debug-listen") {
		t.Fatalf("missing debug address: got HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
}

// parsedTestCertificate issues a certificate for commonName with the given
// TTL from certificateAuthority and returns it parsed.
func parsedTestCertificate(t *testing.T, certificateAuthority *security.CertificateAuthority, commonName string, certificateTTL time.Duration) *x509.Certificate {
	t.Helper()
	issuedCertificate, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{CommonName: commonName, TTL: certificateTTL})
	if issueError != nil {
		t.Fatalf("issue certificate: %v", issueError)
	}
	certificateBlock, _ := pem.Decode(issuedCertificate.CertificatePEM)
	parsedCertificate, parseError := x509.ParseCertificate(certificateBlock.Bytes)
	if parseError != nil {
		t.Fatalf("parse certificate: %v", parseError)
	}
	return parsedCertificate
}

// enrolledNodeCertificate signs a CSR the way enrollment does (Flow 11), giving
// a client-auth-only certificate with CN = nodeID.
func enrolledNodeCertificate(t *testing.T, certificateAuthority *security.CertificateAuthority, nodeID string) *x509.Certificate {
	t.Helper()
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, nodeKey)
	parsedCSR, _ := x509.ParseCertificateRequest(csrDER)
	signResponse, signError := certificateAuthority.SignCSR(security.SignCSRRequest{CSR: parsedCSR, TTL: time.Hour, CommonName: nodeID, DNSNames: []string{nodeID}})
	if signError != nil {
		t.Fatalf("sign CSR: %v", signError)
	}
	certificateBlock, _ := pem.Decode(signResponse.CertificatePEM)
	parsedCertificate, _ := x509.ParseCertificate(certificateBlock.Bytes)
	return parsedCertificate
}

// TestVerifyNodeCertificate covers the custom check that replaces Go's default
// verification for the agent debug proxy (code scanning alert #21): chain to
// the cluster CA, validity period, and CN pinned to the node ID.
func TestVerifyNodeCertificate(t *testing.T) {
	clusterAuthority, _ := security.NewCertificateAuthority(24 * time.Hour)
	foreignAuthority, _ := security.NewCertificateAuthority(24 * time.Hour)
	clusterRoots := x509.NewCertPool()
	clusterRoots.AppendCertsFromPEM(clusterAuthority.CACertificatePEM())

	enrolledCertificate := enrolledNodeCertificate(t, clusterAuthority, "worker-1")
	if len(enrolledCertificate.ExtKeyUsage) != 1 || enrolledCertificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("test setup: enrolled certificate must be client-auth only, got %v", enrolledCertificate.ExtKeyUsage)
	}
	if verifyError := verifyNodeCertificate([]*x509.Certificate{enrolledCertificate}, clusterRoots, "worker-1"); verifyError != nil {
		t.Fatalf("an enrolled client-auth-only certificate for the node must be accepted: %v", verifyError)
	}

	rejectionCases := map[string][]*x509.Certificate{
		"no certificate":       nil,
		"foreign CA":           {parsedTestCertificate(t, foreignAuthority, "worker-1", time.Hour)},
		"expired":              {parsedTestCertificate(t, clusterAuthority, "worker-1", -time.Hour)},
		"another node's CN":    {enrolledNodeCertificate(t, clusterAuthority, "worker-2")},
		"server identity's CN": {parsedTestCertificate(t, clusterAuthority, "ccattler-server", time.Hour)},
	}
	for caseName, peerCertificates := range rejectionCases {
		if verifyNodeCertificate(peerCertificates, clusterRoots, "worker-1") == nil {
			t.Errorf("%s: certificate must be rejected", caseName)
		}
	}
}
