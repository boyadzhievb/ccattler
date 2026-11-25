// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// ansibleTemplateRenderScript renders a role template as Ansible's template
// module does (trim_blocks, strict undefined), with group_vars' cca_pki_layouts
// and cca_pki resolved for the layout the variables name. With mode "pki" it
// prints the resolved cca_pki as JSON instead.
const ansibleTemplateRenderScript = `import json, sys, yaml, jinja2, jinja2.nativetypes
mode, group_vars_path, template_path = sys.argv[1], sys.argv[2], sys.argv[3]
context = json.loads(sys.stdin.read())
group_vars = yaml.safe_load(open(group_vars_path))
context["cca_cert_dir"] = group_vars["cca_cert_dir"]
context["cca_pki_layouts"] = group_vars["cca_pki_layouts"]
context["cca_pki"] = jinja2.nativetypes.NativeEnvironment().from_string(group_vars["cca_pki"]).render(context)
if mode == "pki":
    print(json.dumps(context["cca_pki"]))
else:
    environment = jinja2.Environment(trim_blocks=True, undefined=jinja2.StrictUndefined, keep_trailing_newline=True)
    sys.stdout.write(environment.from_string(open(template_path).read()).render(context))
`

// ansibleGroupVarsPath is the group_vars file holding the PKI layouts.
const ansibleGroupVarsPath = "../../deploy/ansible/group_vars/all.yml"

// ansibleRolesDirectory holds the roles whose templates the tests render.
const ansibleRolesDirectory = "../../deploy/ansible/roles"

// ansiblePKIDirectory is cca_cert_dir in group_vars.
const ansiblePKIDirectory = "/etc/ccattler/pki"

// ansibleLayoutNames maps the group_vars layout names to cca init's layouts.
var ansibleLayoutNames = map[string]clusterPKILayout{"three_ca": threeCAClusterPKILayout, "single_ca": singleCAClusterPKILayout}

// runAnsibleRender runs the render script in mode for templatePath with
// cca_pki_layout set to layoutName, as host nodeID. Skips without python3,
// jinja2 and PyYAML (they come with Ansible).
func runAnsibleRender(t *testing.T, mode string, templatePath string, layoutName string, nodeID string) string {
	t.Helper()
	pythonPath, lookError := exec.LookPath("python3")
	if lookError != nil || exec.Command(pythonPath, "-I", "-c", "import jinja2, yaml").Run() != nil { //nolint:gosec // fixed interpreter probe
		t.Skip("python3 with jinja2 and PyYAML (Ansible's dependencies) not installed")
	}
	templateVariables, _ := json.Marshal(map[string]any{
		"cca_pki_layout": layoutName, "ansible_host": "127.0.0.1", "inventory_hostname": nodeID,
		"cluster_cidr": "10.100.0.0/16", "vip_cidr": "10.200.0.0/24", "etcd_initial_cluster": "ctrl=https://127.0.0.1:2380",
		"groups":   map[string][]string{"controlplane": {"ctrl"}, "etcd": {"ctrl"}},
		"hostvars": map[string]map[string]string{"ctrl": {"ansible_host": "127.0.0.1"}},
	})
	renderCommand := exec.Command(pythonPath, "-I", "-c", ansibleTemplateRenderScript, mode, ansibleGroupVarsPath, templatePath) //nolint:gosec // repository templates
	renderCommand.Stdin = strings.NewReader(string(templateVariables))
	renderedOutput, renderError := renderCommand.CombinedOutput()
	if renderError != nil {
		t.Fatalf("render %s (%s): %v: %s", templatePath, layoutName, renderError, renderedOutput)
	}
	return string(renderedOutput)
}

// renderAnsibleRoleTemplate renders roles/<role>/templates/<name> for a layout.
func renderAnsibleRoleTemplate(t *testing.T, roleName string, templateName string, layoutName string, nodeID string) string {
	t.Helper()
	return runAnsibleRender(t, "template", filepath.Join(ansibleRolesDirectory, roleName, "templates", templateName), layoutName, nodeID)
}

