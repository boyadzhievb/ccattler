// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Cluster PKI file names (M280 D1a): the one place cca init, setup.sh, cca join
// and the CLI defaults take them from. Each key is the certificate's name with -key.pem.
package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
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
	// etcdPeerCertificateFileName is etcd's peer certificate for HA peers (ServerAuth and ClientAuth, 1a).
	etcdPeerCertificateFileName = "etcd-peer-certificate.pem"
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
const ccaServerEtcdClientCommonName = "etcd-client-" + security.CCAServerEtcdClientName

// privateKeyFileName returns the key file of a certificate or CA file:
// the same name with -key.pem.
func privateKeyFileName(certificateFileName string) string {
	return strings.TrimSuffix(certificateFileName, ".pem") + "-key.pem"
}

// installedPKIPath returns the path of a PKI file under /etc/ccattler/pki.
func installedPKIPath(fileName string) string {
	return filepath.Join(clusterPKIDirectory, fileName)
}

// Descriptions of the three CAs, written before their PEM blocks by cca init and cca join (D1a).
const (
	// clusterIdentityCADescription describes cluster-identity-ca.pem.
	clusterIdentityCADescription = "cluster identity CA: signs the API server and node identities; the API trusts only it"
	// etcdAccessCADescription describes etcd-access-ca.pem.
	etcdAccessCADescription = "etcd access CA: signs etcd's serving and peer certificates and the etcd client certificates; etcd trusts only it"
	// gateHTTPSCADescription describes gate-https-ca.pem.
	gateHTTPSCADescription = "gate HTTPS CA: signs gate serving certificates; clients of TLS gates trust it, the API and etcd do not"
	// singleCADescription describes ca.pem of a cluster from before M280 (HA secondary, CR-L7).
	singleCADescription = "single cluster CA of a cluster from before M280: the API and etcd trust it, and it signs gates"
)

// CommonName prefixes of etcd's own certificates; the node etcd client prefix is security.NodeEtcdClientCommonNamePrefix.
const (
	// etcdServingCommonNamePrefix starts the CN of etcd's serving certificate, etcd-server-<node>
	// (etcd-<node> during apply; disjoint prefixes since SA2-L4).
	etcdServingCommonNamePrefix = "etcd-server-"
	// etcdPeerCommonNamePrefix starts the CN of etcd's peer certificate, etcd-peer-<node>.
	etcdPeerCommonNamePrefix = "etcd-peer-"
)

// legacyEtcdCertificateFileName is etcd's serving and peer certificate on a cluster from before M280.
const legacyEtcdCertificateFileName = "etcd.pem"

// clusterPKILayout names, per role, the file cca init writes and setup.sh's
// units use: the three-CA layout of D1a, or a single-CA cluster's older one.
type clusterPKILayout struct {
	clusterCAFileName        string // CA the API trusts; copied to /etc/ccattler for the CLI
	etcdCAFileName           string // CA etcd trusts for clients and peers
	gateCAFileName           string // gate HTTPS CA; "" on a single-CA cluster (no gate flags)
	apiServerFileName        string // cca server --cert
	nodeIdentityFileName     string // cca agent --cert
	etcdServingFileName      string // etcd's serving certificate
	etcdPeerFileName         string // etcd's peer certificate (HA)
	serverEtcdClientFileName string // cca server --etcd-cert
	nodeEtcdClientFileName   string // cca agent --etcd-cert
}

// threeCAClusterPKILayout is the layout of a cluster created by cca init from M280 on.
var threeCAClusterPKILayout = clusterPKILayout{clusterIdentityCAFileName, etcdAccessCAFileName, gateHTTPSCAFileName,
	apiServerIdentityCertificateFileName, nodeIdentityForAPIFileName, etcdServingCertificateFileName,
	etcdPeerCertificateFileName, ccaServerEtcdClientCertificateFileName, nodeEtcdClientCertificateFileName}

