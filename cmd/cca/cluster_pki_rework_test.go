// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// serverConfigFromSetupScript parses the cca-server ExecStart of the setup.sh
// that cca init wrote into outputDirectory, with the installed PKI paths mapped
// back to outputDirectory: the server as setup.sh starts it.
func serverConfigFromSetupScript(t *testing.T, outputDirectory string, advertiseAddress string, nodeID string) serverCommandConfig {
	t.Helper()
	setupScript := buildSetupScript(advertiseAddress, nodeID, outputDirectory, "", "", nil, threeCAClusterPKILayout)
	unitText := setupScriptUnitText(t, setupScript, "/etc/systemd/system/cca-server.service")
	_, execStart, found := strings.Cut(unitText, "ExecStart=/usr/local/bin/cca server ")
	if !found {
		t.Fatal("setup.sh cca-server unit has no ExecStart")
	}
	execStart, _, _ = strings.Cut(execStart, "\nRestart=")
	execStart = strings.ReplaceAll(execStart, "\\\n", " ")
	execStart = strings.ReplaceAll(execStart, clusterPKIDirectory+"/", outputDirectory+"/")
	parsedConfig, parseError := parseServerCommandArgs(strings.Fields(execStart))
	if parseError != nil {
		t.Fatalf("parse setup.sh server flags: %v", parseError)
	}
	return parsedConfig
}

