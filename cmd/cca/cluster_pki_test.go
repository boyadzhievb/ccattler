// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// clusterPKITestTTL is the lifetime of the certificates the cluster PKI tests issue.
const clusterPKITestTTL = time.Hour

// clusterPKITestRequestTimeout bounds one mTLS request in the cluster PKI tests.
const clusterPKITestRequestTimeout = 5 * time.Second

// clusterPKITestGateName is the gate the cluster PKI tests issue certificates for.
const clusterPKITestGateName = "web-public"

// newClusterPKITestEnvelopeCodec returns a keyring codec on a fresh memory store,
// as cca server builds one, to seal and open gate keys.
func newClusterPKITestEnvelopeCodec(t *testing.T) security.SecretEnvelopeCodec {
	t.Helper()
	rootKey, keyError := security.GenerateMasterKey()
	if keyError != nil {
		t.Fatal(keyError)
	}
	rootKeyProvider, providerError := security.NewLocalKeyProvider(rootKey)
	if providerError != nil {
		t.Fatal(providerError)
	}
	envelopeCodec, codecError := security.NewKeyringEnvelopeCodec(context.Background(), store.NewMemoryStore(), rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	return envelopeCodec
}

// issueGateKeyPairThroughController runs RouteTLSController for an auto gate on
// host and returns the certificate it stored with its opened sealed key.
func issueGateKeyPairThroughController(t *testing.T, routeTLSController *controllers.RouteTLSController,
	envelopeCodec security.SecretEnvelopeCodec, host string) tls.Certificate {
	t.Helper()
	changes, reconcileError := routeTLSController.Reconcile(context.Background(), []store.Fact{
		{Key: types.KeyDesiredRouteHost(clusterPKITestGateName), Value: []byte(host)},
		{Key: types.KeyDesiredRouteTLSMode(clusterPKITestGateName), Value: []byte(types.RouteTLSModeAuto)},
	})
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	sealedKeyStoreKey := security.SecretStorePrefix + types.SecretNameRouteTLSKey(clusterPKITestGateName)
	var certificatePEM, sealedKey []byte
	for _, change := range changes {
		switch change.Key {
		case types.KeyDerivedRouteTLSCert(clusterPKITestGateName):
			certificatePEM = change.Value
		case sealedKeyStoreKey:
			sealedKey = change.Value
		}
	}
	envelopeBlob, decodeError := base64.StdEncoding.DecodeString(string(sealedKey))
	if decodeError != nil {
		t.Fatalf("sealed gate key is not base64: %v", decodeError)
	}
	privateKeyPEM, openError := envelopeCodec.OpenEnvelopeForKey(context.Background(), sealedKeyStoreKey, envelopeBlob)
	if openError != nil {
		t.Fatalf("open sealed gate key: %v", openError)
	}
	gateKeyPair, pairError := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if pairError != nil {
		t.Fatalf("gate key pair: %v", pairError)
	}
	return gateKeyPair
}

// startPrincipalEchoAPIServer serves mTLS with the production loadServerTLSConfig
// (enrollment on, as setup.sh runs cca server) and answers with the principal
// MTLSAuthenticator derives from the client certificate.
func startPrincipalEchoAPIServer(t *testing.T, serverCertPath, serverKeyPath, clusterCACertPath string) *httptest.Server {
	t.Helper()
	mtlsAuthenticator := security.NewMTLSAuthenticator()
	echoServer := httptest.NewUnstartedServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		authenticationResult, _ := mtlsAuthenticator.Authenticate(request)
		if authenticationResult == nil {
			http.Error(responseWriter, "no principal", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(responseWriter, authenticationResult.Principal) //nolint:gosec // test echo server, plain text
	}))
	echoServer.TLS = loadServerTLSConfig(serverCertPath, serverKeyPath, clusterCACertPath, true)
	echoServer.StartTLS()
	t.Cleanup(echoServer.Close)
	return echoServer
}

