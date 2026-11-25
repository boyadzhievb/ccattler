// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build etcd_integration

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
)

// etcdTLSTestStartTimeout bounds how long a test etcd may take to serve TLS.
const etcdTLSTestStartTimeout = 30 * time.Second

// etcdTLSTestRequestTimeout bounds one etcd request in the TLS tests.
const etcdTLSTestRequestTimeout = 3 * time.Second

// etcdTLSTestPollInterval is the wait between readiness probes of a test etcd.
const etcdTLSTestPollInterval = 200 * time.Millisecond

// etcdTLSServerFiles are the files a test etcd serves and trusts.
type etcdTLSServerFiles struct {
	servingCertificatePath string // ETCD_CERT_FILE
	servingKeyPath         string // ETCD_KEY_FILE
	trustedCAPath          string // ETCD_TRUSTED_CA_FILE: the only CA etcd accepts client certificates from
}

// freeLoopbackPort returns a TCP port on 127.0.0.1 that was free a moment ago.
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatal(listenError)
	}
	defer func() { _ = listener.Close() }()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

// startTLSEtcd starts a real etcd (ETCD_BIN, else etcd on PATH) with client
// certificate authentication as setup.sh configures it, and returns its client
// URL. The etcd_integration tag requires the binary: a missing one fails the test.
func startTLSEtcd(t *testing.T, serverFiles etcdTLSServerFiles) string {
	t.Helper()
	etcdBinary := os.Getenv("ETCD_BIN")
	if etcdBinary == "" {
		foundBinary, lookupError := exec.LookPath("etcd")
		if lookupError != nil {
			t.Fatal("etcd_integration needs an etcd binary: set ETCD_BIN or put etcd on PATH")
		}
		etcdBinary = foundBinary
	}
	clientURL := "https://127.0.0.1:" + freeLoopbackPort(t)
	peerURL := "http://127.0.0.1:" + freeLoopbackPort(t)
	etcdCommand := exec.Command(etcdBinary, //nolint:gosec // test etcd binary chosen by the operator
		"--name", "pki-test", "--data-dir", t.TempDir(), "--log-level", "error",
		"--listen-client-urls", clientURL, "--advertise-client-urls", clientURL,
		"--listen-peer-urls", peerURL, "--initial-advertise-peer-urls", peerURL,
		"--initial-cluster", "pki-test="+peerURL,
		"--cert-file", serverFiles.servingCertificatePath, "--key-file", serverFiles.servingKeyPath,
		"--trusted-ca-file", serverFiles.trustedCAPath, "--client-cert-auth")
	etcdCommand.Stderr = os.Stderr
	if startError := etcdCommand.Start(); startError != nil {
		t.Fatalf("start etcd: %v", startError)
	}
	t.Cleanup(func() {
		_ = etcdCommand.Process.Kill()
		_ = etcdCommand.Wait()
	})
	return clientURL
}

// etcdAcceptsClientCertificate reports whether etcd at clientURL answers a Get
// for a client presenting the certificate (sent even when etcd's CA list does
// not name its issuer), verifying etcd against serverCAPEM.
func etcdAcceptsClientCertificate(t *testing.T, clientURL string, serverCAPEM []byte, clientCertificate tls.Certificate) error {
	t.Helper()
	serverRoots := x509.NewCertPool()
	if !serverRoots.AppendCertsFromPEM(serverCAPEM) {
		t.Fatal("no CA certificate for etcd's serving certificate")
	}
	etcdClient, clientError := clientv3.New(clientv3.Config{
		Endpoints: []string{clientURL}, DialTimeout: etcdTLSTestRequestTimeout,
		TLS: &tls.Config{RootCAs: serverRoots, MinVersion: tls.VersionTLS12,
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &clientCertificate, nil }},
	})
	if clientError != nil {
		return clientError
	}
	defer func() { _ = etcdClient.Close() }()
	requestContext, cancel := context.WithTimeout(context.Background(), etcdTLSTestRequestTimeout)
	defer cancel()
	_, getError := etcdClient.Get(requestContext, "pki-test-probe")
	return getError
}

// waitForEtcdToAccept polls until etcd answers the accepted certificate, so a
// refusal later is not a slow start. Fails the test at the start timeout.
func waitForEtcdToAccept(t *testing.T, clientURL string, serverCAPEM []byte, acceptedCertificate tls.Certificate) {
	t.Helper()
	deadline := time.Now().Add(etcdTLSTestStartTimeout)
	pollTicker := time.NewTicker(etcdTLSTestPollInterval)
	defer pollTicker.Stop()
	for {
		lastError := etcdAcceptsClientCertificate(t, clientURL, serverCAPEM, acceptedCertificate)
		if lastError == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("etcd at %s did not accept the etcd client certificate: %v", clientURL, lastError)
		}
		<-pollTicker.C
	}
}

