// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// failingTransactionStore is a memory store whose transactions fail, as an
// etcd that times out does (SA3-L1).
type failingTransactionStore struct {
	*store.MemoryStore // every other operation works
}

// Transaction fails without writing.
func (failingStore failingTransactionStore) Transaction(context.Context, []store.Compare, []store.Op, []store.Op) (bool, error) {
	return false, errors.New("etcdserver: request timed out")
}

// droppingTransactionStore is a memory store whose transactions report success
// but write nothing: the record is never confirmed (SA3-L1).
type droppingTransactionStore struct {
	*store.MemoryStore // every other operation works
}

// Transaction reports success without writing.
func (droppingStore droppingTransactionStore) Transaction(context.Context, []store.Compare, []store.Op, []store.Op) (bool, error) {
	return true, nil
}

// issueClusterPKITestCertificate issues a client certificate from issuingCA and
// writes it to a new file, returning its path.
func issueClusterPKITestCertificate(t *testing.T, issuingCA *security.CertificateAuthority, commonName string) string {
	t.Helper()
	issued, issueError := issuingCA.IssueCertificate(security.IssueCertificateRequest{CommonName: commonName,
		Usage: security.UsageClient, TTL: time.Hour})
	if issueError != nil {
		t.Fatal(issueError)
	}
	return writeClusterPKITestFile(t, t.TempDir(), commonName+".pem", issued.CertificatePEM)
}

// foreignNodeCertificateFile returns a node certificate "victim" from a CA that
// is not the cluster's.
func foreignNodeCertificateFile(t *testing.T) string {
	t.Helper()
	foreignCA, caError := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, time.Hour)
	if caError != nil {
		t.Fatal(caError)
	}
	return issueClusterPKITestCertificate(t, foreignCA, "victim")
}

// ccaTestBinaryEnvironmentVariable names the cca binary TestMain (cli_test.go,
// package main_test) builds; the same name is declared there.
const ccaTestBinaryEnvironmentVariable = "CCA_TEST_BINARY"

// cliProcessTimeout bounds one cca process a package-main test runs.
const cliProcessTimeout = 30 * time.Second

// runCCABinaryFromPackageMain runs the cca binary TestMain built and returns its
// stdout, stderr and exit code (-1 when killed at the timeout).
func runCCABinaryFromPackageMain(t *testing.T, commandArgs ...string) (string, string, int) {
	t.Helper()
	binaryPath := os.Getenv(ccaTestBinaryEnvironmentVariable)
	if binaryPath == "" {
		t.Fatal("the cca binary TestMain builds is not set (" + ccaTestBinaryEnvironmentVariable + ")")
	}
	timeoutContext, cancelTimeout := context.WithTimeout(context.Background(), cliProcessTimeout)
	defer cancelTimeout()
	ccaCommand := exec.CommandContext(timeoutContext, binaryPath, commandArgs...) //nolint:gosec // the binary under test
	var stdoutBuffer, stderrBuffer strings.Builder
	ccaCommand.Stdout, ccaCommand.Stderr = &stdoutBuffer, &stderrBuffer
	runError := ccaCommand.Run()
	var exitError *exec.ExitError
	if runError != nil && !errors.As(runError, &exitError) {
		t.Fatalf("run cca: %v", runError)
	}
	return stdoutBuffer.String(), stderrBuffer.String(), ccaCommand.ProcessState.ExitCode()
}

// TestServerExitsOnInvalidNodeCertificateBeforeStartup (stop-time check, user
// decision 1): cca server with an invalid --control-plane-node-cert exits 1 with
// the path and reason, before it creates the store (no "connected", no banner).
// The store left untouched: TestInvalidNodeCertificateLeavesEtcdUntouched (etcd_integration).
func TestServerExitsOnInvalidNodeCertificateBeforeStartup(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	foreignCertificatePath := foreignNodeCertificateFile(t)
	serverArgs := append([]string{"server"}, serverArgsFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)...)
	serverArgs = append(serverArgs, "--store", "memory", "--listen", "127.0.0.1:0", "--dns-listen", "127.0.0.1:0",
		"--control-plane-node-cert", foreignCertificatePath)
	stdoutContent, stderrContent, exitCode := runCCABinaryFromPackageMain(t, serverArgs...)
	if exitCode != 1 || !strings.Contains(stderrContent, foreignCertificatePath) || !strings.Contains(stderrContent, "unknown authority") {
		t.Fatalf("cca server with a foreign --control-plane-node-cert: exit %d, stderr %q; want exit 1 naming the path and the reason",
			exitCode, stderrContent)
	}
	if strings.Contains(stdoutContent, "CCattler server") || strings.Contains(stdoutContent, "enrollment") {
		t.Errorf("cca server started before refusing the node certificate; stdout %q", stdoutContent)
	}
}