// requestPrincipalWithClientCertificate calls the echo server presenting the
// client certificate, also when the server's CA list does not name its issuer
// (Go's client would otherwise send none), and returns the principal or the error.
func requestPrincipalWithClientCertificate(t *testing.T, serverURL string, serverRootsPEM []byte,
	clientCertificate tls.Certificate) (string, error) {
	t.Helper()
	serverRoots := x509.NewCertPool()
	if !serverRoots.AppendCertsFromPEM(serverRootsPEM) {
		t.Fatal("no CA certificate in the server roots")
	}
	httpClient := &http.Client{Timeout: clusterPKITestRequestTimeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: serverRoots, MinVersion: tls.VersionTLS13,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &clientCertificate, nil },
	}}}
	httpResponse, requestError := httpClient.Get(serverURL)
	if requestError != nil {
		return "", requestError
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	return string(responseBody), nil
}

// writeClusterPKITestFile writes one PEM file into directory and returns its path.
func writeClusterPKITestFile(t *testing.T, directory string, fileName string, content []byte) string {
	t.Helper()
	filePath := filepath.Join(directory, fileName)
	if writeError := os.WriteFile(filePath, content, 0o600); writeError != nil {
		t.Fatal(writeError)
	}
	return filePath
}

// TestGateCertificateIsRefusedAsAPIClient is the M280 attack: a gate named like
// the server principal, on a cluster whose gates come from the cluster CA (every
// cluster before M280), must not authenticate to the API as node:ccattler-server.
func TestGateCertificateIsRefusedAsAPIClient(t *testing.T) {
	clusterCA, caError := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, clusterPKITestTTL)
	if caError != nil {
		t.Fatal(caError)
	}
	serverCertificate, issueError := clusterCA.IssueCertificate(security.IssueCertificateRequest{Usage: security.UsageServerAndClient,
		CommonName: "ctrl", IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, TTL: clusterPKITestTTL,
	})
	if issueError != nil {
		t.Fatal(issueError)
	}
	pkiDirectory := t.TempDir()
	echoServer := startPrincipalEchoAPIServer(t,
		writeClusterPKITestFile(t, pkiDirectory, "server.pem", serverCertificate.CertificatePEM),
		writeClusterPKITestFile(t, pkiDirectory, "server-key.pem", serverCertificate.PrivateKeyPEM),
		writeClusterPKITestFile(t, pkiDirectory, "ca.pem", clusterCA.CACertificatePEM()))

	envelopeCodec := newClusterPKITestEnvelopeCodec(t)
	gateKeyPair := issueGateKeyPairThroughController(t,
		controllers.NewRouteTLSController(clusterCA, envelopeCodec), envelopeCodec, "ccattler-server")

	principal, requestError := requestPrincipalWithClientCertificate(t, echoServer.URL, clusterCA.CACertificatePEM(), gateKeyPair)
	if requestError == nil {
		t.Fatalf("the API accepted a gate certificate as a client identity and yielded principal %q", principal)
	}
}

// clusterPKITestNodeID is the node ID the cluster PKI tests run cca init with.
const clusterPKITestNodeID = "ctrl"

// runClusterPKITestInit runs cca init for node ctrl on 127.0.0.1 into a new
// directory and returns it.
func runClusterPKITestInit(t *testing.T) string {
	t.Helper()
	outputDirectory := t.TempDir()
	executeInitCommand(initCommandConfig{
		outputDirectory:  outputDirectory,
		advertiseAddress: "127.0.0.1",
		nodeID:           clusterPKITestNodeID,
		caValidity:       defaultCACertificateValidityInit,
		certValidity:     defaultNodeCertificateValidityInit,
	})
	return outputDirectory
}

// readInitCertificate parses the certificate cca init wrote as fileName.
func readInitCertificate(t *testing.T, outputDirectory string, fileName string) *x509.Certificate {
	t.Helper()
	fileContent, readError := os.ReadFile(filepath.Join(outputDirectory, fileName)) //nolint:gosec // test temp directory
	if readError != nil {
		t.Fatalf("cca init did not write %s: %v", fileName, readError)
	}
	certificateBlock, _ := pem.Decode(fileContent)
	if certificateBlock == nil {
		t.Fatalf("%s holds no PEM block", fileName)
	}
	certificate, parseError := x509.ParseCertificate(certificateBlock.Bytes)
	if parseError != nil {
		t.Fatalf("parse %s: %v", fileName, parseError)
	}
	return certificate
}

// initLeafExpectation is what one leaf certificate cca init writes must be.
type initLeafExpectation struct {
	fileName     string             // certificate file in the cca init output
	issuerCAFile string             // CA file that must have signed it
	commonName   string             // expected Subject CN
	unit         string             // expected Subject OU (descriptive only)
	usages       []x509.ExtKeyUsage // exact ExtKeyUsage
}

