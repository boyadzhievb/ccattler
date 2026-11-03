// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// issueTestKeyPair issues a CA-signed certificate for commonName and returns
// it as a tls.Certificate.
func issueTestKeyPair(testContext *testing.T, certificateAuthority *security.CertificateAuthority, commonName string) tls.Certificate {
	testContext.Helper()
	issuedCertificate, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{
		CommonName:  commonName,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		TTL:         time.Hour,
	})
	if issueError != nil {
		testContext.Fatalf("issue certificate for %s: %v", commonName, issueError)
	}
	keyPair, keyPairError := tls.X509KeyPair(issuedCertificate.CertificatePEM, issuedCertificate.PrivateKeyPEM)
	if keyPairError != nil {
		testContext.Fatalf("load key pair for %s: %v", commonName, keyPairError)
	}
	return keyPair
}

// startMutualTLSDebugServer starts a debug server with mTLS on loopback whose
// access checker admits principals holding cluster-admin in a memory store
// seeded with a grant for "ccattler-server". Returns its address and the CA.
func startMutualTLSDebugServer(testContext *testing.T) (string, *security.CertificateAuthority) {
	testContext.Helper()
	certificateAuthority, caError := security.NewCertificateAuthority(24 * time.Hour)
	if caError != nil {
		testContext.Fatalf("create CA: %v", caError)
	}
	clusterRoots := x509.NewCertPool()
	clusterRoots.AppendCertsFromPEM(certificateAuthority.CACertificatePEM())

	factStore := store.NewMemoryStore()
	testContext.Cleanup(func() { factStore.Close() })
	if _, putError := factStore.Put(context.Background(), types.KeyAuthGrant("node", "ccattler-server", "cluster-admin"), []byte("true")); putError != nil {
		testContext.Fatalf("seed grant: %v", putError)
	}

	listenAddress := fmt.Sprintf("127.0.0.1:%d", findAvailablePort(testContext))
	debugServer := NewDebugServer("worker-1", listenAddress, runtime.NewSimulatorRuntime(), "")
	debugServer.SetMutualTLS(&tls.Config{
		Certificates: []tls.Certificate{issueTestKeyPair(testContext, certificateAuthority, "worker-1")},
		ClientCAs:    clusterRoots,
		MinVersion:   tls.VersionTLS13,
	}, NewClusterAdminGrantChecker(factStore))

	serverCtx, serverCancel := context.WithCancel(context.Background())
	testContext.Cleanup(serverCancel)
	go func() { _ = debugServer.Start(serverCtx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, dialError := net.Dial("tcp", listenAddress); dialError == nil {
			conn.Close()
			return listenAddress, certificateAuthority
		}
		time.Sleep(10 * time.Millisecond)
	}
	testContext.Fatal("debug server did not start within 2s")
	return "", nil
}

// getWithClientCertificate performs GET /debug/containers presenting the given
// client certificates (none when empty).
func getWithClientCertificate(listenAddress string, clientCertificates []tls.Certificate) (*http.Response, error) {
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates:       clientCertificates,
		InsecureSkipVerify: true, //nolint:gosec // test client: the server certificate is not under test
		MinVersion:         tls.VersionTLS13,
	}}}
	return httpClient.Get("https://" + listenAddress + "/debug/containers")
}

func TestDebugServerRefusesPlainNonLoopbackListen(testContext *testing.T) {
	debugServer := NewDebugServer("worker-1", "0.0.0.0:0", runtime.NewSimulatorRuntime(), "")
	if startError := debugServer.Start(context.Background()); startError == nil {
		testContext.Fatal("plain debug API on a non-loopback address must be refused")
	}
}

func TestDebugServerMutualTLSRequiresClientCertificate(testContext *testing.T) {
	listenAddress, _ := startMutualTLSDebugServer(testContext)

	httpResponse, requestError := getWithClientCertificate(listenAddress, nil)
	if requestError == nil {
		_ = httpResponse.Body.Close()
		testContext.Fatalf("request without a client certificate must fail, got HTTP %d", httpResponse.StatusCode)
	}
}

func TestDebugServerMutualTLSRejectsNonAdminPrincipal(testContext *testing.T) {
	listenAddress, certificateAuthority := startMutualTLSDebugServer(testContext)

	httpResponse, requestError := getWithClientCertificate(listenAddress, []tls.Certificate{issueTestKeyPair(testContext, certificateAuthority, "worker-2")})
	if requestError != nil {
		testContext.Fatalf("request: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if httpResponse.StatusCode != http.StatusForbidden {
		testContext.Fatalf("non-admin node certificate: got HTTP %d, want 403", httpResponse.StatusCode)
	}
}

func TestDebugServerMutualTLSAdmitsClusterAdminPrincipal(testContext *testing.T) {
	listenAddress, certificateAuthority := startMutualTLSDebugServer(testContext)

	httpResponse, requestError := getWithClientCertificate(listenAddress, []tls.Certificate{issueTestKeyPair(testContext, certificateAuthority, "ccattler-server")})
	if requestError != nil {
		testContext.Fatalf("request: %v", requestError)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if httpResponse.StatusCode != http.StatusOK {
		testContext.Fatalf("cluster-admin certificate: got HTTP %d, want 200", httpResponse.StatusCode)
	}
}

func TestDebugServerMutualTLSRejectsForeignCA(testContext *testing.T) {
	listenAddress, _ := startMutualTLSDebugServer(testContext)
	foreignAuthority, _ := security.NewCertificateAuthority(24 * time.Hour)

	httpResponse, requestError := getWithClientCertificate(listenAddress, []tls.Certificate{issueTestKeyPair(testContext, foreignAuthority, "ccattler-server")})
	if requestError == nil {
		_ = httpResponse.Body.Close()
		testContext.Fatalf("certificate from a foreign CA must be rejected, got HTTP %d", httpResponse.StatusCode)
	}
}