// TestServerCAKindChecks (SA3-L2): cca server refuses a CA file whose Subject
// OU names another kind than its flag's, and accepts the right kinds and a CA
// from before M280 (no OU) for every flag.
func TestServerCAKindChecks(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	initFile := func(fileName string) string { return filepath.Join(outputDirectory, fileName) }
	legacyCAPath, _ := writeLegacySingleCAFiles(t)
	threeCAConfig := serverCommandConfig{tlsCACertPath: initFile(clusterIdentityCAFileName),
		etcdCACertPath: initFile(etcdAccessCAFileName), gateCACertPath: initFile(gateHTTPSCAFileName)}
	withChange := func(change func(*serverCommandConfig)) serverCommandConfig {
		changedConfig := threeCAConfig
		change(&changedConfig)
		return changedConfig
	}
	testCases := []struct {
		name         string              // the CA files given
		parsedConfig serverCommandConfig // cca server's CA flags
		wantError    string              // "" when accepted
	}{
		{"three-CA cluster as setup.sh starts it", threeCAConfig, ""},
		{"pre-M280 CA for every flag", serverCommandConfig{tlsCACertPath: legacyCAPath, etcdCACertPath: legacyCAPath, gateCACertPath: legacyCAPath}, ""},
		{"etcd access CA as --gate-ca-cert", withChange(func(config *serverCommandConfig) { config.gateCACertPath = initFile(etcdAccessCAFileName) }),
			"--gate-ca-cert " + initFile(etcdAccessCAFileName) + " is the etcd access CA, not the gate HTTPS CA"},
		{"gate HTTPS CA as --ca", withChange(func(config *serverCommandConfig) { config.tlsCACertPath = initFile(gateHTTPSCAFileName) }),
			"is the gate HTTPS CA, not the cluster identity CA"},
		{"cluster identity CA as --etcd-ca", withChange(func(config *serverCommandConfig) { config.etcdCACertPath = initFile(clusterIdentityCAFileName) }),
			"is the cluster identity CA, not the etcd access CA"},
		{"unreadable --etcd-ca", withChange(func(config *serverCommandConfig) { config.etcdCACertPath = initFile("missing.pem") }), "missing.pem"},
	}
	for _, testCase := range testCases {
		kindError := checkServerCAKinds(testCase.parsedConfig)
		switch {
		case testCase.wantError == "" && kindError != nil:
			t.Errorf("%s: refused: %v", testCase.name, kindError)
		case testCase.wantError != "" && (kindError == nil || !strings.Contains(kindError.Error(), testCase.wantError)):
			t.Errorf("%s: error %v, want a refusal containing %q", testCase.name, kindError, testCase.wantError)
		}
	}
}

// TestControlPlaneNodeRecordMustBeConfirmed (SA3-L1): with
// --control-plane-node-cert given, a record write that fails or is not found
// afterwards is a startup error; on an old cluster (no flag) it only warns.
func TestControlPlaneNodeRecordMustBeConfirmed(t *testing.T) {
	outputDirectory := runClusterPKITestInit(t)
	parsedConfig := serverConfigFromSetupScript(t, outputDirectory, "127.0.0.1", clusterPKITestNodeID)
	serverTLSConfig := loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath, true)
	testCases := []struct {
		name      string           // how the store misbehaves
		factStore store.StateStore // the store the server writes to
		wantError string           // the reason the startup error names
	}{
		{"transaction fails", failingTransactionStore{store.NewMemoryStore()}, "not recorded as enrolled"},
		{"transaction reports success but writes nothing", droppingTransactionStore{store.NewMemoryStore()}, "record not confirmed"},
	}
	for _, testCase := range testCases {
		t.Cleanup(func() { _ = testCase.factStore.Close() })
		startupError := startServerIdentityAndEnrollment(testCase.factStore, serverTLSConfig, parsedConfig)
		if startupError == nil || !strings.Contains(startupError.Error(), testCase.wantError) {
			t.Errorf("%s: startup error %v, want one naming %q", testCase.name, startupError, testCase.wantError)
		}
	}
	oldClusterStore := failingTransactionStore{store.NewMemoryStore()}
	t.Cleanup(func() { _ = oldClusterStore.Close() })
	legacyTLSConfig := loadServerTLSConfig(filepath.Join(outputDirectory, nodeIdentityForAPIFileName),
		filepath.Join(outputDirectory, privateKeyFileName(nodeIdentityForAPIFileName)), parsedConfig.tlsCACertPath, true)
	if fallbackError := bootstrapControlPlaneNodeEnrollment(oldClusterStore, legacyTLSConfig, nil); fallbackError != nil {
		t.Errorf("old cluster (no flag): a failed record must only warn, got %v", fallbackError)
	}
}

