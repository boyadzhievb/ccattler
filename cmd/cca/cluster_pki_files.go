// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Cluster PKI file names (M280 D1a): the one place cca init, setup.sh, cca join
// and the CLI defaults take them from. Each key is the certificate's name with -key.pem.
package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// clusterPKIDirectory is where setup.sh and cca join install the cluster PKI.
const clusterPKIDirectory = "/etc/ccattler/pki"

// File names of a cluster created by cca init from M280 on (design.md D1a).
const (
	// clusterIdentityCAFileName is the CA of the API server and node identities; the API trusts only it.
	clusterIdentityCAFileName = "cluster-identity-ca.pem"
	// etcdAccessCAFileName is the CA etcd trusts; it signs etcd's serving and client certificates.
	etcdAccessCAFileName = "etcd-access-ca.pem"
	// gateHTTPSCAFileName is the CA of gate serving certificates; clients of TLS gates trust it.
	gateHTTPSCAFileName = "gate-https-ca.pem"
	// apiServerIdentityCertificateFileName is cca server's API certificate (CN ccattler-server).
	apiServerIdentityCertificateFileName = "api-server-identity-certificate.pem"
	// nodeIdentityForAPIFileName is a node's client identity to the API (CN = node ID).
	nodeIdentityForAPIFileName = "node-identity-for-api.pem"
	// etcdServingCertificateFileName is etcd's own serving certificate.
	etcdServingCertificateFileName = "etcd-serving-certificate.pem"
	// ccaServerEtcdClientCertificateFileName is cca server's etcd client certificate.
	ccaServerEtcdClientCertificateFileName = "cca-server-etcd-client-certificate.pem"
	// nodeEtcdClientCertificateFileName is one node's etcd client certificate, used by its agent.
	nodeEtcdClientCertificateFileName = "node-etcd-client-certificate.pem"
)

// File names of clusters created before M280, which keep them (no rename).
const (
	// legacyClusterCAFileName is the single CA of an older cluster.
	legacyClusterCAFileName = "ca.pem"
	// legacyNodeCertificateFileName is an older node's certificate, used for the API and etcd.
	legacyNodeCertificateFileName = "node.pem"
)

// ccaServerEtcdClientCommonName is the CommonName of cca server's etcd client
// certificate. A distinct identity, not a separately authorized one: until
// M181 (store access isolation, etcd roles) every accepted etcd client certificate has full etcd access.
const ccaServerEtcdClientCommonName = "etcd-client-cca-server"

// privateKeyFileName returns the key file of a certificate or CA file:
// the same name with -key.pem.
func privateKeyFileName(certificateFileName string) string {
	return strings.TrimSuffix(certificateFileName, ".pem") + "-key.pem"
}

// installedPKIPath returns the path of a PKI file under /etc/ccattler/pki.
func installedPKIPath(fileName string) string {
	return filepath.Join(clusterPKIDirectory, fileName)
}

// controlPlaneCertificateFileNames are the certificates and CAs setup.sh
// installs on a cca init host; each one's key is installed next to it.
var controlPlaneCertificateFileNames = []string{
	clusterIdentityCAFileName, etcdAccessCAFileName, gateHTTPSCAFileName,
	apiServerIdentityCertificateFileName, nodeIdentityForAPIFileName, etcdServingCertificateFileName,
	ccaServerEtcdClientCertificateFileName, nodeEtcdClientCertificateFileName,
}

// buildSetupScriptPKIInstallStep returns the setup.sh lines that install every
// certificate at 0644 and every key at 0600, owned by root, and copy the cluster
// identity CA to /etc/ccattler for the CLI.
func buildSetupScriptPKIInstallStep() string {
	var stepBuilder strings.Builder
	stepBuilder.WriteString("echo '==> Installing PKI certificates'\n")
	for _, certificateFileName := range controlPlaneCertificateFileNames {
		fmt.Fprintf(&stepBuilder, "install -o root -g root -m 644 \"$SOURCE_DIR/%s\" \"$PKI_DIR/%s\"\n",
			certificateFileName, certificateFileName)
		keyFileName := privateKeyFileName(certificateFileName)
		fmt.Fprintf(&stepBuilder, "install -o root -g root -m 600 \"$SOURCE_DIR/%s\" \"$PKI_DIR/%s\"\n", keyFileName, keyFileName)
	}
	fmt.Fprintf(&stepBuilder, "install -o root -g root -m 644 \"$SOURCE_DIR/%s\" /etc/ccattler/%s\n",
		clusterIdentityCAFileName, clusterIdentityCAFileName)
	return stepBuilder.String()
}