// serverConfigFromRenderedUnit parses the cca server flags of a rendered unit,
// with the PKI directory mapped to pkiDirectory.
func serverConfigFromRenderedUnit(t *testing.T, renderedUnit string, pkiDirectory string) serverCommandConfig {
	t.Helper()
	_, execStart, found := strings.Cut(renderedUnit, "ExecStart=/usr/local/bin/cca server ")
	if !found {
		t.Fatalf("rendered unit has no cca server ExecStart:\n%s", renderedUnit)
	}
	execStart, _, _ = strings.Cut(execStart, "\nRestart=")
	execStart = strings.ReplaceAll(strings.ReplaceAll(execStart, "\\\n", " "), ansiblePKIDirectory+"/", pkiDirectory+"/")
	parsedConfig, parseError := parseServerCommandArgs(strings.Fields(execStart))
	if parseError != nil {
		t.Fatalf("parse rendered cca server flags: %v", parseError)
	}
	return parsedConfig
}

// enrolledNodeRecorded reports whether factStore holds an enrollment record for nodeID.
func enrolledNodeRecorded(factStore store.StateStore, nodeID string) bool {
	_, getError := factStore.Get(context.Background(), security.EnrolledNodePrefix+nodeID)
	return getError == nil
}

// TestControlPlaneNodeCertificateMustBelongToTheCluster (SA2-L1/CR2-L1, user
// decision 1 "exits"): the server records --control-plane-node-cert only when it
// chains to --ca, is valid and names an unreserved node; else the bootstrap
// fails with an error naming the path, so cca server exits, and records nothing.
func TestControlPlaneNodeCertificateMustBelongToTheCluster(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	clusterCA, loadError := security.LoadCertificateAuthorityFromFiles(filepath.Join(outputDirectory, clusterIdentityCAFileName),
		filepath.Join(outputDirectory, privateKeyFileName(clusterIdentityCAFileName)))
	if loadError != nil {
		t.Fatal(loadError)
	}
	foreignCA, _ := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, time.Hour)
	issueCertificateWithUsage := func(issuingCA *security.CertificateAuthority, commonName string, usage security.CertificateUsage, ttl time.Duration) string {
		issued, issueError := issuingCA.IssueCertificate(security.IssueCertificateRequest{CommonName: commonName,
			Usage: usage, TTL: ttl})
		if issueError != nil {
			t.Fatal(issueError)
		}
		return writeClusterPKITestFile(t, t.TempDir(), commonName+".pem", issued.CertificatePEM)
	}
	issueCertificateFile := func(issuingCA *security.CertificateAuthority, commonName string, ttl time.Duration) string {
		return issueCertificateWithUsage(issuingCA, commonName, security.UsageClient, ttl)
	}
	testCases := []struct {
		name            string // what the flag points at
		certificatePath string // the --control-plane-node-cert file
		recordedNodeID  string // the CN that must (or must not) be recorded
		wantReason      string // "" for a valid identity, else a fragment of the refusal reason
	}{
		{"node identity from cca init", filepath.Join(outputDirectory, nodeIdentityForAPIFileName), clusterPKITestNodeID, ""},
		{"certificate from a foreign CA", issueCertificateFile(foreignCA, "victim", time.Hour), "victim", "unknown authority"},
		{"expired cluster certificate", issueCertificateFile(clusterCA, "expiredx", time.Nanosecond), "expiredx", "expired"},
		{"reserved node ID cca-server", issueCertificateFile(clusterCA, security.CCAServerEtcdClientName, time.Hour), security.CCAServerEtcdClientName, "reserved"},
		{"invalid node ID", issueCertificateFile(clusterCA, "Not_A_Label", time.Hour), "Not_A_Label", "must be a lowercase DNS name"},
		{"server-only certificate (no ClientAuth)", issueCertificateWithUsage(clusterCA, "serveronly", security.UsageServer, time.Hour), "serveronly", "usage"},
		{"the API server certificate", filepath.Join(outputDirectory, apiServerIdentityCertificateFileName), security.ServerCertificateCommonName, "reserved"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
			parsedConfig.controlPlaneNodeCertificatePath = testCase.certificatePath
			serverTLSConfig := loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
			factStore := store.NewMemoryStore()
			t.Cleanup(func() { _ = factStore.Close() })
			bootstrapError := bootstrapControlPlaneNodeEnrollment(factStore, serverTLSConfig, parsedConfig)
			recorded := enrolledNodeRecorded(factStore, testCase.recordedNodeID)
			if testCase.wantReason == "" {
				if bootstrapError != nil || !recorded {
					t.Errorf("valid node identity: error %v, recorded %v; want nil and recorded", bootstrapError, recorded)
				}
				return
			}
			if bootstrapError == nil || !strings.Contains(bootstrapError.Error(), testCase.certificatePath) ||
				!strings.Contains(bootstrapError.Error(), testCase.wantReason) {
				t.Errorf("bootstrap error = %v; want a startup error naming %s and %q", bootstrapError, testCase.certificatePath, testCase.wantReason)
			}
			if recorded {
				t.Errorf("node %q recorded from a refused --control-plane-node-cert", testCase.recordedNodeID)
			}
		})
	}
}

