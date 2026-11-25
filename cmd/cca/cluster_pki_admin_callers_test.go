// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// automationRootDirectories hold the scripts and playbooks that act on a
// cluster: the e2e and deploy scripts and the Ansible roles and playbooks.
var automationRootDirectories = []string{"../../test/e2e", "../../deploy/ansible"}

// apiClientCredentialNames are the API client certificates and keys automation
// must not present as an admin client (SA2-L2, CR2-L2): the node identity, the
// API server's own certificate (cluster-admin by design), the old node.pem, and
// the cca_pki variables that name them.
var apiClientCredentialNames = []string{
	nodeIdentityForAPIFileName, privateKeyFileName(nodeIdentityForAPIFileName),
	apiServerIdentityCertificateFileName, privateKeyFileName(apiServerIdentityCertificateFileName),
	legacyNodeCertificateFileName, privateKeyFileName(legacyNodeCertificateFileName),
	"cca_pki.node_cert", "cca_pki.node_key", "cca_pki.server_cert", "cca_pki.server_key",
}

// credentialAllowedLineMarkers mark lines that name a credential without using
// it as an API client: Ansible's creates: guards, and an etcd client file
// chosen by layout (node.pem is the etcd client on clusters from before M280).
var credentialAllowedLineMarkers = []string{"creates:", "node-etcd-client-certificate"}

// credentialAllowedFileNames are the files that name the credentials on purpose:
// the agent and server units present them as their own identity, and group_vars
// holds the layout table.
var credentialAllowedFileNames = map[string]bool{"cca-agent.service.j2": true, "cca-server.service.j2": true, "all.yml": true}

// TestAutomationNeverUsesCertificateAsAdminClient (M280 CR-H1, SA2-L2,
// CR2-L2): no script or playbook presents the node identity, the API server's
// certificate or the old node.pem as an API client; they use sudo cca with
// the local token. Comment lines are skipped.
func TestAutomationNeverUsesCertificateAsAdminClient(t *testing.T) {
	for _, rootDirectory := range automationRootDirectories {
		walkError := filepath.WalkDir(rootDirectory, func(filePath string, entry fs.DirEntry, walkError error) error {
			if walkError != nil || entry.IsDir() || credentialAllowedFileNames[filepath.Base(filePath)] {
				return walkError
			}
			for lineIndex, lineText := range strings.Split(string(mustReadFile(t, filePath)), "\n") {
				if strings.HasPrefix(strings.TrimSpace(lineText), "#") || lineHasAnyMarker(lineText, credentialAllowedLineMarkers) {
					continue
				}
				if lineHasAnyMarker(lineText, apiClientCredentialNames) {
					t.Errorf("%s:%d names an API client credential: %s", filePath, lineIndex+1, strings.TrimSpace(lineText))
				}
			}
			return nil
		})
		if walkError != nil {
			t.Fatal(walkError)
		}
	}
}

// lineHasAnyMarker reports whether lineText contains one of the markers.
func lineHasAnyMarker(lineText string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lineText, marker) {
			return true
		}
	}
	return false
}

// TestLocalAdminCurlSendsTokenOnStdinOnly (M280 CR-H1): the e2e helper's
// remote curl authenticates with the local token, which reaches curl on stdin
// and appears in no argv (sudo's or curl's); it trusts the first installed
// cluster CA, so a cluster with only the old ca.pem keeps working.
func TestLocalAdminCurlSendsTokenOnStdinOnly(t *testing.T) {
	bashPath, lookError := exec.LookPath("bash")
	if lookError != nil {
		t.Skip("bash not installed")
	}
	workDirectory := t.TempDir()
	stubDirectory := filepath.Join(workDirectory, "bin")
	if mkdirError := os.Mkdir(stubDirectory, 0o700); mkdirError != nil {
		t.Fatal(mkdirError)
	}
	const localTokenValue = "0123456789abcdef-local-token"
	writeStubExecutable(t, filepath.Join(stubDirectory, "sudo"), "#!/bin/sh\necho \"$*\" >> \"$STUB_LOG_DIR/argv\"\nexec \"$@\"\n")
	writeStubExecutable(t, filepath.Join(stubDirectory, "curl"),
		"#!/bin/sh\necho \"$*\" >> \"$STUB_LOG_DIR/argv\"\ncat > \"$STUB_LOG_DIR/curl-stdin\"\n")
	writeTestFile(t, filepath.Join(workDirectory, "local-token"), []byte(localTokenValue))
	writeTestFile(t, filepath.Join(workDirectory, "ca.pem"), []byte("old cluster CA"))

	helperScript := `source ../../test/e2e/local_admin_access.sh
remote_command=$(local_admin_curl_remote_command -sf -H "Accept: application/json" "https://10.0.0.1:9770/api/state?prefix=desired/route/")
PATH="$STUB_DIR:$PATH" sh -c "$remote_command"`
	helperCommand := exec.Command(bashPath, "-c", helperScript) //nolint:gosec // test runs the repository's own helper
	helperCommand.Env = append(os.Environ(), "STUB_DIR="+stubDirectory, "STUB_LOG_DIR="+workDirectory,
		"LOCAL_ADMIN_TOKEN_FILE="+filepath.Join(workDirectory, "local-token"),
		"LOCAL_ADMIN_CA_CANDIDATES="+filepath.Join(workDirectory, "cluster-identity-ca.pem")+" "+filepath.Join(workDirectory, "ca.pem"))
	helperOutput, runError := helperCommand.CombinedOutput()
	if runError != nil {
		t.Fatalf("helper failed: %v: %s", runError, helperOutput)
	}
	if strings.Contains(string(helperOutput), localTokenValue) {
		t.Errorf("the local token was printed: %s", helperOutput)
	}
	recordedArgv := string(mustReadFile(t, filepath.Join(workDirectory, "argv")))
	if strings.Contains(recordedArgv, localTokenValue) {
		t.Errorf("the local token is in an argv: %s", recordedArgv)
	}
	for _, wantArgument := range []string{"-H @-", "--cacert " + filepath.Join(workDirectory, "ca.pem"), "https://10.0.0.1:9770/api/state?prefix=desired/route/"} {
		if !strings.Contains(recordedArgv, wantArgument) {
			t.Errorf("curl argv lacks %q: %s", wantArgument, recordedArgv)
		}
	}
	if curlStdin := string(mustReadFile(t, filepath.Join(workDirectory, "curl-stdin"))); curlStdin != "Authorization: Bearer "+localTokenValue+"\n" {
		t.Errorf("curl stdin %q, want the bearer header with the local token", curlStdin)
	}
}

// writeStubExecutable writes a stub shell command the helper runs instead of
// the real sudo or curl, and makes it executable.
func writeStubExecutable(t *testing.T, filePath string, scriptContent string) {
	t.Helper()
	writeTestFile(t, filePath, []byte(scriptContent))
	if chmodError := os.Chmod(filePath, 0o700); chmodError != nil { //nolint:gosec // test stub must be executable
		t.Fatal(chmodError)
	}
}