// ansibleDirectory is the Ansible tree the PKI-name invariant scans.
const ansibleDirectory = "../../deploy/ansible"

// clusterPKIFileNames returns every certificate, CA and key file name of both
// layouts (cca init's names), sorted.
func clusterPKIFileNames() []string {
	fileNameSet := map[string]bool{}
	for _, layout := range []clusterPKILayout{threeCAClusterPKILayout, singleCAClusterPKILayout} {
		for _, fileName := range layout.installedFileNames() {
			fileNameSet[fileName] = true
			fileNameSet[privateKeyFileName(fileName)] = true
		}
	}
	return slices.Sorted(maps.Keys(fileNameSet))
}

// TestAnsibleNamesPKIFilesOnlyThroughGroupVars (CR3-L1): outside group_vars,
// no Ansible line other than a comment names a PKI file literally; tasks,
// creates: guards and templates use cca_pki, so a playbook re-run on a cluster
// from before M280 names that cluster's files.
func TestAnsibleNamesPKIFilesOnlyThroughGroupVars(t *testing.T) {
	quotedNames := []string{}
	for _, fileName := range clusterPKIFileNames() {
		quotedNames = append(quotedNames, regexp.QuoteMeta(fileName))
	}
	literalNamePattern := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(` + strings.Join(quotedNames, "|") + `)($|[^A-Za-z0-9_.-])`)
	groupVarsPath := filepath.Clean(ansibleGroupVarsPath)
	walkError := filepath.WalkDir(ansibleDirectory, func(filePath string, entry os.DirEntry, entryError error) error {
		if entryError != nil || entry.IsDir() || filepath.Clean(filePath) == groupVarsPath {
			return entryError
		}
		fileContent, readError := os.ReadFile(filePath) //nolint:gosec // repository file
		if readError != nil {
			return readError
		}
		for lineIndex, line := range strings.Split(string(fileContent), "\n") {
			trimmedLine := strings.TrimSpace(line)
			if strings.HasPrefix(trimmedLine, "#") || strings.HasPrefix(trimmedLine, "{#") {
				continue
			}
			if match := literalNamePattern.FindStringSubmatch(line); match != nil {
				t.Errorf("%s:%d names %s literally; use cca_pki (group_vars): %s", filePath, lineIndex+1, match[2], trimmedLine)
			}
		}
		return nil
	})
	if walkError != nil {
		t.Fatal(walkError)
	}
}

// ansibleTaskField returns a string field of the named task in an Ansible task
// file (fieldPath: e.g. "set_fact", "cca_pki_layout"); fails when absent.
func ansibleTaskField(t *testing.T, taskFilePath string, taskName string, fieldPath ...string) any {
	t.Helper()
	var taskList []map[string]any
	if decodeError := yaml.Unmarshal(mustReadFile(t, taskFilePath), &taskList); decodeError != nil {
		t.Fatalf("%s: %v", taskFilePath, decodeError)
	}
	for _, task := range taskList {
		if task["name"] != taskName {
			continue
		}
		var fieldValue any = task
		for _, fieldName := range fieldPath {
			fieldMap, isMap := fieldValue.(map[string]any)
			if !isMap {
				t.Fatalf("%s: task %q has no %v", taskFilePath, taskName, fieldPath)
			}
			fieldValue = fieldMap[fieldName]
		}
		if fieldValue == nil {
			t.Fatalf("%s: task %q has no %v", taskFilePath, taskName, fieldPath)
		}
		return fieldValue
	}
	t.Fatalf("%s: no task %q", taskFilePath, taskName)
	return nil
}

// renderAnsibleText renders an Ansible template string for a layout with extra variables.
func renderAnsibleText(t *testing.T, templateText string, layoutName string, extraVariables map[string]any) string {
	t.Helper()
	templatePath := writeClusterPKITestFile(t, t.TempDir(), "inline.j2", []byte(templateText))
	return strings.TrimSpace(runAnsibleRenderWithVariables(t, "template", templatePath, layoutName, "ctrl-2", extraVariables))
}

// TestAnsiblePKILayoutDetection (CR3-L1): pki_layout.yml, rendered with the
// stat results of its own probe list, picks single_ca only when the host has
// ca.pem and lacks cluster-identity-ca.pem.
func TestAnsiblePKILayoutDetection(t *testing.T) {
	taskFilePath := filepath.Join(ansibleRolesDirectory, "common", "tasks", "pki_layout.yml")
	probeItems, _ := ansibleTaskField(t, taskFilePath, "Look for the cluster CA files on the first control-plane host", "loop").([]any)
	layoutExpression, _ := ansibleTaskField(t, taskFilePath, "Use the cluster's PKI layout", "set_fact", "cca_pki_layout").(string)
	probedFiles := []string{}
	for _, probeItem := range probeItems {
		probeText, _ := probeItem.(string)
		probedFiles = append(probedFiles, renderAnsibleText(t, probeText, "three_ca", nil))
	}
	for _, testCase := range []struct {
		installedFiles []string // the CA files present on the first control-plane host
		wantLayout     string   // the layout the play must use
	}{
		{nil, "three_ca"},
		{[]string{legacyClusterCAFileName}, "single_ca"},
		{[]string{clusterIdentityCAFileName}, "three_ca"},
		{[]string{clusterIdentityCAFileName, legacyClusterCAFileName}, "three_ca"},
	} {
		probeResults := []map[string]any{}
		for _, probedFile := range probedFiles {
			probeResults = append(probeResults, map[string]any{"item": probedFile,
				"stat": map[string]any{"exists": slices.Contains(testCase.installedFiles, probedFile)}})
		}
		renderedLayout := renderAnsibleText(t, layoutExpression, "three_ca",
			map[string]any{"cca_pki_layout_probe": map[string]any{"results": probeResults}})
		if renderedLayout != testCase.wantLayout {
			t.Errorf("host with %v: layout %q, want %q (probed %v)", testCase.installedFiles, renderedLayout, testCase.wantLayout, probedFiles)
		}
	}
}

// TestAnsibleHASecondaryInitFollowsTheLayout (CR3-L1): the HA role's cca init
// for a secondary passes the cluster CA of its layout, and the etcd access and
// gate HTTPS CAs (with keys) only on a three-CA cluster.
func TestAnsibleHASecondaryInitFollowsTheLayout(t *testing.T) {
	initCommand, _ := ansibleTaskField(t, filepath.Join(ansibleRolesDirectory, "ha-controlplane", "tasks", "main.yml"),
		"Generate per-node certificates on secondary control-plane nodes", "command").(string)
	for layoutName, layout := range ansibleLayoutNames {
		renderedFlags := map[string]string{}
		renderedFields := strings.Fields(renderAnsibleText(t, initCommand, layoutName, nil))
		for fieldIndex := 0; fieldIndex+1 < len(renderedFields); fieldIndex++ {
			if strings.HasPrefix(renderedFields[fieldIndex], "--") {
				renderedFlags[renderedFields[fieldIndex]] = renderedFields[fieldIndex+1]
			}
		}
		wantFlags := map[string]string{"--ca": layout.clusterCAFileName, "--ca-key": privateKeyFileName(layout.clusterCAFileName)}
		if !layout.isSingleCA() {
			wantFlags["--etcd-access-ca"], wantFlags["--etcd-access-ca-key"] = layout.etcdCAFileName, privateKeyFileName(layout.etcdCAFileName)
			wantFlags["--gate-https-ca"], wantFlags["--gate-https-ca-key"] = layout.gateCAFileName, privateKeyFileName(layout.gateCAFileName)
		}
		for _, caFlag := range []string{"--ca", "--ca-key", "--etcd-access-ca", "--etcd-access-ca-key", "--gate-https-ca", "--gate-https-ca-key"} {
			wantPath := ""
			if wantFlags[caFlag] != "" {
				wantPath = ansiblePKIDirectory + "/" + wantFlags[caFlag]
			}
			if renderedFlags[caFlag] != wantPath {
				t.Errorf("%s: cca init %s = %q, want %q", layoutName, caFlag, renderedFlags[caFlag], wantPath)
			}
		}
	}
}