// newReworkTestCSR returns a PEM CSR with a fresh key.
func newReworkTestCSR(t *testing.T) []byte {
	t.Helper()
	privateKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		t.Fatal(keyError)
	}
	csrDER, csrError := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, privateKey)
	if csrError != nil {
		t.Fatal(csrError)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

// bootstrapServerAsSetupScriptRuns runs the server's grant and enrollment
// bootstrap with setup.sh's flags on factStore and returns an enrollment service
// wired as cca server wires it.
func bootstrapServerAsSetupScriptRuns(t *testing.T, factStore store.StateStore, outputDirectory string, nodeID string) *security.EnrollmentService {
	t.Helper()
	parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", nodeID)
	serverTLSConfig := loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
	rbacAuthorizer := security.NewRBACAuthorizer()
	bootstrapAdminGrants(factStore, bindServerPrincipal(serverTLSConfig, rbacAuthorizer))
	bootstrapControlPlaneNodeEnrollment(factStore, serverTLSConfig, parsedConfig)
	clusterCA, caError := security.LoadCertificateAuthorityFromFiles(parsedConfig.tlsCACertPath, parsedConfig.tlsCAKeyPath)
	if caError != nil {
		t.Fatal(caError)
	}
	return buildEnrollmentService(factStore, clusterCA, rbacAuthorizer, parsedConfig)
}

// runHASecondaryInit runs cca init for an HA secondary that reuses the three
// CAs of firstDirectory, as the HA role does, and returns its output directory.
func runHASecondaryInit(t *testing.T, firstDirectory string, nodeID string) string {
	t.Helper()
	inFirst := func(fileName string) string { return filepath.Join(firstDirectory, fileName) }
	secondaryDirectory := t.TempDir()
	executeInitCommand(initCommandConfig{outputDirectory: secondaryDirectory, advertiseAddress: "127.0.0.1", nodeID: nodeID,
		caValidity: defaultCACertificateValidityInit, certValidity: defaultNodeCertificateValidityInit,
		existingCAPath: inFirst(clusterIdentityCAFileName), existingCAKeyPath: inFirst(privateKeyFileName(clusterIdentityCAFileName)),
		existingEtcdAccessCAPath: inFirst(etcdAccessCAFileName), existingEtcdAccessCAKeyPath: inFirst(privateKeyFileName(etcdAccessCAFileName)),
		existingGateHTTPSCAPath: inFirst(gateHTTPSCAFileName), existingGateHTTPSCAKeyPath: inFirst(privateKeyFileName(gateHTTPSCAFileName)),
	})
	return secondaryDirectory
}

// TestServerRecordsControlPlaneNodeEnrollment (SA-H1, CR-M2): a server started
// as setup.sh starts it records its own node (the init host, and an HA
// secondary) as enrolled from node-identity-for-api.pem, so an unbound join token
// cannot enroll as that node and the node's agent gets its gate keys.
func TestServerRecordsControlPlaneNodeEnrollment(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	firstDirectory := runClusterPKITestInit(t)
	controlPlaneDirectories := map[string]string{clusterPKITestNodeID: firstDirectory, "ctrl-2": runHASecondaryInit(t, firstDirectory, "ctrl-2")}
	var enrollmentService *security.EnrollmentService
	for nodeID, outputDirectory := range controlPlaneDirectories {
		enrollmentService = bootstrapServerAsSetupScriptRuns(t, factStore, outputDirectory, nodeID)
	}
	gateKeyDelivery := security.NewGateKeyDeliveryService(factStore, nil, nil)
	for nodeID := range controlPlaneDirectories {
		if !enrollmentService.IsNodeEnrolled(ctx, nodeID) {
			t.Errorf("control-plane node %q is not recorded as enrolled", nodeID)
		}
		if _, putError := factStore.Put(ctx, types.KeyObservedNodeGateProxy(nodeID), []byte(types.GateProxyEnabled)); putError != nil {
			t.Fatal(putError)
		}
		if _, deliveryError := gateKeyDelivery.DeliverGateKeys(ctx, nodeID); deliveryError != nil {
			t.Errorf("the agent of control-plane node %q is refused its gate keys: %v", nodeID, deliveryError)
		}
		unboundToken, _ := enrollmentService.GenerateJoinToken(ctx, "", time.Hour)
		if response, enrollError := enrollmentService.EnrollNode(ctx, security.EnrollmentRequest{Token: unboundToken.Token,
			NodeID: nodeID, CSRPEM: newReworkTestCSR(t), EtcdCSRPEM: newReworkTestCSR(t)}); enrollError == nil {
			t.Errorf("an unbound join token enrolled as control-plane node %q: %s", nodeID, response.Principal)
		}
	}
}

// TestServerRecordsNodeFromServerCertificateOnOldClusters: without
// --control-plane-node-cert (a cluster from before M280, whose server presents
// node.pem), the server certificate's CN is recorded as enrolled, as before.
func TestServerRecordsNodeFromServerCertificateOnOldClusters(t *testing.T) {
	clusterCA, _ := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, time.Hour)
	legacyNodeCertificate, issueError := clusterCA.IssueCertificate(security.IssueCertificateRequest{CommonName: "old-ctrl",
		Usage: security.UsageServerAndClient, TTL: time.Hour})
	if issueError != nil {
		t.Fatal(issueError)
	}
	pkiDirectory := t.TempDir()
	serverTLSConfig := loadServerTLSConfig(writeClusterPKITestFile(t, pkiDirectory, "node.pem", legacyNodeCertificate.CertificatePEM),
		writeClusterPKITestFile(t, pkiDirectory, "node-key.pem", legacyNodeCertificate.PrivateKeyPEM),
		writeClusterPKITestFile(t, pkiDirectory, "ca.pem", clusterCA.CACertificatePEM()), true)
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	bootstrapControlPlaneNodeEnrollment(factStore, serverTLSConfig, serverCommandConfig{})
	if _, getError := factStore.Get(context.Background(), security.EnrolledNodePrefix+"old-ctrl"); getError != nil {
		t.Errorf("old cluster: node old-ctrl is not recorded as enrolled: %v", getError)
	}
}

