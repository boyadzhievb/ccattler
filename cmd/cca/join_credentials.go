// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Files cca join writes from an enrollment response (M280 D4): the D1a names
// against a server with an etcd access CA, the older names otherwise.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// joinedCredentialPaths are the files cca join wrote; the etcd paths are empty
// when the server issued no etcd client certificate (a cluster from before M280).
type joinedCredentialPaths struct {
	nodeCertificatePath string // the node's identity to the API
	nodeKeyPath         string // its private key
	clusterCAPath       string // the CA the API server's certificate chains to
	etcdCertificatePath string // the node's own etcd client certificate, or ""
	etcdKeyPath         string // its private key, or ""
	etcdCAPath          string // the etcd access CA, or ""
}

// joinedCredentialFile is one file cca join writes: its name, content and the
// description line it starts with ("" for the older layout, which has none).
type joinedCredentialFile struct {
	fileName    string // name in the data directory
	content     string // PEM content from the response or the local key
	description string // D1a description, without "# CCattler "
}

// joinedCredentialFileMode is the mode of every file cca join writes, as before M280.
const joinedCredentialFileMode = 0o600

// writeEnrollmentCredentials writes the enrollment response and the local keys
// to dataDirectory and prints the paths. With an etcd client certificate it
// uses the D1a names and description lines; without, today's node.pem layout.
func writeEnrollmentCredentials(dataDirectory string, enrollmentResponse enrollmentResponsePayload,
	nodePrivateKeyPEM string, etcdPrivateKeyPEM string) joinedCredentialPaths {
	if mkdirError := os.MkdirAll(dataDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating data directory: %v\n", mkdirError)
		os.Exit(1)
	}
	credentialFiles, credentialPaths := planJoinedCredentialFiles(dataDirectory, enrollmentResponse, nodePrivateKeyPEM, etcdPrivateKeyPEM)
	fmt.Println()
	for _, credentialFile := range credentialFiles {
		fileContent := credentialFile.content
		if credentialFile.description != "" {
			fileContent = string(describedPEM(credentialFile.description, []byte(fileContent)))
		}
		filePath := filepath.Join(dataDirectory, credentialFile.fileName)
		if writeError := os.WriteFile(filePath, []byte(fileContent), joinedCredentialFileMode); writeError != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", filePath, writeError)
			os.Exit(1)
		}
		fmt.Printf("  Wrote %s\n", filePath)
	}
	return credentialPaths
}

// planJoinedCredentialFiles returns the files to write and their paths: the D1a
// names when the response carries an etcd client certificate, else node.pem,
// node-key.pem and ca.pem, the layout of clusters from before M280.
func planJoinedCredentialFiles(dataDirectory string, enrollmentResponse enrollmentResponsePayload,
	nodePrivateKeyPEM string, etcdPrivateKeyPEM string) ([]joinedCredentialFile, joinedCredentialPaths) {
	inDataDirectory := func(fileName string) string { return filepath.Join(dataDirectory, fileName) }
	if enrollmentResponse.EtcdCertificatePEM == "" {
		legacyKeyFileName := privateKeyFileName(legacyNodeCertificateFileName)
		return []joinedCredentialFile{
				{legacyNodeCertificateFileName, enrollmentResponse.CertificatePEM, ""},
				{legacyKeyFileName, nodePrivateKeyPEM, ""},
				{legacyClusterCAFileName, enrollmentResponse.CACertPEM, ""},
			}, joinedCredentialPaths{nodeCertificatePath: inDataDirectory(legacyNodeCertificateFileName),
				nodeKeyPath: inDataDirectory(legacyKeyFileName), clusterCAPath: inDataDirectory(legacyClusterCAFileName)}
	}
	nodeIdentityDescription := "node identity for API, signed by " + clusterIdentityCAFileName +
		", presented by cca agent to the API (written by cca join)"
	etcdClientDescription := "node etcd client, signed by " + etcdAccessCAFileName +
		", used by cca agent to connect to etcd (written by cca join)"
	return []joinedCredentialFile{
			{nodeIdentityForAPIFileName, enrollmentResponse.CertificatePEM, nodeIdentityDescription},
			{privateKeyFileName(nodeIdentityForAPIFileName), nodePrivateKeyPEM, "private key of " + nodeIdentityForAPIFileName},
			{clusterIdentityCAFileName, enrollmentResponse.CACertPEM, clusterIdentityCADescription},
			{nodeEtcdClientCertificateFileName, enrollmentResponse.EtcdCertificatePEM, etcdClientDescription},
			{privateKeyFileName(nodeEtcdClientCertificateFileName), etcdPrivateKeyPEM, "private key of " + nodeEtcdClientCertificateFileName},
			{etcdAccessCAFileName, enrollmentResponse.EtcdCACertPEM, etcdAccessCADescription},
		}, joinedCredentialPaths{
			nodeCertificatePath: inDataDirectory(nodeIdentityForAPIFileName),
			nodeKeyPath:         inDataDirectory(privateKeyFileName(nodeIdentityForAPIFileName)),
			clusterCAPath:       inDataDirectory(clusterIdentityCAFileName),
			etcdCertificatePath: inDataDirectory(nodeEtcdClientCertificateFileName),
			etcdKeyPath:         inDataDirectory(privateKeyFileName(nodeEtcdClientCertificateFileName)),
			etcdCAPath:          inDataDirectory(etcdAccessCAFileName),
		}
}

// buildJoinedAgentStartCommand returns the cca agent command cca join prints:
// the node identity flags, and the etcd client flags when an etcd client
// certificate was issued (without them the agent uses the node certificate, as before M280).
func buildJoinedAgentStartCommand(nodeID string, credentialPaths joinedCredentialPaths) string {
	commandParts := []string{"cca agent --node-id " + nodeID, "--cert " + credentialPaths.nodeCertificatePath,
		"--key " + credentialPaths.nodeKeyPath, "--ca " + credentialPaths.clusterCAPath}
	if credentialPaths.etcdCertificatePath != "" {
		commandParts = append(commandParts, "--etcd-cert "+credentialPaths.etcdCertificatePath,
			"--etcd-key "+credentialPaths.etcdKeyPath, "--etcd-ca "+credentialPaths.etcdCAPath)
	}
	return strings.Join(commandParts, " ")
}