// TestThreeCAServerWithoutNodeCertFlagRecordsNoServerName (SA2-L1): without
// --control-plane-node-cert, a three-CA server's certificate names
// ccattler-server; that name is never recorded as an enrolled node.
func TestThreeCAServerWithoutNodeCertFlagRecordsNoServerName(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
	parsedConfig.controlPlaneNodeCertificatePath = ""
	serverTLSConfig := loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	logOutput := captureDefaultLogger(t)
	if bootstrapError := bootstrapControlPlaneNodeEnrollment(factStore, serverTLSConfig, parsedConfig); bootstrapError != nil {
		t.Errorf("without --control-plane-node-cert the server must start (warn only), got error %v", bootstrapError)
	}
	if enrolledNodeRecorded(factStore, security.ServerCertificateCommonName) {
		t.Errorf("a three-CA server without --control-plane-node-cert recorded %q as an enrolled node", security.ServerCertificateCommonName)
	}
	if !strings.Contains(logOutput.String(), "control-plane node not recorded as enrolled") {
		t.Errorf("without --control-plane-node-cert the server did not warn; log: %q", logOutput.String())
	}
}

// lockedLogBuffer is a log sink safe for concurrent writers.
type lockedLogBuffer struct {
	bufferLock sync.Mutex      // guards logContent
	logContent strings.Builder // everything logged so far
}

// Write appends one log line under the lock.
func (logBuffer *lockedLogBuffer) Write(logLine []byte) (int, error) {
	logBuffer.bufferLock.Lock()
	defer logBuffer.bufferLock.Unlock()
	return logBuffer.logContent.Write(logLine)
}

// String returns everything logged so far.
func (logBuffer *lockedLogBuffer) String() string {
	logBuffer.bufferLock.Lock()
	defer logBuffer.bufferLock.Unlock()
	return logBuffer.logContent.String()
}

// captureDefaultLogger points the default logger at a buffer for the test and
// restores the previous logger at cleanup.
func captureDefaultLogger(t *testing.T) *lockedLogBuffer {
	t.Helper()
	logOutput := &lockedLogBuffer{}
	previousLogger := logging.Default()
	logging.SetDefault(logging.New(logOutput, logging.LevelInfo, logging.FormatHuman))
	t.Cleanup(func() { logging.SetDefault(previousLogger) })
	return logOutput
}