// setupScriptPKIFlag is one systemd unit flag whose value is an installed PKI file.
type setupScriptPKIFlag struct {
	flagName string // flag without the leading dashes
	fileName string // file under /etc/ccattler/pki
}

// writeSetupScriptPKIFlags writes one continued ExecStart line per flag.
func writeSetupScriptPKIFlags(scriptBuilder *strings.Builder, pkiFlags []setupScriptPKIFlag) {
	for _, pkiFlag := range pkiFlags {
		fmt.Fprintf(scriptBuilder, "  --%s %s \\\n", pkiFlag.flagName, installedPKIPath(pkiFlag.fileName))
	}
}

// describedPEM prefixes PEM data with the one-line "# CCattler ..." comment that
// says what it is (D1a). pem.Decode and openssl skip text before -----BEGIN.
func describedPEM(description string, pemData []byte) []byte {
	return append([]byte("# CCattler "+description+"\n"), pemData...)
}

// installedPKIFileSet is one certificate, its key and the CA that verifies the
// peer, as installed paths: the defaults a CLI command or the agent falls back to.
type installedPKIFileSet struct {
	certificatePath   string // client or identity certificate
	privateKeyPath    string // its private key
	caCertificatePath string // CA of the peer it connects to
}

// installedPKIFileSetFor returns the installed paths of a certificate, its key and a CA.
func installedPKIFileSetFor(certificateFileName string, caFileName string) installedPKIFileSet {
	return installedPKIFileSet{installedPKIPath(certificateFileName),
		installedPKIPath(privateKeyFileName(certificateFileName)), installedPKIPath(caFileName)}
}

// installedEtcdClientFileSets are the etcd client defaults, M280 names first,
// then node.pem with ca.pem (clusters from before M280, which keep their files).
var installedEtcdClientFileSets = []installedPKIFileSet{
	installedPKIFileSetFor(nodeEtcdClientCertificateFileName, etcdAccessCAFileName),
	installedPKIFileSetFor(legacyNodeCertificateFileName, legacyClusterCAFileName),
}

// installedNodeIdentityFileSets are the agent's API identity defaults, M280
// names first, then the older node.pem with ca.pem.
var installedNodeIdentityFileSets = []installedPKIFileSet{
	installedPKIFileSetFor(nodeIdentityForAPIFileName, clusterIdentityCAFileName),
	installedPKIFileSetFor(legacyNodeCertificateFileName, legacyClusterCAFileName),
}

// installedClusterCACandidatePaths are where the CLI and cca join look for the
// CA that signed the API server: setup.sh's copy in /etc/ccattler, then the PKI directory; M280 names first.
var installedClusterCACandidatePaths = []string{
	filepath.Join(filepath.Dir(clusterPKIDirectory), clusterIdentityCAFileName), installedPKIPath(clusterIdentityCAFileName),
	filepath.Join(filepath.Dir(clusterPKIDirectory), legacyClusterCAFileName), installedPKIPath(legacyClusterCAFileName),
}

// findInstalledPKIFileSet returns the first set whose three files all exist.
func findInstalledPKIFileSet(candidateSets []installedPKIFileSet) (installedPKIFileSet, bool) {
	for _, candidateSet := range candidateSets {
		if fileExists(candidateSet.certificatePath) && fileExists(candidateSet.privateKeyPath) &&
			fileExists(candidateSet.caCertificatePath) {
			return candidateSet, true
		}
	}
	return installedPKIFileSet{}, false
}

// firstExistingPath returns the first path that exists, or "".
func firstExistingPath(candidatePaths []string) string {
	for _, candidatePath := range candidatePaths {
		if fileExists(candidatePath) {
			return candidatePath
		}
	}
	return ""
}