// TestLocalAdminCLIReachesThreeCAServer (CR-H1): on a new three-CA cluster the
// CLI as root (`sudo cca status`) trusts cluster-identity-ca.pem, sends no client
// certificate and authenticates with the local token as user:local-admin; the
// node identity certificate alone is no admin credential.
func TestLocalAdminCLIReachesThreeCAServer(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
	localTokenAuthenticator, tokenError := security.NewLocalTokenAuthenticator(t.TempDir())
	if tokenError != nil {
		t.Fatal(tokenError)
	}
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	apiServer := api.NewServer(factStore)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(security.NewMTLSAuthenticator(), localTokenAuthenticator))
	apiServer.SetRequirePrincipal(true)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:local-admin", "cluster-admin")
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	statusServer := httptest.NewUnstartedServer(apiServer.Handler())
	statusServer.TLS = loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
	statusServer.StartTLS()
	t.Cleanup(statusServer.Close)

	localToken, readError := os.ReadFile(localTokenAuthenticator.TokenFilePath())
	if readError != nil {
		t.Fatal(readError)
	}
	cliTransport := buildTLSTransportFromCACertCandidates([]string{filepath.Join(outputDirectory, clusterIdentityCAFileName)})
	cliClient := &http.Client{Timeout: clusterPKITestRequestTimeout,
		Transport: &localTokenRoundTripper{baseTransport: cliTransport, bearerToken: strings.TrimSpace(string(localToken))}}
	statusResponse, statusError := cliClient.Get(statusServer.URL + "/api/status")
	if statusError != nil {
		t.Fatalf("sudo cca status: %v", statusError)
	}
	_ = statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK {
		t.Errorf("sudo cca status: HTTP %d, want 200", statusResponse.StatusCode)
	}

	nodeKeyPair := loadReworkKeyPair(t, outputDirectory, nodeIdentityForAPIFileName)
	nodeCertificateTransport := cliTransport.(*http.Transport).Clone()
	nodeCertificateTransport.TLSClientConfig.Certificates = []tls.Certificate{nodeKeyPair}
	nodeResponse, nodeError := (&http.Client{Timeout: clusterPKITestRequestTimeout, Transport: nodeCertificateTransport}).Get(statusServer.URL + "/api/status")
	if nodeError != nil {
		t.Fatalf("node identity request: %v", nodeError)
	}
	_ = nodeResponse.Body.Close()
	if nodeResponse.StatusCode != http.StatusForbidden {
		t.Errorf("node identity certificate alone on /api/status: HTTP %d, want 403 (no certificate is an admin credential)", nodeResponse.StatusCode)
	}
}

// loadReworkKeyPair loads a certificate cca init wrote, with its key.
func loadReworkKeyPair(t *testing.T, outputDirectory string, certificateFileName string) tls.Certificate {
	t.Helper()
	keyPair, pairError := tls.LoadX509KeyPair(filepath.Join(outputDirectory, certificateFileName),
		filepath.Join(outputDirectory, privateKeyFileName(certificateFileName)))
	if pairError != nil {
		t.Fatal(pairError)
	}
	return keyPair
}

// TestInitRefusesReservedOrInvalidNodeIDs (SA-L3): cca init validates --node-id
// like enrollment: a malformed ID and the reserved server names are refused.
func TestInitRefusesReservedOrInvalidNodeIDs(t *testing.T) {
	for _, refusedNodeID := range []string{"ccattler-server", "cca-server", "Bad_Node", "-ctrl"} {
		if _, parseError := parseInitCommandArgs([]string{"--node-id", refusedNodeID, "--advertise-address", "127.0.0.1"}); parseError == nil {
			t.Errorf("cca init --node-id %q: accepted, want an error", refusedNodeID)
		}
	}
	if _, parseError := parseInitCommandArgs([]string{"--node-id", "ctrl-1", "--advertise-address", "127.0.0.1"}); parseError != nil {
		t.Errorf("cca init --node-id ctrl-1: %v", parseError)
	}
}

// TestEtcdClientNetworkWarningStatesResidualTrust (SA-L4): the wide-network
// warning states the residual trust until M181, not the fixed M280 hole.
func TestEtcdClientNetworkWarningStatesResidualTrust(t *testing.T) {
	_, wideNetwork, _ := net.ParseCIDR("0.0.0.0/1")
	warnings := etcdClientNetworkWarnings([]*net.IPNet{wideNetwork})
	if len(warnings) != 1 || strings.Contains(warnings[0], "M280") || !strings.Contains(warnings[0], "M181") ||
		!strings.Contains(warnings[0], "etcd client certificate") {
		t.Errorf("warning %q: want the M181 residual-trust text", warnings)
	}
}

