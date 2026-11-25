// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJoinWritesEtcdClientCertificate: against a server with an etcd access CA,
// cca join writes the node's API identity and its own etcd client certificate
// under the D1a names, each with a description line, and the agent command it
// prints connects to etcd with the etcd client certificate (D4).
func TestJoinWritesEtcdClientCertificate(t *testing.T) {
	dataDirectory := t.TempDir()
	credentialPaths := writeEnrollmentCredentials(dataDirectory, enrollmentResponsePayload{
		CertificatePEM:     "-----BEGIN CERTIFICATE-----\nbm9kZQ==\n-----END CERTIFICATE-----\n",
		CACertPEM:          "-----BEGIN CERTIFICATE-----\nY2x1c3Rlcg==\n-----END CERTIFICATE-----\n",
		EtcdCertificatePEM: "-----BEGIN CERTIFICATE-----\nZXRjZA==\n-----END CERTIFICATE-----\n",
		EtcdCACertPEM:      "-----BEGIN CERTIFICATE-----\nZXRjZC1jYQ==\n-----END CERTIFICATE-----\n",
	}, "-----BEGIN EC PRIVATE KEY-----\nbm9kZWtleQ==\n-----END EC PRIVATE KEY-----\n",
		"-----BEGIN EC PRIVATE KEY-----\nZXRjZGtleQ==\n-----END EC PRIVATE KEY-----\n")
	for fileName, wantBody := range map[string]string{
		"node-identity-for-api.pem":            "bm9kZQ==",
		"node-identity-for-api-key.pem":        "bm9kZWtleQ==",
		"cluster-identity-ca.pem":              "Y2x1c3Rlcg==",
		"node-etcd-client-certificate.pem":     "ZXRjZA==",
		"node-etcd-client-certificate-key.pem": "ZXRjZGtleQ==",
		"etcd-access-ca.pem":                   "ZXRjZC1jYQ==",
	} {
		fileContent, readError := os.ReadFile(filepath.Join(dataDirectory, fileName)) //nolint:gosec // test temp directory
		if readError != nil {
			t.Errorf("cca join did not write %s: %v", fileName, readError)
			continue
		}
		if !strings.HasPrefix(string(fileContent), "# CCattler ") || !strings.Contains(string(fileContent), wantBody) {
			t.Errorf("%s: no description line or wrong content:\n%s", fileName, fileContent)
		}
	}
	agentCommand := buildJoinedAgentStartCommand("worker-1", credentialPaths)
	for _, wantFlag := range []string{
		"--cert " + filepath.Join(dataDirectory, "node-identity-for-api.pem"),
		"--etcd-cert " + filepath.Join(dataDirectory, "node-etcd-client-certificate.pem"),
		"--etcd-key " + filepath.Join(dataDirectory, "node-etcd-client-certificate-key.pem"),
		"--etcd-ca " + filepath.Join(dataDirectory, "etcd-access-ca.pem"),
	} {
		if !strings.Contains(agentCommand, wantFlag) {
			t.Errorf("printed agent command lacks %q: %s", wantFlag, agentCommand)
		}
	}
}

// TestJoinAgainstOldServerKeepsLegacyFiles: a server without an etcd access CA
// returns no etcd material; cca join keeps the old file names and etcd use (D4).
func TestJoinAgainstOldServerKeepsLegacyFiles(t *testing.T) {
	dataDirectory := t.TempDir()
	credentialPaths := writeEnrollmentCredentials(dataDirectory, enrollmentResponsePayload{
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nbm9kZQ==\n-----END CERTIFICATE-----\n",
		CACertPEM:      "-----BEGIN CERTIFICATE-----\nY2x1c3Rlcg==\n-----END CERTIFICATE-----\n",
	}, "-----BEGIN EC PRIVATE KEY-----\nbm9kZWtleQ==\n-----END EC PRIVATE KEY-----\n", "")
	for _, fileName := range []string{"node.pem", "node-key.pem", "ca.pem"} {
		if _, statError := os.Stat(filepath.Join(dataDirectory, fileName)); statError != nil {
			t.Errorf("legacy join did not write %s: %v", fileName, statError)
		}
	}
	if _, statError := os.Stat(filepath.Join(dataDirectory, "node-etcd-client-certificate.pem")); statError == nil {
		t.Error("legacy join wrote an etcd client certificate")
	}
	if agentCommand := buildJoinedAgentStartCommand("worker-1", credentialPaths); strings.Contains(agentCommand, "--etcd-cert") {
		t.Errorf("legacy agent command names an etcd client certificate: %s", agentCommand)
	}
}

// TestServerWarnsOnSingleCACluster: a server whose etcd CA is its cluster CA, or
// that has no gate CA, warns that one CA serves etcd, the API and gates (D6); a
// three-CA server does not.
func TestServerWarnsOnSingleCACluster(t *testing.T) {
	pkiDirectory := t.TempDir()
	clusterCAPath := writeClusterPKITestFile(t, pkiDirectory, "ca.pem", []byte("cluster CA"))
	copiedCAPath := writeClusterPKITestFile(t, pkiDirectory, "etcd-copy.pem", []byte("cluster CA"))
	etcdCAPath := writeClusterPKITestFile(t, pkiDirectory, "etcd-access-ca.pem", []byte("etcd CA"))
	testCases := map[string]struct {
		parsedConfig serverCommandConfig // flags of the server start
		wantWarning  bool                // whether the single-CA warning is due
	}{
		"legacy: etcd trusts ca.pem, no gate CA": {serverCommandConfig{tlsCACertPath: clusterCAPath, etcdCACertPath: clusterCAPath}, true},
		"etcd CA is a copy of the cluster CA": {serverCommandConfig{tlsCACertPath: clusterCAPath, etcdCACertPath: copiedCAPath,
			gateCACertPath: etcdCAPath, gateCAKeyPath: etcdCAPath}, true},
		"separate etcd CA but no gate CA": {serverCommandConfig{tlsCACertPath: clusterCAPath, etcdCACertPath: etcdCAPath}, true},
		"three CAs": {serverCommandConfig{tlsCACertPath: clusterCAPath, etcdCACertPath: etcdCAPath,
			gateCACertPath: etcdCAPath, gateCAKeyPath: etcdCAPath}, false},
		"no cluster CA files (dev --tls or plain)": {serverCommandConfig{}, false},
	}
	for testName, testCase := range testCases {
		warning := singleCAClusterWarning(testCase.parsedConfig)
		if (warning != "") != testCase.wantWarning {
			t.Errorf("%s: warning %q, want warning %v", testName, warning, testCase.wantWarning)
		}
		if warning != "" && (!strings.Contains(warning, "re-initialize") || !strings.Contains(warning, "cca init")) {
			t.Errorf("%s: warning does not say to re-initialize with cca init: %q", testName, warning)
		}
	}
}