// TestHAControlPlaneUnitRecordsItsNode (SA2-L1, CR2 mutation M26): the HA
// role's cca-server unit, rendered for a three-CA cluster, passes
// --control-plane-node-cert, so the secondary records its node and an unbound
// join token cannot enroll as it.
func TestHAControlPlaneUnitRecordsItsNode(t *testing.T) {
	const secondaryNodeID = "ctrl-2"
	secondaryDirectory := runHASecondaryInit(t, runClusterPKITestInit(t), secondaryNodeID)
	renderedUnit := renderAnsibleRoleTemplate(t, "ha-controlplane", "cca-server.service.j2", "three_ca", secondaryNodeID)
	parsedConfig := serverConfigFromRenderedUnit(t, renderedUnit, secondaryDirectory)
	if parsedConfig.controlPlaneNodeCertificatePath != filepath.Join(secondaryDirectory, nodeIdentityForAPIFileName) {
		t.Fatalf("HA unit --control-plane-node-cert = %q, want %s", parsedConfig.controlPlaneNodeCertificatePath, nodeIdentityForAPIFileName)
	}
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	serverTLSConfig := loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
	rbacAuthorizer := security.NewRBACAuthorizer()
	bootstrapAdminGrants(factStore, bindServerPrincipal(serverTLSConfig, rbacAuthorizer))
	if bootstrapError := bootstrapControlPlaneNodeEnrollment(factStore, serverTLSConfig, parsedConfig); bootstrapError != nil {
		t.Fatal(bootstrapError)
	}
	clusterCA, caError := security.LoadCertificateAuthorityFromFiles(parsedConfig.tlsCACertPath, parsedConfig.tlsCAKeyPath)
	if caError != nil {
		t.Fatal(caError)
	}
	enrollmentService := buildEnrollmentService(factStore, clusterCA, rbacAuthorizer, parsedConfig)
	ctx := context.Background()
	if !enrollmentService.IsNodeEnrolled(ctx, secondaryNodeID) {
		t.Errorf("HA secondary %q is not recorded as enrolled", secondaryNodeID)
	}
	unboundToken, _ := enrollmentService.GenerateJoinToken(ctx, "", time.Hour)
	if _, enrollError := enrollmentService.EnrollNode(ctx, security.EnrollmentRequest{Token: unboundToken.Token,
		NodeID: secondaryNodeID, CSRPEM: newReworkTestCSR(t), EtcdCSRPEM: newReworkTestCSR(t)}); enrollError == nil {
		t.Errorf("an unbound join token enrolled as HA secondary %q", secondaryNodeID)
	}
}

// ansibleLayoutFields ties each group_vars cca_pki key to the cca init file it names.
func ansibleLayoutFields(layout clusterPKILayout) map[string]string {
	layoutFields := map[string]string{
		"cluster_ca": layout.clusterCAFileName, "cluster_ca_key": privateKeyFileName(layout.clusterCAFileName),
		"server_cert": layout.apiServerFileName, "server_key": privateKeyFileName(layout.apiServerFileName),
		"node_cert": layout.nodeIdentityFileName, "node_key": privateKeyFileName(layout.nodeIdentityFileName),
		"etcd_ca":           layout.etcdCAFileName,
		"etcd_serving_cert": layout.etcdServingFileName, "etcd_serving_key": privateKeyFileName(layout.etcdServingFileName),
		"etcd_peer_cert": layout.etcdPeerFileName, "etcd_peer_key": privateKeyFileName(layout.etcdPeerFileName),
		"server_etcd_cert": layout.serverEtcdClientFileName, "server_etcd_key": privateKeyFileName(layout.serverEtcdClientFileName),
		"node_etcd_cert": layout.nodeEtcdClientFileName, "node_etcd_key": privateKeyFileName(layout.nodeEtcdClientFileName),
	}
	if !layout.isSingleCA() {
		layoutFields["etcd_ca_key"] = privateKeyFileName(layout.etcdCAFileName)
		layoutFields["gate_ca"], layoutFields["gate_ca_key"] = layout.gateCAFileName, privateKeyFileName(layout.gateCAFileName)
	}
	return layoutFields
}