// loadInitKeyPair loads a certificate cca init wrote, with its -key.pem.
func loadInitKeyPair(t *testing.T, outputDirectory string, certificateFileName string) tls.Certificate {
	t.Helper()
	keyPair, pairError := tls.LoadX509KeyPair(filepath.Join(outputDirectory, certificateFileName),
		filepath.Join(outputDirectory, privateKeyFileName(certificateFileName)))
	if pairError != nil {
		t.Fatalf("load %s: %v", certificateFileName, pairError)
	}
	return keyPair
}

// TestEtcdRefusesClusterCACertificates: etcd set up as setup.sh does (trusting
// etcd-access-ca.pem only) refuses the API identities from the cluster identity
// CA and accepts the server's and the node's etcd client certificates (D1, D5).
func TestEtcdRefusesClusterCACertificates(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	initFilePath := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	clientURL := startTLSEtcd(t, etcdTLSServerFiles{initFilePath(etcdServingCertificateFileName),
		initFilePath(privateKeyFileName(etcdServingCertificateFileName)), initFilePath(etcdAccessCAFileName)})
	etcdCAPEM, _ := os.ReadFile(initFilePath(etcdAccessCAFileName))
	waitForEtcdToAccept(t, clientURL, etcdCAPEM, loadInitKeyPair(t, outputDirectory, nodeEtcdClientCertificateFileName))

	for _, acceptedFile := range []string{nodeEtcdClientCertificateFileName, ccaServerEtcdClientCertificateFileName} {
		if acceptError := etcdAcceptsClientCertificate(t, clientURL, etcdCAPEM, loadInitKeyPair(t, outputDirectory, acceptedFile)); acceptError != nil {
			t.Errorf("etcd refused %s: %v", acceptedFile, acceptError)
		}
	}
	for _, refusedFile := range []string{nodeIdentityForAPIFileName, apiServerIdentityCertificateFileName} {
		if etcdAcceptsClientCertificate(t, clientURL, etcdCAPEM, loadInitKeyPair(t, outputDirectory, refusedFile)) == nil {
			t.Errorf("etcd accepted %s, a cluster identity CA certificate", refusedFile)
		}
	}
}

// TestGateCertificateIsRefusedByEtcd: a gate certificate for host
// ccattler-server is refused by etcd on a new cluster (gate HTTPS CA, which etcd
// does not trust) and on an old one, whose etcd trusts the single cluster CA
// that also signs gates: there the missing ClientAuth usage refuses it (D3).
// On the new cluster a ClientAuth certificate from the gate CA is refused at
// TLS too, which proves the CA separation, not only the EKU (SA4-L1).
func TestGateCertificateIsRefusedByEtcd(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	initFilePath := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	gateHTTPSCA, gateCAError := security.LoadCertificateAuthorityFromFiles(initFilePath(gateHTTPSCAFileName),
		initFilePath(privateKeyFileName(gateHTTPSCAFileName)))
	if gateCAError != nil {
		t.Fatal(gateCAError)
	}
	legacyClusterCA, legacyCAError := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, clusterPKITestTTL)
	if legacyCAError != nil {
		t.Fatal(legacyCAError)
	}
	legacyDirectory := t.TempDir()
	legacyEtcdServing, issueError := legacyClusterCA.IssueCertificate(security.IssueCertificateRequest{
		CommonName: "etcd-ctrl", Usage: security.UsageServer, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, TTL: clusterPKITestTTL})
	legacyNode, nodeIssueError := legacyClusterCA.IssueCertificate(security.IssueCertificateRequest{
		CommonName: "ctrl", Usage: security.UsageClient, TTL: clusterPKITestTTL})
	if issueError != nil || nodeIssueError != nil {
		t.Fatal(issueError, nodeIssueError)
	}
	legacyNodeKeyPair, _ := tls.X509KeyPair(legacyNode.CertificatePEM, legacyNode.PrivateKeyPEM)
	envelopeCodec := newClusterPKITestEnvelopeCodec(t)

	gateCAClientKeyPair := issueGateCAClientKeyPair(t, gateHTTPSCA)
	clusterSetups := map[string]struct {
		serverFiles    etcdTLSServerFiles             // what etcd serves and trusts
		serverCAPEM    []byte                         // CA of etcd's serving certificate
		acceptedClient tls.Certificate                // a certificate etcd accepts, to know it is up
		gateIssuer     *security.CertificateAuthority // the CA RouteTLSController issues gates from
		gateCAClient   *tls.Certificate               // a ClientAuth certificate from the gate CA; nil where it is etcd's CA
	}{
		"new cluster, gate HTTPS CA": {etcdTLSServerFiles{initFilePath(etcdServingCertificateFileName),
			initFilePath(privateKeyFileName(etcdServingCertificateFileName)), initFilePath(etcdAccessCAFileName)},
			mustReadFile(t, initFilePath(etcdAccessCAFileName)), loadInitKeyPair(t, outputDirectory, nodeEtcdClientCertificateFileName), gateHTTPSCA,
			&gateCAClientKeyPair},
		"old cluster, one CA": {etcdTLSServerFiles{
			writeClusterPKITestFile(t, legacyDirectory, "etcd.pem", legacyEtcdServing.CertificatePEM),
			writeClusterPKITestFile(t, legacyDirectory, "etcd-key.pem", legacyEtcdServing.PrivateKeyPEM),
			writeClusterPKITestFile(t, legacyDirectory, "ca.pem", legacyClusterCA.CACertificatePEM())},
			legacyClusterCA.CACertificatePEM(), legacyNodeKeyPair, legacyClusterCA, nil},
	}
	for _, setupName := range slices.Sorted(maps.Keys(clusterSetups)) {
		clusterSetup := clusterSetups[setupName]
		clientURL := startTLSEtcd(t, clusterSetup.serverFiles)
		waitForEtcdToAccept(t, clientURL, clusterSetup.serverCAPEM, clusterSetup.acceptedClient)
		gateKeyPair := issueGateKeyPairThroughController(t,
			controllers.NewRouteTLSController(clusterSetup.gateIssuer, envelopeCodec), envelopeCodec, security.ServerCertificateCommonName)
		if etcdAcceptsClientCertificate(t, clientURL, clusterSetup.serverCAPEM, gateKeyPair) == nil {
			t.Errorf("%s: etcd accepted a gate certificate for host %s", setupName, security.ServerCertificateCommonName)
		}
		if clusterSetup.gateCAClient == nil {
			continue
		}
		// SA4-L1: CA separation itself, not only the gate certificate's missing ClientAuth.
		if refused, observed := tlsServerRefusesClientCertificate(t, strings.TrimPrefix(clientURL, "https://"),
			clusterSetup.serverCAPEM, *clusterSetup.gateCAClient); !refused {
			t.Errorf("%s: etcd did not refuse a ClientAuth certificate from the gate HTTPS CA at TLS: %s", setupName, observed)
		}
	}
}

