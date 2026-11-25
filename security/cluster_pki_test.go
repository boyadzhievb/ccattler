// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net/http"
	"slices"
	"testing"
	"time"
)

// TestMTLSPrincipalIgnoresSubjectUnit: the Subject OU is descriptive
// (D1a); the principal comes from the CommonName alone, whatever the OU claims.
func TestMTLSPrincipalIgnoresSubjectUnit(t *testing.T) {
	for _, organizationalUnit := range [][]string{nil, {"API server identity"}, {"cluster-admin"}, {"node etcd client"}} {
		request := &http.Request{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			Subject:  pkix.Name{Organization: []string{"CCattler"}, OrganizationalUnit: organizationalUnit, CommonName: "worker-1"},
			NotAfter: time.Now().Add(time.Hour),
		}}}}
		authenticationResult, authenticationError := NewMTLSAuthenticator().Authenticate(request)
		if authenticationError != nil || authenticationResult == nil || authenticationResult.Principal != "node:worker-1" {
			t.Errorf("OU %v: result %+v, error %v; want principal node:worker-1", organizationalUnit, authenticationResult, authenticationError)
		}
	}
}

// TestEnrollmentIssuesEtcdClientCertificate: with an etcd access CA, enrollment
// signs the node's etcd CSR as an etcd client (ClientAuth only, CN
// etcd-client-<node>) next to its API identity from the cluster CA (D4).
func TestEnrollmentIssuesEtcdClientCertificate(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)
	etcdAccessCA, caError := NewCertificateAuthority(EtcdAccessCertificateAuthority, 24*time.Hour)
	if caError != nil {
		t.Fatal(caError)
	}
	enrollmentService.SetEtcdClientCertificateAuthority(etcdAccessCA)
	joinToken, tokenError := enrollmentService.GenerateJoinToken(context.Background(), "worker-1", 5*time.Minute)
	if tokenError != nil {
		t.Fatal(tokenError)
	}
	enrollmentResponse, enrollError := enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
		Token: joinToken.Token, NodeID: "worker-1",
		CSRPEM:     generateTestCSR(t, "worker-1", nil),
		EtcdCSRPEM: generateTestCSR(t, "chosen-by-node", nil),
	})
	if enrollError != nil {
		t.Fatal(enrollError)
	}
	etcdBlock, _ := pem.Decode(enrollmentResponse.EtcdCertificatePEM)
	if etcdBlock == nil {
		t.Fatal("enrollment returned no etcd client certificate")
	}
	etcdCertificate, parseError := x509.ParseCertificate(etcdBlock.Bytes)
	if parseError != nil {
		t.Fatal(parseError)
	}
	if etcdCertificate.Subject.CommonName != "etcd-client-worker-1" {
		t.Errorf("etcd client CN = %q, want etcd-client-worker-1", etcdCertificate.Subject.CommonName)
	}
	if !slices.Equal(etcdCertificate.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("etcd client ExtKeyUsage = %v, want [ClientAuth]", etcdCertificate.ExtKeyUsage)
	}
	etcdRoots := x509.NewCertPool()
	etcdRoots.AppendCertsFromPEM(enrollmentResponse.EtcdCACertPEM)
	if _, verifyError := etcdCertificate.Verify(x509.VerifyOptions{Roots: etcdRoots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); verifyError != nil {
		t.Errorf("etcd client certificate does not verify against the returned etcd CA: %v", verifyError)
	}
	if !slices.Equal(enrollmentResponse.EtcdCACertPEM, etcdAccessCA.CACertificatePEM()) {
		t.Error("enrollment returned another CA than the etcd access CA")
	}
	nodeCertificate := parseEnrolledCertificate(t, enrollmentResponse)
	if nodeCertificate.CheckSignatureFrom(etcdAccessCA.caCertificate) == nil {
		t.Error("the node's API identity is signed by the etcd access CA")
	}
}