// TestServerWarnsOnEverySingleCACase (SA-L2, CR-L5): the single-CA warning also
// fires when the gate CA is the cluster CA, when --etcd-ca is a bundle holding
// the cluster CA, without --etcd-ca, and with auto --tls; not on a three-CA server.
func TestServerWarnsOnEverySingleCACase(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	inInit := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	bundlePath := writeClusterPKITestFile(t, t.TempDir(), "etcd-bundle.pem",
		append(mustReadFile(t, inInit(etcdAccessCAFileName)), mustReadFile(t, inInit(clusterIdentityCAFileName))...))
	threeCAConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
	withChange := func(change func(*serverCommandConfig)) serverCommandConfig {
		changedConfig := threeCAConfig
		change(&changedConfig)
		return changedConfig
	}
	testCases := map[string]struct {
		parsedConfig serverCommandConfig // flags of the server start
		wantWarning  bool                // whether the single-CA warning is due
	}{
		"three CAs as setup.sh starts it": {threeCAConfig, false},
		"gate CA is the cluster CA": {withChange(func(changed *serverCommandConfig) {
			changed.gateCACertPath, changed.gateCAKeyPath = changed.tlsCACertPath, changed.tlsCAKeyPath
		}), true},
		"etcd CA bundle holds the cluster CA": {withChange(func(changed *serverCommandConfig) { changed.etcdCACertPath = bundlePath }), true},
		"no --etcd-ca":                        {withChange(func(changed *serverCommandConfig) { changed.etcdCACertPath = "" }), true},
		"auto --tls":                          {serverCommandConfig{tlsEnabled: true}, true},
		"plain, no TLS":                       {serverCommandConfig{}, false},
	}
	for testName, testCase := range testCases {
		if warning := singleCAClusterWarning(testCase.parsedConfig); (warning != "") != testCase.wantWarning {
			t.Errorf("%s: warning %q, want warning %v", testName, warning, testCase.wantWarning)
		}
	}
}

// TestInitSingleCASecondaryWritesOldLayout (CR-L7): on a cluster from before
// M280, an HA secondary given only --ca/--ca-key reuses the single CA and writes
// the old layout (ca.pem, node.pem, etcd.pem), and its setup.sh uses those files.
func TestInitSingleCASecondaryWritesOldLayout(t *testing.T) {
	singleCA, _ := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, time.Hour)
	singleCAKeyPEM, _ := singleCA.CAPrivateKeyPEM()
	firstDirectory := t.TempDir()
	parsedConfig, parseError := parseInitCommandArgs([]string{"--node-id", "ctrl-2", "--advertise-address", "127.0.0.1",
		"--ca", writeClusterPKITestFile(t, firstDirectory, "ca.pem", singleCA.CACertificatePEM()),
		"--ca-key", writeClusterPKITestFile(t, firstDirectory, "ca-key.pem", singleCAKeyPEM), "-o", t.TempDir()})
	if parseError != nil {
		t.Fatalf("cca init --ca/--ca-key on a single-CA cluster: %v", parseError)
	}
	executeInitCommand(parsedConfig)
	inOutput := func(fileName string) string { return filepath.Join(parsedConfig.outputDirectory, fileName) }
	for _, legacyFile := range []string{"ca.pem", "ca-key.pem", "node.pem", "node-key.pem", "etcd.pem", "etcd-key.pem"} {
		if !fileExists(inOutput(legacyFile)) {
			t.Errorf("single-CA secondary: %s not written", legacyFile)
		}
	}
	if fileExists(inOutput(clusterIdentityCAFileName)) || fileExists(inOutput(etcdAccessCAFileName)) || fileExists(inOutput(gateHTTPSCAFileName)) {
		t.Error("single-CA secondary wrote new CAs its peers do not trust")
	}
	for _, leafFile := range []string{"node.pem", "etcd.pem"} {
		if leafCertificate := readInitCertificate(t, parsedConfig.outputDirectory, leafFile); !singleCA.IsIssuerOf(leafCertificate) {
			t.Errorf("%s is not signed by the cluster's single CA", leafFile)
		}
	}
	setupScript := string(mustReadFile(t, inOutput("setup.sh")))
	for _, wantText := range []string{`ETCD_TRUSTED_CA_FILE=/etc/ccattler/pki/ca.pem`, "--cert /etc/ccattler/pki/node.pem",
		"--etcd-cert /etc/ccattler/pki/node.pem", "--ca /etc/ccattler/pki/ca.pem"} {
		if !strings.Contains(setupScript, wantText) {
			t.Errorf("single-CA setup.sh lacks %q", wantText)
		}
	}
	if strings.Contains(setupScript, "--gate-ca-cert") || strings.Contains(setupScript, "--etcd-access-ca-key") {
		t.Error("single-CA setup.sh names a gate or etcd access CA the cluster does not have")
	}
}