// singleCAClusterPKILayout is the older layout an HA secondary of a single-CA cluster keeps (CR-L7).
var singleCAClusterPKILayout = clusterPKILayout{legacyClusterCAFileName, legacyClusterCAFileName, "",
	legacyNodeCertificateFileName, legacyNodeCertificateFileName, legacyEtcdCertificateFileName,
	legacyEtcdCertificateFileName, legacyNodeCertificateFileName, legacyNodeCertificateFileName}

// isSingleCA reports whether the layout has no separate etcd and gate CAs.
func (layout clusterPKILayout) isSingleCA() bool {
	return layout.gateCAFileName == ""
}

// installedFileNames returns the certificates and CAs setup.sh installs, each
// once and in a fixed order; each one's key is installed next to it.
func (layout clusterPKILayout) installedFileNames() []string {
	var fileNames []string
	for _, fileName := range []string{layout.clusterCAFileName, layout.etcdCAFileName, layout.gateCAFileName,
		layout.apiServerFileName, layout.nodeIdentityFileName, layout.etcdServingFileName, layout.etcdPeerFileName,
		layout.serverEtcdClientFileName, layout.nodeEtcdClientFileName} {
		if fileName != "" && !slices.Contains(fileNames, fileName) {
			fileNames = append(fileNames, fileName)
		}
	}
	return fileNames
}

// buildSetupScriptPKIInstallStep returns the setup.sh lines that install every
// certificate of the layout at 0644 and every key at 0600, owned by root, and
// copy the cluster CA to /etc/ccattler for the CLI.
func buildSetupScriptPKIInstallStep(layout clusterPKILayout) string {
	var stepBuilder strings.Builder
	stepBuilder.WriteString("echo '==> Installing PKI certificates'\n")
	for _, certificateFileName := range layout.installedFileNames() {
		fmt.Fprintf(&stepBuilder, "install -o root -g root -m 644 \"$SOURCE_DIR/%s\" \"$PKI_DIR/%s\"\n",
			certificateFileName, certificateFileName)
		keyFileName := privateKeyFileName(certificateFileName)
		fmt.Fprintf(&stepBuilder, "install -o root -g root -m 600 \"$SOURCE_DIR/%s\" \"$PKI_DIR/%s\"\n", keyFileName, keyFileName)
	}
	fmt.Fprintf(&stepBuilder, "install -o root -g root -m 644 \"$SOURCE_DIR/%s\" %s/%s\n",
		layout.clusterCAFileName, filepath.Dir(clusterPKIDirectory), layout.clusterCAFileName)
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

// setupScriptServerIdentityFlags returns cca server's API flags in setup.sh:
// its certificate and the cluster CA, and the gate HTTPS CA on a three-CA layout.
func setupScriptServerIdentityFlags(layout clusterPKILayout) []setupScriptPKIFlag {
	serverFlags := []setupScriptPKIFlag{
		{"cert", layout.apiServerFileName}, {"key", privateKeyFileName(layout.apiServerFileName)},
		{"ca", layout.clusterCAFileName}, {"ca-key", privateKeyFileName(layout.clusterCAFileName)},
	}
	if layout.isSingleCA() {
		return serverFlags
	}
	return append(serverFlags,
		setupScriptPKIFlag{"gate-ca-cert", layout.gateCAFileName}, setupScriptPKIFlag{"gate-ca-key", privateKeyFileName(layout.gateCAFileName)})
}

// setupScriptServerEtcdFlags returns cca server's etcd client flags in setup.sh;
// a three-CA layout adds the etcd access CA key for enrollment (D4) and this
// host's node identity, which the server records as enrolled (SA-H1).
func setupScriptServerEtcdFlags(layout clusterPKILayout) []setupScriptPKIFlag {
	etcdFlags := []setupScriptPKIFlag{
		{"etcd-cert", layout.serverEtcdClientFileName}, {"etcd-key", privateKeyFileName(layout.serverEtcdClientFileName)},
	}
	if layout.isSingleCA() {
		return etcdFlags
	}
	return append(etcdFlags, setupScriptPKIFlag{"etcd-access-ca-key", privateKeyFileName(layout.etcdCAFileName)},
		setupScriptPKIFlag{"control-plane-node-cert", layout.nodeIdentityFileName})
}