// initLeafExpectations lists every leaf certificate of a new cluster (D1, D1a).
var initLeafExpectations = []initLeafExpectation{
	{"api-server-identity-certificate.pem", "cluster-identity-ca.pem", "ccattler-server", "API server identity",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}},
	{"node-identity-for-api.pem", "cluster-identity-ca.pem", clusterPKITestNodeID, "node identity for API",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
	{"etcd-serving-certificate.pem", "etcd-access-ca.pem", "etcd-" + clusterPKITestNodeID, "etcd serving certificate",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}},
	{"etcd-peer-certificate.pem", "etcd-access-ca.pem", "etcd-peer-" + clusterPKITestNodeID, "etcd peer certificate",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}},
	{"cca-server-etcd-client-certificate.pem", "etcd-access-ca.pem", "etcd-client-cca-server", "cca-server etcd client",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
	{"node-etcd-client-certificate.pem", "etcd-access-ca.pem", "etcd-client-" + clusterPKITestNodeID, "node etcd client",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
}

// initCAExpectations maps each CA file of a new cluster to its Subject OU.
var initCAExpectations = map[string]string{
	"cluster-identity-ca.pem": "cluster identity CA",
	"etcd-access-ca.pem":      "etcd access CA",
	"gate-https-ca.pem":       "gate HTTPS CA",
}

// TestInitWritesThreeCAs: cca init creates three CAs with their keys at 0600,
// no two sharing a key, and signs every leaf with the CA of its purpose,
// with exactly the usages that purpose needs (D1, D2).
func TestInitWritesThreeCAs(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	caPublicKeys := map[string]string{}
	for _, caFileName := range slices.Sorted(maps.Keys(initCAExpectations)) {
		caCertificate := readInitCertificate(t, outputDirectory, caFileName)
		if !caCertificate.IsCA {
			t.Errorf("%s is not a CA", caFileName)
		}
		publicKeyText := string(caCertificate.RawSubjectPublicKeyInfo)
		if otherCA, shared := caPublicKeys[publicKeyText]; shared {
			t.Errorf("%s and %s share a key", caFileName, otherCA)
		}
		caPublicKeys[publicKeyText] = caFileName
		keyFileName := strings.TrimSuffix(caFileName, ".pem") + "-key.pem"
		if keyInfo, statError := os.Stat(filepath.Join(outputDirectory, keyFileName)); statError != nil || keyInfo.Mode().Perm() != 0o600 {
			t.Errorf("%s: missing or not 0600 (%v)", keyFileName, statError)
		}
	}
	leafPublicKeys := map[string]string{}
	for _, expectation := range initLeafExpectations {
		leafCertificate := readInitCertificate(t, outputDirectory, expectation.fileName)
		issuerCertificate := readInitCertificate(t, outputDirectory, expectation.issuerCAFile)
		if signatureError := leafCertificate.CheckSignatureFrom(issuerCertificate); signatureError != nil {
			t.Errorf("%s is not signed by %s: %v", expectation.fileName, expectation.issuerCAFile, signatureError)
		}
		if !slices.Equal(leafCertificate.ExtKeyUsage, expectation.usages) {
			t.Errorf("%s ExtKeyUsage = %v, want %v", expectation.fileName, leafCertificate.ExtKeyUsage, expectation.usages)
		}
		if leafCertificate.Subject.CommonName != expectation.commonName {
			t.Errorf("%s CN = %q, want %q", expectation.fileName, leafCertificate.Subject.CommonName, expectation.commonName)
		}
		publicKeyText := string(leafCertificate.RawSubjectPublicKeyInfo)
		if otherLeaf, shared := leafPublicKeys[publicKeyText]; shared {
			t.Errorf("%s and %s share a key", expectation.fileName, otherLeaf)
		}
		leafPublicKeys[publicKeyText] = expectation.fileName
	}
}

// TestInitCertificatesHaveDescriptiveSubjects: every certificate cca init writes
// says what it is in O and OU; the CAs also in their CN (D1a).
func TestInitCertificatesHaveDescriptiveSubjects(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	expectedUnits := map[string]string{}
	for caFileName, caUnit := range initCAExpectations {
		expectedUnits[caFileName] = caUnit
		caCertificate := readInitCertificate(t, outputDirectory, caFileName)
		if wantCommonName := "CCattler " + caUnit; caCertificate.Subject.CommonName != wantCommonName {
			t.Errorf("%s CN = %q, want %q", caFileName, caCertificate.Subject.CommonName, wantCommonName)
		}
	}
	for _, expectation := range initLeafExpectations {
		expectedUnits[expectation.fileName] = expectation.unit
	}
	for _, fileName := range slices.Sorted(maps.Keys(expectedUnits)) {
		subject := readInitCertificate(t, outputDirectory, fileName).Subject
		if !slices.Equal(subject.Organization, []string{"CCattler"}) || !slices.Equal(subject.OrganizationalUnit, []string{expectedUnits[fileName]}) {
			t.Errorf("%s Subject O=%v OU=%v, want O=[CCattler] OU=[%s]", fileName, subject.Organization,
				subject.OrganizationalUnit, expectedUnits[fileName])
		}
	}
}

// TestInitFilesParseWithDescriptionLine: every PEM file cca init writes starts
// with a one-line "# CCattler ..." comment, and still loads as a key pair or CA
// in the parsers cca uses (D1a).
func TestInitFilesParseWithDescriptionLine(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	pemFilePaths, _ := filepath.Glob(filepath.Join(outputDirectory, "*.pem"))
	if len(pemFilePaths) == 0 {
		t.Fatal("cca init wrote no PEM files")
	}
	for _, pemFilePath := range pemFilePaths {
		fileContent, _ := os.ReadFile(pemFilePath) //nolint:gosec // test temp directory
		firstLine, _, _ := strings.Cut(string(fileContent), "\n")
		if !strings.HasPrefix(firstLine, "# CCattler ") {
			t.Errorf("%s: first line %q is not a '# CCattler ...' description", filepath.Base(pemFilePath), firstLine)
		}
	}
	for caFileName := range initCAExpectations {
		caKeyPath := filepath.Join(outputDirectory, strings.TrimSuffix(caFileName, ".pem")+"-key.pem")
		if _, loadError := security.LoadCertificateAuthorityFromFiles(filepath.Join(outputDirectory, caFileName), caKeyPath); loadError != nil {
			t.Errorf("%s does not load as a CA: %v", caFileName, loadError)
		}
	}
	for _, expectation := range initLeafExpectations {
		keyFileName := strings.TrimSuffix(expectation.fileName, ".pem") + "-key.pem"
		if _, pairError := tls.LoadX509KeyPair(filepath.Join(outputDirectory, expectation.fileName),
			filepath.Join(outputDirectory, keyFileName)); pairError != nil {
			t.Errorf("%s with %s does not load as a key pair: %v", expectation.fileName, keyFileName, pairError)
		}
	}
}

// setupScriptUnitText returns the body of the systemd unit setup.sh writes to unitPath.
func setupScriptUnitText(t *testing.T, setupScript string, unitPath string) string {
	t.Helper()
	_, unitStart, found := strings.Cut(setupScript, "cat > "+unitPath+" <<'UNIT'\n")
	if !found {
		t.Fatalf("setup.sh writes no %s", unitPath)
	}
	unitText, _, _ := strings.Cut(unitStart, "\nUNIT\n")
	return unitText
}

// TestInitRefusesPartialExistingCAFlags: an HA secondary given only some of the
// six existing-CA flags is refused, so it never mints CAs its peers do not trust (D1).
func TestInitRefusesPartialExistingCAFlags(t *testing.T) {
	// --ca/--ca-key alone is a single-CA cluster's secondary (CR-L7, TestInitSingleCASecondaryWritesOldLayout).
	for _, partialFlags := range [][]string{
		{"--ca", "/pki/ca.pem"},
		{"--ca", "/pki/ca.pem", "--ca-key", "/pki/ca-key.pem", "--gate-https-ca", "/pki/g.pem", "--gate-https-ca-key", "/pki/g-key.pem"},
		{"--etcd-access-ca", "/pki/e.pem", "--etcd-access-ca-key", "/pki/e-key.pem"},
		{"--ca", "/pki/ca.pem", "--ca-key", "/pki/ca-key.pem", "--etcd-access-ca", "/pki/e.pem",
			"--etcd-access-ca-key", "/pki/e-key.pem", "--gate-https-ca", "/pki/g.pem"},
	} {
		if _, parseError := parseInitCommandArgs(append([]string{"--node-id", "ctrl-2"}, partialFlags...)); parseError == nil ||
			!strings.Contains(parseError.Error(), "go together") {
			t.Errorf("cca init %v: error %v, want the all-or-none refusal", partialFlags, parseError)
		}
	}
}

// TestSetupScriptUsesEtcdCA: setup.sh makes etcd trust only the etcd access CA
// and gives cca server and cca agent each their own etcd client certificate
// and the API identities of D1a.
func TestSetupScriptUsesEtcdCA(t *testing.T) {
	setupScript := buildSetupScript("192.0.2.10", "node-1", "/tmp/cca-init", "10.100.0.0/16", "10.200.0.0/24", nil, threeCAClusterPKILayout)
	for _, wantLine := range []string{
		`Environment="ETCD_TRUSTED_CA_FILE=/etc/ccattler/pki/etcd-access-ca.pem"`,
		`Environment="ETCD_CERT_FILE=/etc/ccattler/pki/etcd-serving-certificate.pem"`,
		`Environment="ETCD_KEY_FILE=/etc/ccattler/pki/etcd-serving-certificate-key.pem"`,
	} {
		if !strings.Contains(setupScript, wantLine) {
			t.Errorf("setup.sh etcd drop-in lacks %s", wantLine)
		}
	}
	unitFlags := map[string][]string{
		"/etc/systemd/system/cca-server.service": {
			"--cert /etc/ccattler/pki/api-server-identity-certificate.pem",
			"--key /etc/ccattler/pki/api-server-identity-certificate-key.pem",
			"--ca /etc/ccattler/pki/cluster-identity-ca.pem",
			"--ca-key /etc/ccattler/pki/cluster-identity-ca-key.pem",
			"--gate-ca-cert /etc/ccattler/pki/gate-https-ca.pem",
			"--gate-ca-key /etc/ccattler/pki/gate-https-ca-key.pem",
			"--etcd-cert /etc/ccattler/pki/cca-server-etcd-client-certificate.pem",
			"--etcd-key /etc/ccattler/pki/cca-server-etcd-client-certificate-key.pem",
			"--etcd-ca /etc/ccattler/pki/etcd-access-ca.pem",
			"--etcd-access-ca-key /etc/ccattler/pki/etcd-access-ca-key.pem",
		},
		"/etc/systemd/system/cca-agent.service": {
			"--cert /etc/ccattler/pki/node-identity-for-api.pem",
			"--key /etc/ccattler/pki/node-identity-for-api-key.pem",
			"--ca /etc/ccattler/pki/cluster-identity-ca.pem",
			"--etcd-cert /etc/ccattler/pki/node-etcd-client-certificate.pem",
			"--etcd-key /etc/ccattler/pki/node-etcd-client-certificate-key.pem",
			"--etcd-ca /etc/ccattler/pki/etcd-access-ca.pem",
		},
	}
	for _, unitPath := range slices.Sorted(maps.Keys(unitFlags)) {
		unitText := setupScriptUnitText(t, setupScript, unitPath)
		for _, wantFlag := range unitFlags[unitPath] {
			if !strings.Contains(unitText, wantFlag) {
				t.Errorf("%s lacks %q", unitPath, wantFlag)
			}
		}
	}
	for _, legacyPath := range []string{"/pki/ca.pem", "/pki/node.pem", "/pki/node-key.pem", "/pki/etcd.pem"} {
		if strings.Contains(setupScript, legacyPath) {
			t.Errorf("setup.sh of a new cluster still uses %s", legacyPath)
		}
	}
}

// TestAPIRefusesNonClusterCACertificates: the API, set up as setup.sh runs it,
// refuses an etcd client certificate and a gate certificate (other CAs), and
// accepts the node's API identity (D5).
func TestAPIRefusesNonClusterCACertificates(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	initFilePath := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	echoServer := startPrincipalEchoAPIServer(t, initFilePath("api-server-identity-certificate.pem"),
		initFilePath("api-server-identity-certificate-key.pem"), initFilePath("cluster-identity-ca.pem"))
	clusterCAPEM, _ := os.ReadFile(initFilePath("cluster-identity-ca.pem"))

	gateCA, gateCAError := security.LoadCertificateAuthorityFromFiles(initFilePath("gate-https-ca.pem"), initFilePath("gate-https-ca-key.pem"))
	if gateCAError != nil {
		t.Fatalf("load gate CA: %v", gateCAError)
	}
	envelopeCodec := newClusterPKITestEnvelopeCodec(t)
	gateKeyPair := issueGateKeyPairThroughController(t, controllers.NewRouteTLSController(gateCA, envelopeCodec), envelopeCodec, "app.example.com")
	refusedCertificates := map[string]tls.Certificate{"gate certificate": gateKeyPair}
	for _, etcdClientFile := range []string{"node-etcd-client-certificate", "cca-server-etcd-client-certificate"} {
		etcdClientKeyPair, pairError := tls.LoadX509KeyPair(initFilePath(etcdClientFile+".pem"), initFilePath(etcdClientFile+"-key.pem"))
		if pairError != nil {
			t.Fatal(pairError)
		}
		refusedCertificates[etcdClientFile] = etcdClientKeyPair
	}
	for _, certificateName := range slices.Sorted(maps.Keys(refusedCertificates)) {
		if principal, requestError := requestPrincipalWithClientCertificate(t, echoServer.URL, clusterCAPEM,
			refusedCertificates[certificateName]); requestError == nil {
			t.Errorf("the API accepted the %s and yielded principal %q", certificateName, principal)
		}
	}

	nodeKeyPair, pairError := tls.LoadX509KeyPair(initFilePath("node-identity-for-api.pem"), initFilePath("node-identity-for-api-key.pem"))
	if pairError != nil {
		t.Fatal(pairError)
	}
	if principal, requestError := requestPrincipalWithClientCertificate(t, echoServer.URL, clusterCAPEM, nodeKeyPair); requestError != nil ||
		principal != "node:"+clusterPKITestNodeID {
		t.Errorf("node identity: principal %q, error %v; want node:%s", principal, requestError, clusterPKITestNodeID)
	}
}

// TestAnsibleEtcdClusterTemplateUsesEtcdAccessCA: the HA etcd template serves
// the etcd serving certificate, authenticates peers with the etcd peer
// certificate, and trusts only the etcd access CA for clients and peers (1a).
func TestAnsibleEtcdClusterTemplateUsesEtcdAccessCA(t *testing.T) {
	const templatePath = "deploy/ansible/roles/etcd-cluster/templates/etcd-cluster.conf.j2"
	templateContent, readError := os.ReadFile(filepath.Join("..", "..", templatePath)) //nolint:gosec // G304: fixed repository path
	if readError != nil {
		t.Fatal(readError)
	}
	templateFlags := map[string]string{}
	for _, templateLine := range strings.Split(string(templateContent), "\n") {
		lineFields := strings.Fields(strings.TrimSuffix(strings.TrimSpace(templateLine), "\\"))
		if len(lineFields) >= 2 && strings.HasPrefix(lineFields[0], "--") {
			templateFlags[lineFields[0]] = strings.Join(lineFields[1:], " ")
		}
	}
	for flagName, wantFile := range map[string]string{
		"--trusted-ca-file": etcdAccessCAFileName, "--peer-trusted-ca-file": etcdAccessCAFileName,
		"--cert-file": etcdServingCertificateFileName, "--key-file": privateKeyFileName(etcdServingCertificateFileName),
		"--peer-cert-file": etcdPeerCertificateFileName, "--peer-key-file": privateKeyFileName(etcdPeerCertificateFileName),
	} {
		if wantValue := "{{ cca_cert_dir }}/" + wantFile; templateFlags[flagName] != wantValue {
			t.Errorf("%s: %s = %q, want %q", templatePath, flagName, templateFlags[flagName], wantValue)
		}
	}
	for _, requiredFlag := range []string{"--client-cert-auth", "--peer-client-cert-auth"} {
		if !strings.Contains(string(templateContent), requiredFlag) {
			t.Errorf("%s lacks %s", templatePath, requiredFlag)
		}
	}
}

// mustReadFile reads a file or fails the test.
func mustReadFile(t *testing.T, filePath string) []byte {
	t.Helper()
	fileContent, readError := os.ReadFile(filePath) //nolint:gosec // test file path
	if readError != nil {
		t.Fatal(readError)
	}
	return fileContent
}