// TestServerWiresGateCAIntoRouteTLS (CR-M4 M12): with setup.sh's flags the
// server's RouteTLSController issues gate certificates from gate-https-ca.pem.
func TestServerWiresGateCAIntoRouteTLS(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
	clusterCA, _ := security.LoadCertificateAuthorityFromFiles(parsedConfig.tlsCACertPath, parsedConfig.tlsCAKeyPath)
	gateCA, _ := security.LoadCertificateAuthorityFromFiles(parsedConfig.gateCACertPath, parsedConfig.gateCAKeyPath)
	envelopeCodec := newClusterPKITestEnvelopeCodec(t)
	gateKeyPair := issueGateKeyPairThroughController(t, buildRouteTLSController(parsedConfig, clusterCA, envelopeCodec), envelopeCodec, "app.example.com")
	gateCertificate, _ := x509.ParseCertificate(gateKeyPair.Certificate[0])
	if !gateCA.IsIssuerOf(gateCertificate) || clusterCA.IsIssuerOf(gateCertificate) {
		t.Error("the server's RouteTLSController does not issue from the gate HTTPS CA")
	}
}

// TestServerEnrollmentIssuesEtcdClientCertificates (CR-M4 M13): with setup.sh's
// flags the server's enrollment service issues etcd client certificates.
func TestServerEnrollmentIssuesEtcdClientCertificates(t *testing.T) {
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	enrollmentService := bootstrapServerAsSetupScriptRuns(t, factStore, runClusterPKITestInit(t), clusterPKITestNodeID)
	joinToken, _ := enrollmentService.GenerateJoinToken(context.Background(), "worker-1", time.Hour)
	response, enrollError := enrollmentService.EnrollNode(context.Background(), security.EnrollmentRequest{Token: joinToken.Token,
		NodeID: "worker-1", CSRPEM: newReworkTestCSR(t), EtcdCSRPEM: newReworkTestCSR(t)})
	if enrollError != nil || len(response.EtcdCertificatePEM) == 0 {
		t.Fatalf("enrollment on a server started by setup.sh: error %v, etcd certificate %d bytes", enrollError, len(response.EtcdCertificatePEM))
	}
}

// TestJoinEnrollmentRequestCarriesEtcdCSR (CR-M4 M14): cca join sends a node
// CSR and an etcd CSR with different keys.
func TestJoinEnrollmentRequestCarriesEtcdCSR(t *testing.T) {
	requestPayload, nodeKey, etcdKey := buildJoinEnrollmentRequest("token", "worker-1", nil, nil)
	var enrollmentRequest struct {
		CSRPEM     string `json:"csr_pem"`      // node identity CSR
		EtcdCSRPEM string `json:"etcd_csr_pem"` // etcd client CSR
	}
	if unmarshalError := json.Unmarshal(requestPayload, &enrollmentRequest); unmarshalError != nil {
		t.Fatal(unmarshalError)
	}
	for csrName, csrPEM := range map[string]string{"csr_pem": enrollmentRequest.CSRPEM, "etcd_csr_pem": enrollmentRequest.EtcdCSRPEM} {
		if csrBlock, _ := pem.Decode([]byte(csrPEM)); csrBlock == nil || csrBlock.Type != "CERTIFICATE REQUEST" {
			t.Errorf("cca join sends no %s", csrName)
		}
	}
	if nodeKey.Equal(etcdKey) {
		t.Error("cca join uses one key for the node identity and the etcd client certificate")
	}
}