// renderedPKIFilePattern finds the PKI files a rendered unit names.
var renderedPKIFilePattern = regexp.MustCompile(regexp.QuoteMeta(ansiblePKIDirectory) + `/([A-Za-z0-9._-]+)`)

// TestAnsibleUnitsFollowTheClusterPKILayout (CR2-L4): group_vars names the
// same files as cca init for both layouts, and the rendered server, agent and
// etcd units name only files of their cluster's layout, so a playbook re-run on
// a cluster from before M280 keeps its units working.
func TestAnsibleUnitsFollowTheClusterPKILayout(t *testing.T) {
	for layoutName, layout := range ansibleLayoutNames {
		t.Run(layoutName, func(t *testing.T) {
			var groupVarsFiles map[string]any
			if decodeError := json.Unmarshal([]byte(runAnsibleRender(t, "pki", "", layoutName, "ctrl")), &groupVarsFiles); decodeError != nil {
				t.Fatal(decodeError)
			}
			for fieldName, wantFile := range ansibleLayoutFields(layout) {
				if groupVarsFiles[fieldName] != wantFile {
					t.Errorf("group_vars %s.%s = %v, want %s", layoutName, fieldName, groupVarsFiles[fieldName], wantFile)
				}
			}
			layoutFiles := map[string]bool{}
			for _, installedFile := range layout.installedFileNames() {
				layoutFiles[installedFile], layoutFiles[privateKeyFileName(installedFile)] = true, true
			}
			for _, roleTemplate := range [][2]string{{"ha-controlplane", "cca-server.service.j2"}, {"worker", "cca-agent.service.j2"},
				{"etcd-cluster", "etcd-cluster.conf.j2"}} {
				renderedUnit := renderAnsibleRoleTemplate(t, roleTemplate[0], roleTemplate[1], layoutName, "ctrl")
				for _, fileMatch := range renderedPKIFilePattern.FindAllStringSubmatch(renderedUnit, -1) {
					if !layoutFiles[fileMatch[1]] {
						t.Errorf("%s (%s) names %s, which a %s cluster does not have", roleTemplate[1], layoutName, fileMatch[1], layoutName)
					}
				}
			}
			serverUnit := renderAnsibleRoleTemplate(t, "ha-controlplane", "cca-server.service.j2", layoutName, "ctrl")
			for _, threeCAOnlyFlag := range []string{"--gate-ca-cert", "--etcd-access-ca-key", "--control-plane-node-cert"} {
				if strings.Contains(serverUnit, threeCAOnlyFlag) != !layout.isSingleCA() {
					t.Errorf("cca-server.service.j2 (%s): %s present = %v", layoutName, threeCAOnlyFlag, !layout.isSingleCA())
				}
			}
		})
	}
}

// TestEtcdStartsOnlyAfterThePeerFirewall (SA2-L5): the etcd drop-in requires
// and follows the peer-firewall unit, and the role never restarts that unit
// (Requires= would restart etcd with it).
func TestEtcdStartsOnlyAfterThePeerFirewall(t *testing.T) {
	const firewallUnit = "ccattler-etcd-peer-firewall.service"
	etcdDropIn := renderAnsibleRoleTemplate(t, "etcd-cluster", "etcd-cluster.conf.j2", "three_ca", "ctrl")
	unitSection, _, _ := strings.Cut(etcdDropIn, "[Service]")
	for _, wantLine := range []string{"[Unit]", "Requires=" + firewallUnit, "After=" + firewallUnit} {
		if !slices.Contains(strings.Split(unitSection, "\n"), wantLine) {
			t.Errorf("etcd drop-in [Unit] lacks %q:\n%s", wantLine, unitSection)
		}
	}
	roleTasks := string(mustReadFile(t, filepath.Join(ansibleRolesDirectory, "etcd-cluster", "tasks", "main.yml")))
	_, firewallTask, _ := strings.Cut(roleTasks, "name: ccattler-etcd-peer-firewall\n")
	firewallTask, _, _ = strings.Cut(firewallTask, "\n- name:")
	if !strings.Contains(firewallTask, "state: started") || strings.Contains(firewallTask, "restarted") {
		t.Errorf("the peer-firewall unit task must start, never restart, the unit:\n%s", firewallTask)
	}
}