// TestEnrollmentWithoutEtcdCAReturnsNoEtcdCertificate: a server without an etcd
// access CA (a cluster from before M280) answers as before (D4).
func TestEnrollmentWithoutEtcdCAReturnsNoEtcdCertificate(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)
	joinToken, _ := enrollmentService.GenerateJoinToken(context.Background(), "worker-1", 5*time.Minute)
	enrollmentResponse, enrollError := enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
		Token: joinToken.Token, NodeID: "worker-1",
		CSRPEM: generateTestCSR(t, "worker-1", nil), EtcdCSRPEM: generateTestCSR(t, "worker-1", nil),
	})
	if enrollError != nil {
		t.Fatal(enrollError)
	}
	if len(enrollmentResponse.EtcdCertificatePEM) != 0 || len(enrollmentResponse.EtcdCACertPEM) != 0 {
		t.Error("a server without an etcd access CA returned etcd material")
	}
}

// TestEnrollmentRefusesEtcdCSRReusingNodeKey: the etcd client certificate needs
// its own key; a CSR with the node identity's key is refused and the token stays unused (D1a).
func TestEnrollmentRefusesEtcdCSRReusingNodeKey(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)
	etcdAccessCA, _ := NewCertificateAuthority(EtcdAccessCertificateAuthority, time.Hour)
	enrollmentService.SetEtcdClientCertificateAuthority(etcdAccessCA)
	joinToken, _ := enrollmentService.GenerateJoinToken(context.Background(), "worker-1", 5*time.Minute)
	sharedKeyCSR := generateTestCSR(t, "worker-1", nil)
	_, enrollError := enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
		Token: joinToken.Token, NodeID: "worker-1", CSRPEM: sharedKeyCSR, EtcdCSRPEM: sharedKeyCSR,
	})
	if enrollError == nil {
		t.Fatal("enrollment accepted an etcd CSR that reuses the node identity key")
	}
	if _, retryError := enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
		Token: joinToken.Token, NodeID: "worker-1", CSRPEM: generateTestCSR(t, "worker-1", nil),
		EtcdCSRPEM: generateTestCSR(t, "worker-1", nil),
	}); retryError != nil {
		t.Errorf("the refused enrollment consumed the token: %v", retryError)
	}
}

// TestIssueCertificateRequiresUsage: IssueCertificate has no default usage; the
// zero value and unknown values are refused, so every caller chooses (D2).
func TestIssueCertificateRequiresUsage(t *testing.T) {
	certificateAuthority, _ := NewCertificateAuthority(ClusterIdentityCertificateAuthority, time.Hour)
	for _, usage := range []CertificateUsage{0, UsageServerAndClient + 1} {
		if _, issueError := certificateAuthority.IssueCertificate(IssueCertificateRequest{CommonName: "x", Usage: usage, TTL: time.Hour}); issueError == nil {
			t.Errorf("usage %d: issued a certificate, want an error", usage)
		}
	}
	wantUsages := map[CertificateUsage][]x509.ExtKeyUsage{
		UsageServer:          {x509.ExtKeyUsageServerAuth},
		UsageClient:          {x509.ExtKeyUsageClientAuth},
		UsageServerAndClient: {x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	for usage, wantExtKeyUsage := range wantUsages {
		issued, issueError := certificateAuthority.IssueCertificate(IssueCertificateRequest{CommonName: "x", Usage: usage, TTL: time.Hour})
		if issueError != nil {
			t.Fatal(issueError)
		}
		certificateBlock, _ := pem.Decode(issued.CertificatePEM)
		issuedCertificate, _ := x509.ParseCertificate(certificateBlock.Bytes)
		if !slices.Equal(issuedCertificate.ExtKeyUsage, wantExtKeyUsage) {
			t.Errorf("usage %d: ExtKeyUsage %v, want %v", usage, issuedCertificate.ExtKeyUsage, wantExtKeyUsage)
		}
	}
}