// TestSetupScriptInstallsEveryInitFileWithItsMode (CR-M4 M15, M16): setup.sh
// installs every certificate and key cca init wrote (the etcd peer pair
// included), certificates at 0644 and keys at 0600.
func TestSetupScriptInstallsEveryInitFileWithItsMode(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	setupScript := string(mustReadFile(t, filepath.Join(outputDirectory, "setup.sh")))
	initPEMFiles, _ := filepath.Glob(filepath.Join(outputDirectory, "*.pem"))
	for _, pemFilePath := range initPEMFiles {
		fileName := filepath.Base(pemFilePath)
		wantMode := "644"
		if strings.HasSuffix(fileName, "-key.pem") {
			wantMode = "600"
		}
		wantLine := fmt.Sprintf("install -o root -g root -m %s \"$SOURCE_DIR/%s\" \"$PKI_DIR/%s\"", wantMode, fileName, fileName)
		if !strings.Contains(setupScript, wantLine) {
			t.Errorf("setup.sh does not install %s at %s: want %q", fileName, wantMode, wantLine)
		}
	}
	if len(initPEMFiles) != 2*len(threeCAClusterPKILayout.installedFileNames()) {
		t.Errorf("cca init wrote %d PEM files, setup.sh installs %d", len(initPEMFiles), 2*len(threeCAClusterPKILayout.installedFileNames()))
	}
}

// TestCLIEtcdDefaultsPreferM280Files (CR-M4 M18): the CLI and agent etcd
// defaults try the node etcd client certificate with the etcd access CA first,
// then node.pem with ca.pem; the API identity defaults likewise.
func TestCLIEtcdDefaultsPreferM280Files(t *testing.T) {
	wantEtcdSets := []installedPKIFileSet{installedPKIFileSetFor(nodeEtcdClientCertificateFileName, etcdAccessCAFileName),
		installedPKIFileSetFor(legacyNodeCertificateFileName, legacyClusterCAFileName)}
	wantIdentitySets := []installedPKIFileSet{installedPKIFileSetFor(nodeIdentityForAPIFileName, clusterIdentityCAFileName),
		installedPKIFileSetFor(legacyNodeCertificateFileName, legacyClusterCAFileName)}
	if !slices.Equal(installedEtcdClientFileSets, wantEtcdSets) || !slices.Equal(installedNodeIdentityFileSets, wantIdentitySets) {
		t.Errorf("etcd defaults %v, identity defaults %v; want %v and %v", installedEtcdClientFileSets, installedNodeIdentityFileSets,
			wantEtcdSets, wantIdentitySets)
	}
}

// TestAnsibleEtcdPeerPortLimitedToMembers (SA-L1): every node holds an etcd
// access CA client certificate, which etcd's peer port also accepts, so the HA
// role lets only the etcd members reach 2380 (an nftables table, kept at boot).
func TestAnsibleEtcdPeerPortLimitedToMembers(t *testing.T) {
	const roleDirectory = "../../deploy/ansible/roles/etcd-cluster"
	firewallTemplate, readError := os.ReadFile(filepath.Join(roleDirectory, "templates", "etcd-peer-firewall.nft.j2"))
	if readError != nil {
		t.Fatalf("no etcd peer firewall template: %v", readError)
	}
	for _, wantText := range []string{"tcp dport 2380", "drop", "groups['etcd']", "hostvars[member].ansible_host"} {
		if !strings.Contains(string(firewallTemplate), wantText) {
			t.Errorf("etcd-peer-firewall.nft.j2 lacks %q", wantText)
		}
	}
	roleTasks := string(mustReadFile(t, filepath.Join(roleDirectory, "tasks", "main.yml")))
	firewallTaskIndex := strings.Index(roleTasks, "etcd-peer-firewall.nft.j2")
	etcdStartIndex := strings.Index(roleTasks, "- name: Reload systemd and start etcd cluster")
	if firewallTaskIndex < 0 || etcdStartIndex < 0 || firewallTaskIndex > etcdStartIndex {
		t.Error("the etcd-cluster role does not install the peer firewall before it starts etcd")
	}
	if !strings.Contains(roleTasks, "ccattler-etcd-peer-firewall") {
		t.Error("the etcd-cluster role does not keep the peer firewall across reboots (ccattler-etcd-peer-firewall unit)")
	}
}