// writeLegacySingleCAFiles writes a CA as cca init wrote it before M280 (Subject
// O=CCattler, CN "CCattler Root CA", no OU) and returns its certificate and key paths.
func writeLegacySingleCAFiles(t *testing.T) (string, string) {
	t.Helper()
	caPrivateKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		t.Fatal(keyError)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{Organization: []string{"CCattler"}, CommonName: "CCattler Root CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true, IsCA: true}
	caDER, createError := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	keyDER, marshalError := x509.MarshalECPrivateKey(caPrivateKey)
	if createError != nil || marshalError != nil {
		t.Fatal(createError, marshalError)
	}
	caDirectory := t.TempDir()
	return writeClusterPKITestFile(t, caDirectory, legacyClusterCAFileName, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		writeClusterPKITestFile(t, caDirectory, privateKeyFileName(legacyClusterCAFileName), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// TestCheckLoadedCAKind (SA2-L3): cca init checks each given CA's kind by its
// Subject OU. The single-CA path refuses an M280 CA (e.g. etcd-access-ca.pem as
// --ca) and accepts a CA from before M280; the six-flag path refuses swapped CAs.
// The refusal through the binary: TestInitRefusesEtcdAccessCAAsSingleCA (cli tests).
func TestCheckLoadedCAKind(t *testing.T) {
	firstDirectory := runClusterPKITestInit(t)
	loadCA := func(certificatePath string, keyPath string) *security.CertificateAuthority {
		loadedCA, loadError := security.LoadCertificateAuthorityFromFiles(certificatePath, keyPath)
		if loadError != nil {
			t.Fatal(loadError)
		}
		return loadedCA
	}
	loadInitCA := func(caFileName string) *security.CertificateAuthority {
		return loadCA(filepath.Join(firstDirectory, caFileName), filepath.Join(firstDirectory, privateKeyFileName(caFileName)))
	}
	legacyCA := loadCA(writeLegacySingleCAFiles(t))
	testCases := []struct {
		name           string                            // the CA given and where
		loadedCA       *security.CertificateAuthority    // the CA file's content
		wantKind       security.CertificateAuthorityKind // the flag's kind
		singleCALayout bool                              // --ca/--ca-key alone
		wantError      string                            // "" when accepted
	}{
		{"etcd access CA as the single --ca", loadInitCA(etcdAccessCAFileName), security.ClusterIdentityCertificateAuthority, true, "three-CA cluster"},
		{"cluster identity CA as the single --ca", loadInitCA(clusterIdentityCAFileName), security.ClusterIdentityCertificateAuthority, true, "three-CA cluster"},
		{"gate HTTPS CA as --etcd-access-ca", loadInitCA(gateHTTPSCAFileName), security.EtcdAccessCertificateAuthority, false, "not the etcd access CA"},
		{"a CA from before M280 as --gate-https-ca", legacyCA, security.GateHTTPSCertificateAuthority, false, "not a gate HTTPS CA"},
		{"etcd access CA as --etcd-access-ca", loadInitCA(etcdAccessCAFileName), security.EtcdAccessCertificateAuthority, false, ""},
		{"a CA from before M280 as the single --ca", legacyCA, security.ClusterIdentityCertificateAuthority, true, ""},
	}
	for _, testCase := range testCases {
		kindError := checkLoadedCAKind(testCase.loadedCA, testCase.wantKind, testCase.singleCALayout)
		switch {
		case testCase.wantError == "" && kindError != nil:
			t.Errorf("%s: refused: %v", testCase.name, kindError)
		case testCase.wantError != "" && (kindError == nil || !strings.Contains(kindError.Error(), testCase.wantError)):
			t.Errorf("%s: error %v, want a refusal naming %q", testCase.name, kindError, testCase.wantError)
		}
	}
}

// etcdCommonNamesOf returns every etcd CN a node ID yields, keyed by identity kind.
func etcdCommonNamesOf(nodeID string) map[string]string {
	return map[string]string{"serving": etcdServingCommonNamePrefix + nodeID, "peer": etcdPeerCommonNamePrefix + nodeID,
		"node client": security.NodeEtcdClientCommonNamePrefix + nodeID}
}

// TestEtcdCommonNamesAreDisjoint (SA2-L4): no node ID yields another identity's
// etcd CN, nor cca server's etcd-client-cca-server, so M181 can map roles from CNs.
func TestEtcdCommonNamesAreDisjoint(t *testing.T) {
	nodeIDs := []string{"ctrl", "client-cca-server", "client-ctrl", "peer-ctrl", "server-ctrl", "node-ctrl", "client-node-ctrl", "cca-server"}
	commonNameOwners := map[string]string{ccaServerEtcdClientCommonName: "cca server"}
	for _, nodeID := range nodeIDs {
		for identityKind, commonName := range etcdCommonNamesOf(nodeID) {
			owner := identityKind + " of node " + nodeID
			if earlierOwner, taken := commonNameOwners[commonName]; taken {
				t.Errorf("etcd CN %q is both the %s and the %s", commonName, earlierOwner, owner)
			}
			commonNameOwners[commonName] = owner
		}
	}
}

// TestLocalAdminCACandidatesMatchTheCLI (CR2 suggestion): the e2e helper tries
// the cluster CA files in the CLI's order (installedClusterCACandidatePaths).
func TestLocalAdminCACandidatesMatchTheCLI(t *testing.T) {
	helperOutput, runError := exec.Command("bash", "-c", //nolint:gosec // repository helper
		`source ../../test/e2e/local_admin_access.sh && printf '%s' "$LOCAL_ADMIN_CA_CANDIDATES"`).CombinedOutput()
	if runError != nil {
		t.Fatalf("source local_admin_access.sh: %v: %s", runError, helperOutput)
	}
	if helperCandidates := strings.Fields(string(helperOutput)); !slices.Equal(helperCandidates, installedClusterCACandidatePaths) {
		t.Errorf("LOCAL_ADMIN_CA_CANDIDATES = %v, want the CLI's %v", helperCandidates, installedClusterCACandidatePaths)
	}
}

// TestPKIFileRemoteExpressionFollowsTheLayout (CR2-L4): the expression
// cluster_test.sh puts in remote commands names the M280 file when
// cluster-identity-ca.pem is installed, else the file from before M280.
func TestPKIFileRemoteExpressionFollowsTheLayout(t *testing.T) {
	for _, testCase := range []struct {
		installedCAFile string // the cluster CA file present on the host
		wantFile        string // the file the expression must name
	}{{clusterIdentityCAFileName, etcdAccessCAFileName}, {legacyClusterCAFileName, legacyClusterCAFileName}} {
		pkiDirectory := t.TempDir()
		writeClusterPKITestFile(t, pkiDirectory, testCase.installedCAFile, []byte("CA"))
		evaluated, runError := exec.Command("bash", "-c", //nolint:gosec // repository helper
			`source ../../test/e2e/local_admin_access.sh && CLUSTER_PKI_DIRECTORY="$1" && eval "echo $(pki_file_remote_expression etcd-access-ca.pem ca.pem)"`,
			"bash", pkiDirectory).CombinedOutput()
		if runError != nil {
			t.Fatalf("%v: %s", runError, evaluated)
		}
		if got := strings.TrimSpace(string(evaluated)); got != filepath.Join(pkiDirectory, testCase.wantFile) {
			t.Errorf("with %s installed the expression names %s, want %s", testCase.installedCAFile, got, testCase.wantFile)
		}
	}
}