// countEtcdKeys returns how many keys etcd at clientURL holds, read with the
// cca-server etcd client certificate.
func countEtcdKeys(t *testing.T, clientURL string, serverCAPEM []byte, clientCertificate tls.Certificate) int64 {
	t.Helper()
	serverRoots := x509.NewCertPool()
	serverRoots.AppendCertsFromPEM(serverCAPEM)
	etcdClient, clientError := clientv3.New(clientv3.Config{Endpoints: []string{clientURL}, DialTimeout: etcdTLSTestRequestTimeout,
		TLS: &tls.Config{RootCAs: serverRoots, Certificates: []tls.Certificate{clientCertificate}, MinVersion: tls.VersionTLS12}})
	if clientError != nil {
		t.Fatal(clientError)
	}
	defer func() { _ = etcdClient.Close() }()
	requestContext, cancel := context.WithTimeout(context.Background(), etcdTLSTestRequestTimeout)
	defer cancel()
	countResponse, getError := etcdClient.Get(requestContext, "\x00", clientv3.WithFromKey(), clientv3.WithCountOnly())
	if getError != nil {
		t.Fatal(getError)
	}
	return countResponse.Count
}

// TestInvalidNodeCertificateLeavesEtcdUntouched (stop-time check): cca server,
// started as setup.sh starts it against a real etcd but with a foreign
// --control-plane-node-cert, exits 1 before it writes anything: no local token,
// grants, keyring or leader lease reach etcd.
func TestInvalidNodeCertificateLeavesEtcdUntouched(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	initFilePath := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	clientURL := startTLSEtcd(t, etcdTLSServerFiles{initFilePath(etcdServingCertificateFileName),
		initFilePath(privateKeyFileName(etcdServingCertificateFileName)), initFilePath(etcdAccessCAFileName)})
	etcdCAPEM := mustReadFile(t, initFilePath(etcdAccessCAFileName))
	serverEtcdClient := loadInitKeyPair(t, outputDirectory, ccaServerEtcdClientCertificateFileName)
	waitForEtcdToAccept(t, clientURL, etcdCAPEM, serverEtcdClient)
	keysBefore := countEtcdKeys(t, clientURL, etcdCAPEM, serverEtcdClient)

	foreignCertificatePath := foreignNodeCertificateFile(t)
	serverArgs := append([]string{"server"}, serverArgsFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)...)
	serverArgs = append(serverArgs, "--endpoints", clientURL, "--listen", "127.0.0.1:0", "--dns-listen", "127.0.0.1:0",
		"--secret-root-key", initFilePath("secret-root.key"), "--control-plane-node-cert", foreignCertificatePath)
	_, stderrContent, exitCode := runCCABinaryFromPackageMain(t, serverArgs...)
	if exitCode != 1 || !strings.Contains(stderrContent, foreignCertificatePath) {
		t.Fatalf("cca server with a foreign --control-plane-node-cert: exit %d, stderr %q; want exit 1 naming the path", exitCode, stderrContent)
	}
	if keysAfter := countEtcdKeys(t, clientURL, etcdCAPEM, serverEtcdClient); keysAfter != keysBefore {
		t.Errorf("etcd held %d keys before and %d after the refused start; want it untouched", keysBefore, keysAfter)
	}
}
