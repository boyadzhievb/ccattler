// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Init command: generates PKI and produces a setup script for the node.
package main

import (
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/security"
)

// defaultCACertificateValidityInit is the CA certificate lifetime used by
// cca init. Matches the server's default.
const defaultCACertificateValidityInit = 10 * 365 * 24 * time.Hour

// defaultNodeCertificateValidityInit is the node certificate lifetime.
const defaultNodeCertificateValidityInit = 365 * 24 * time.Hour

// initCommandConfig holds parsed flags for the "init" command.
type initCommandConfig struct {
	// outputDirectory is where the generated PKI and setup script are written.
	outputDirectory string
	// advertiseAddress is the IP address for the node certificate SAN.
	advertiseAddress string
	// nodeID is the common name for the node certificate.
	nodeID string
	// extraSANs holds additional DNS names or IP addresses for the certificate.
	extraSANs []string
	// caValidity is the CA certificate lifetime.
	caValidity time.Duration
	// certValidity is the node certificate lifetime.
	certValidity time.Duration
	// clusterCIDR overrides the default overlay network CIDR for container IPs.
	clusterCIDR string
	// vipCIDR overrides the default service virtual IP CIDR.
	vipCIDR string
	// existingCAPath points to an existing CA certificate to reuse (HA setup).
	existingCAPath string
	// existingCAKeyPath points to the corresponding CA private key (HA setup).
	existingCAKeyPath string
	// existingEtcdAccessCAPath points to the cluster's etcd access CA certificate (HA setup).
	existingEtcdAccessCAPath string
	// existingEtcdAccessCAKeyPath points to the etcd access CA private key (HA setup).
	existingEtcdAccessCAKeyPath string
	// existingGateHTTPSCAPath points to the cluster's gate HTTPS CA certificate (HA setup).
	existingGateHTTPSCAPath string
	// existingGateHTTPSCAKeyPath points to the gate HTTPS CA private key (HA setup).
	existingGateHTTPSCAKeyPath string
	// etcdClientNetworks are the networks setup.sh opens the etcd client port to (D5).
	etcdClientNetworks []*net.IPNet
}

// parseInitCommandArgs parses the flags following "init". An unknown flag, a
// flag without its value, a malformed duration and a positional argument are
// errors (M227).
func parseInitCommandArgs(args []string) (initCommandConfig, error) {
	parsedConfig := initCommandConfig{
		caValidity:   defaultCACertificateValidityInit,
		certValidity: defaultNodeCertificateValidityInit,
	}

	flagSet := newDaemonFlagSet("init")
	flagSet.StringVar(&parsedConfig.advertiseAddress, "advertise-address", "", "IP address for the node certificate (default: detected)")
	flagSet.StringVar(&parsedConfig.nodeID, "node-id", "", "node name for the certificate (default: hostname)")
	flagSet.StringVar(&parsedConfig.outputDirectory, "output", "", "directory for the generated PKI and setup script")
	flagSet.StringVar(&parsedConfig.outputDirectory, "o", "", "short for --output")
	flagSet.Func("san", "extra DNS name or IP address for the certificate (repeatable)", func(flagValue string) error {
		parsedConfig.extraSANs = append(parsedConfig.extraSANs, flagValue)
		return nil
	})
	flagSet.DurationVar(&parsedConfig.caValidity, "ca-validity", parsedConfig.caValidity, "CA certificate lifetime")
	flagSet.DurationVar(&parsedConfig.certValidity, "cert-validity", parsedConfig.certValidity, "node certificate lifetime")
	flagSet.StringVar(&parsedConfig.clusterCIDR, "cluster-cidr", "", "overlay network CIDR for container IPs")
	flagSet.StringVar(&parsedConfig.vipCIDR, "vip-cidr", "", "service virtual IP CIDR")
	flagSet.StringVar(&parsedConfig.existingCAPath, "ca", "", "existing CA certificate to reuse (HA setup)")
	flagSet.StringVar(&parsedConfig.existingCAKeyPath, "ca-key", "", "private key of the existing CA")
	flagSet.StringVar(&parsedConfig.existingEtcdAccessCAPath, "etcd-access-ca", "", "existing etcd access CA to reuse (HA setup)")
	flagSet.StringVar(&parsedConfig.existingEtcdAccessCAKeyPath, "etcd-access-ca-key", "", "private key of the existing etcd access CA")
	flagSet.StringVar(&parsedConfig.existingGateHTTPSCAPath, "gate-https-ca", "", "existing gate HTTPS CA to reuse (HA setup)")
	flagSet.StringVar(&parsedConfig.existingGateHTTPSCAKeyPath, "gate-https-ca-key", "", "private key of the existing gate HTTPS CA")
	flagSet.Func("etcd-client-cidr", "network allowed to reach etcd on 2379/tcp (repeatable or comma-separated)", func(flagValue string) error {
		parsedNetworks, cidrError := parseEtcdClientCIDRs(flagValue)
		parsedConfig.etcdClientNetworks = append(parsedConfig.etcdClientNetworks, parsedNetworks...)
		return cidrError
	})
	if parseError := parseDaemonFlagSet(flagSet, args); parseError != nil {
		return parsedConfig, parseError
	}
	if caFlagsError := checkInitExistingCAFlags(parsedConfig); caFlagsError != nil {
		return parsedConfig, caFlagsError
	}
	if etcdClientNetworksCoverFamily(parsedConfig.etcdClientNetworks) {
		networkTexts := make([]string, 0, len(parsedConfig.etcdClientNetworks))
		for _, network := range parsedConfig.etcdClientNetworks {
			networkTexts = append(networkTexts, network.String())
		}
		return parsedConfig, fmt.Errorf("--etcd-client-cidr %s: these networks together cover every address; "+
			"name the cluster's networks, not every source", strings.Join(networkTexts, ","))
	}

	if parsedConfig.nodeID == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			parsedConfig.nodeID = hostname
		} else {
			parsedConfig.nodeID = "node-1"
		}
	}

	if nodeIDError := security.ValidateNodeIdentity(parsedConfig.nodeID); nodeIDError != nil {
		return parsedConfig, fmt.Errorf("--node-id: %w", nodeIDError)
	}

	if parsedConfig.advertiseAddress == "" {
		detectedAddresses := detectLocalIPAddresses()
		if len(detectedAddresses) > 0 {
			parsedConfig.advertiseAddress = detectedAddresses[0].String()
		} else {
			parsedConfig.advertiseAddress = "127.0.0.1"
		}
	}

	return parsedConfig, nil
}

// singleCAFlagCount is the number of existing-CA flags of a single-CA cluster's secondary: --ca and --ca-key.
const singleCAFlagCount = 2

// checkInitExistingCAFlags requires an HA secondary's existing-CA flags all six
// or none, or --ca/--ca-key alone for a cluster from before M280 (one CA, CR-L7):
// a three-CA secondary reusing only one CA would mint CAs its peers do not trust.
func checkInitExistingCAFlags(parsedConfig initCommandConfig) error {
	existingCAFlags := []string{parsedConfig.existingCAPath, parsedConfig.existingCAKeyPath,
		parsedConfig.existingEtcdAccessCAPath, parsedConfig.existingEtcdAccessCAKeyPath,
		parsedConfig.existingGateHTTPSCAPath, parsedConfig.existingGateHTTPSCAKeyPath}
	emptyCount := 0
	for _, flagValue := range existingCAFlags {
		if flagValue == "" {
			emptyCount++
		}
	}
	singleCAPairOnly := parsedConfig.existingCAPath != "" && parsedConfig.existingCAKeyPath != "" &&
		emptyCount == len(existingCAFlags)-singleCAFlagCount
	if emptyCount != 0 && emptyCount != len(existingCAFlags) && !singleCAPairOnly {
		return errors.New("--ca, --ca-key, --etcd-access-ca, --etcd-access-ca-key, --gate-https-ca and " +
			"--gate-https-ca-key go together: an HA secondary reuses all three CAs of the first node " +
			"(a cluster from before M280, with one CA: --ca and --ca-key alone)")
	}
	return nil
}

// parseEtcdClientCIDRs parses one --etcd-client-cidr value, a comma-separated list
// of IPv4 or IPv6 CIDRs, into canonical networks, and names the first item that is
// not one or that covers every address (which would open etcd to all sources).
func parseEtcdClientCIDRs(flagValue string) ([]*net.IPNet, error) {
	var parsedNetworks []*net.IPNet
	for _, cidrItem := range strings.Split(flagValue, ",") {
		_, parsedNetwork, parseError := net.ParseCIDR(strings.TrimSpace(cidrItem))
		if parseError != nil {
			return nil, fmt.Errorf("--etcd-client-cidr %q: %q is not an IPv4 or IPv6 CIDR", flagValue, cidrItem)
		}
		canonicalNetwork := canonicalEtcdClientNetwork(parsedNetwork)
		if prefixLength, _ := canonicalNetwork.Mask.Size(); prefixLength == 0 {
			return nil, fmt.Errorf("--etcd-client-cidr %q: %q covers every address; name the cluster's networks, not every source",
				flagValue, cidrItem)
		}
		parsedNetworks = append(parsedNetworks, canonicalNetwork)
	}
	return parsedNetworks, nil
}

// ipv4MappedPrefixBits is the length of the ::ffff:0:0/96 prefix of IPv4-mapped IPv6 addresses.
const ipv4MappedPrefixBits = 96

// canonicalEtcdClientNetwork returns the one form both the checks and setup.sh use:
// a 4-byte IPv4 network, also for an IPv4-mapped IPv6 network, or a 16-byte IPv6 one.
func canonicalEtcdClientNetwork(network *net.IPNet) *net.IPNet {
	prefixLength, maskBits := network.Mask.Size()
	ipv4Address := network.IP.To4()
	switch {
	case ipv4Address != nil && maskBits == net.IPv4len*8:
		return &net.IPNet{IP: ipv4Address, Mask: network.Mask}
	case ipv4Address != nil && prefixLength >= ipv4MappedPrefixBits:
		return &net.IPNet{IP: ipv4Address, Mask: net.CIDRMask(prefixLength-ipv4MappedPrefixBits, net.IPv4len*8)}
	}
	return &net.IPNet{IP: network.IP.To16(), Mask: network.Mask}
}

// etcdClientNetworksCoverFamily reports whether canonical networks together cover a
// whole address family. CIDR blocks either nest or are disjoint, so after dropping
// every block inside a wider one the sizes add up to the union's size.
func etcdClientNetworksCoverFamily(networks []*net.IPNet) bool {
	networksByFamilyBits := map[int][]*net.IPNet{}
	for _, network := range networks {
		_, maskBits := network.Mask.Size()
		networksByFamilyBits[maskBits] = append(networksByFamilyBits[maskBits], network)
	}
	for familyBits, familyNetworks := range networksByFamilyBits {
		slices.SortFunc(familyNetworks, func(first, second *net.IPNet) int {
			firstLength, _ := first.Mask.Size()
			secondLength, _ := second.Mask.Size()
			return firstLength - secondLength
		})
		coveredAddresses := new(big.Int)
		var outermostNetworks []*net.IPNet
		for _, network := range familyNetworks {
			if slices.ContainsFunc(outermostNetworks, func(wider *net.IPNet) bool { return wider.Contains(network.IP) }) {
				continue // inside a wider block already counted
			}
			outermostNetworks = append(outermostNetworks, network)
			prefixLength, _ := network.Mask.Size()
			coveredAddresses.Add(coveredAddresses, new(big.Int).Lsh(big.NewInt(1), uint(familyBits-prefixLength)))
		}
		if coveredAddresses.Cmp(new(big.Int).Lsh(big.NewInt(1), uint(familyBits))) >= 0 {
			return true
		}
	}
	return false
}

// etcdClientWideIPv4Bits and etcdClientWideIPv6Bits: networks shorter than these
// prefixes get a warning (D5), as do networks that are not private or loopback.
const (
	etcdClientWideIPv4Bits = 8
	etcdClientWideIPv6Bits = 32
)

// etcdClientNetworkWarnings returns one warning per network that is wider than /8
// (IPv4) or /32 (IPv6) or is not private or loopback: until M181 every etcd client
// certificate has full etcd access, so those sources can use any of them.
func etcdClientNetworkWarnings(networks []*net.IPNet) []string {
	var warnings []string
	for _, network := range networks {
		prefixLength, maskBits := network.Mask.Size()
		wideLimit := etcdClientWideIPv6Bits
		if maskBits == net.IPv4len*8 {
			wideLimit = etcdClientWideIPv4Bits
		}
		var reasons []string
		if prefixLength < wideLimit {
			reasons = append(reasons, fmt.Sprintf("is wider than /%d", wideLimit))
		}
		if !networkIsPrivateOrLoopback(network) {
			reasons = append(reasons, "is not private or loopback")
		}
		if len(reasons) > 0 {
			warnings = append(warnings, fmt.Sprintf("WARNING: --etcd-client-cidr %s %s; until etcd roles exist (M181), "+
				"every etcd client certificate has full etcd access from there. Name only the cluster's own networks.",
				network, strings.Join(reasons, " and ")))
		}
	}
	return warnings
}

// networkIsPrivateOrLoopback reports whether a canonical network's first and last
// addresses are private or loopback; an aligned block with both lies in one such range.
func networkIsPrivateOrLoopback(network *net.IPNet) bool {
	lastAddressBytes := make([]byte, len(network.IP))
	for byteIndex := range network.IP {
		lastAddressBytes[byteIndex] = network.IP[byteIndex] | ^network.Mask[byteIndex]
	}
	for _, addressBytes := range [][]byte{network.IP, lastAddressBytes} {
		address, valid := netip.AddrFromSlice(addressBytes)
		if !valid || (!address.IsPrivate() && !address.IsLoopback()) {
			return false
		}
	}
	return true
}

// executeInitCommand generates PKI material and writes a setup script that
// the user reviews and runs with sudo to configure the node.
func executeInitCommand(parsedConfig initCommandConfig) {
	outputDirectory := resolveInitOutputDirectory(parsedConfig.outputDirectory)

	for _, networkWarning := range etcdClientNetworkWarnings(parsedConfig.etcdClientNetworks) {
		fmt.Fprintln(os.Stderr, networkWarning)
	}
	fmt.Printf("Generating PKI for node %s (advertise: %s)...\n", parsedConfig.nodeID, parsedConfig.advertiseAddress)

	pkiLayout := resolveInitPKILayout(parsedConfig)
	clusterCAs := resolveInitCertificateAuthorities(parsedConfig, pkiLayout)
	for _, caFile := range clusterCAs.files(pkiLayout) {
		writeInitCertificateAuthorityFiles(outputDirectory, caFile)
	}
	for _, leafFile := range buildInitLeafCertificates(parsedConfig, clusterCAs, pkiLayout) {
		writeInitLeafCertificateFiles(outputDirectory, leafFile)
	}
	isSecondaryReplica := parsedConfig.existingCAPath != "" && parsedConfig.existingCAKeyPath != ""
	if !isSecondaryReplica {
		writeInitSecretKeys(outputDirectory)
	}

	setupScript := buildSetupScript(parsedConfig.advertiseAddress, parsedConfig.nodeID, outputDirectory,
		parsedConfig.clusterCIDR, parsedConfig.vipCIDR, parsedConfig.etcdClientNetworks, pkiLayout)
	writeInitFile(outputDirectory, "setup.sh", []byte(setupScript), 0755)

	printInitSummary(outputDirectory, pkiLayout)
}

// resolveInitOutputDirectory returns the output path for generated PKI. Uses a
// temp directory when no explicit path is given.
func resolveInitOutputDirectory(explicitPath string) string {
	if explicitPath == "" {
		tempDirectory, tempError := os.MkdirTemp("", "cca-init-")
		if tempError != nil {
			fmt.Fprintf(os.Stderr, "error creating temp directory: %v\n", tempError)
			os.Exit(1)
		}
		return tempDirectory
	}
	if mkdirError := os.MkdirAll(explicitPath, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating output directory: %v\n", mkdirError)
		os.Exit(1)
	}
	return explicitPath
}

// buildCertificateSANs assembles the IP addresses and DNS names for the node
// and etcd certificates from the init command configuration.
func buildCertificateSANs(parsedConfig initCommandConfig) ([]net.IP, []string) {
	var nodeIPAddresses []net.IP
	if parsedIP := net.ParseIP(parsedConfig.advertiseAddress); parsedIP != nil {
		nodeIPAddresses = append(nodeIPAddresses, parsedIP)
	}
	nodeIPAddresses = append(nodeIPAddresses, net.ParseIP("127.0.0.1"))

	nodeDNSNames := []string{"localhost", parsedConfig.nodeID}
	for _, extraSAN := range parsedConfig.extraSANs {
		if extraIP := net.ParseIP(extraSAN); extraIP != nil {
			nodeIPAddresses = append(nodeIPAddresses, extraIP)
		} else {
			nodeDNSNames = append(nodeDNSNames, extraSAN)
		}
	}
	return nodeIPAddresses, nodeDNSNames
}

// initCertificateAuthorityFile is one CA cca init writes, with its file name and the
// one-line description of D1a.
type initCertificateAuthorityFile struct {
	fileName             string                         // certificate file; the key is privateKeyFileName(fileName)
	certificateAuthority *security.CertificateAuthority // the loaded or generated CA
	description          string                         // what the CA signs and who trusts it
}

// initCertificateAuthorities are the three CAs of a new cluster (D1).
type initCertificateAuthorities struct {
	clusterIdentity *security.CertificateAuthority // API server and node identities; the API trusts it
	etcdAccess      *security.CertificateAuthority // etcd serving and client certificates; etcd trusts it
	gateHTTPS       *security.CertificateAuthority // gate serving certificates; clients of TLS gates trust it
}

// files returns the CAs to write with their file names and descriptions, in
// write order: the three of D1, or the single ca.pem of an older cluster.
func (clusterCAs initCertificateAuthorities) files(layout clusterPKILayout) []initCertificateAuthorityFile {
	if layout.isSingleCA() {
		return []initCertificateAuthorityFile{{layout.clusterCAFileName, clusterCAs.clusterIdentity, singleCADescription}}
	}
	return []initCertificateAuthorityFile{
		{clusterIdentityCAFileName, clusterCAs.clusterIdentity, clusterIdentityCADescription},
		{etcdAccessCAFileName, clusterCAs.etcdAccess, etcdAccessCADescription},
		{gateHTTPSCAFileName, clusterCAs.gateHTTPS, gateHTTPSCADescription},
	}
}

// resolveInitPKILayout returns the single-CA layout for an HA secondary given
// --ca/--ca-key alone (a cluster from before M280, CR-L7), else the three-CA one.
func resolveInitPKILayout(parsedConfig initCommandConfig) clusterPKILayout {
	if parsedConfig.existingCAPath != "" && parsedConfig.existingEtcdAccessCAPath == "" {
		return singleCAClusterPKILayout
	}
	return threeCAClusterPKILayout
}

// initCAFlagPaths holds the certificate and key paths of one CA given to cca init (HA secondaries).
type initCAFlagPaths struct {
	certificatePath string // existing CA certificate
	privateKeyPath  string // its private key
}

// resolveInitCertificateAuthorities loads the three CAs on an HA secondary
// (--ca, --etcd-access-ca and --gate-https-ca with their keys) or creates them;
// a single-CA layout uses the --ca for all three roles. Exits on an error.
func resolveInitCertificateAuthorities(parsedConfig initCommandConfig, layout clusterPKILayout) initCertificateAuthorities {
	resolveOne := func(kind security.CertificateAuthorityKind, flagPaths initCAFlagPaths) *security.CertificateAuthority {
		if flagPaths.certificatePath == "" {
			createdCA, createError := security.NewCertificateAuthority(kind, parsedConfig.caValidity)
			if createError != nil {
				fmt.Fprintf(os.Stderr, "error creating the %s CA: %v\n", kind, createError)
				os.Exit(1)
			}
			return createdCA
		}
		loadedCA, loadError := security.LoadCertificateAuthorityFromFiles(flagPaths.certificatePath, flagPaths.privateKeyPath)
		if loadError != nil {
			fmt.Fprintf(os.Stderr, "error loading the %s CA from %s: %v\n", kind, flagPaths.certificatePath, loadError)
			os.Exit(1)
		}
		fmt.Printf("Loaded existing %s CA from %s (HA mode)\n", kind, flagPaths.certificatePath)
		return loadedCA
	}
	if layout.isSingleCA() {
		singleCA := resolveOne(security.ClusterIdentityCertificateAuthority,
			initCAFlagPaths{parsedConfig.existingCAPath, parsedConfig.existingCAKeyPath})
		return initCertificateAuthorities{clusterIdentity: singleCA, etcdAccess: singleCA, gateHTTPS: singleCA}
	}
	return initCertificateAuthorities{
		clusterIdentity: resolveOne(security.ClusterIdentityCertificateAuthority,
			initCAFlagPaths{parsedConfig.existingCAPath, parsedConfig.existingCAKeyPath}),
		etcdAccess: resolveOne(security.EtcdAccessCertificateAuthority,
			initCAFlagPaths{parsedConfig.existingEtcdAccessCAPath, parsedConfig.existingEtcdAccessCAKeyPath}),
		gateHTTPS: resolveOne(security.GateHTTPSCertificateAuthority,
			initCAFlagPaths{parsedConfig.existingGateHTTPSCAPath, parsedConfig.existingGateHTTPSCAKeyPath}),
	}
}

// initLeafCertificate is one leaf certificate cca init issues and writes.
type initLeafCertificate struct {
	fileName    string                           // certificate file; the key is privateKeyFileName(fileName)
	issuer      *security.CertificateAuthority   // CA of its purpose
	request     security.IssueCertificateRequest // identity, OU, usage, SANs and TTL
	description string                           // the D1a comment line, without "# CCattler "
}

// buildInitLeafCertificates lists the leaf certificates of a control-plane node:
// the six of the D1a table, each with its own key and usage, or for a single-CA
// secondary (CR-L7) the older node.pem and etcd.pem, both server and client.
func buildInitLeafCertificates(parsedConfig initCommandConfig, clusterCAs initCertificateAuthorities,
	layout clusterPKILayout) []initLeafCertificate {
	nodeIPAddresses, nodeDNSNames := buildCertificateSANs(parsedConfig)
	nodeID, validity := parsedConfig.nodeID, parsedConfig.certValidity
	leafRequest := func(commonName string, subjectUnit security.CertificateSubjectUnit, usage security.CertificateUsage,
		withSANs bool) security.IssueCertificateRequest {
		request := security.IssueCertificateRequest{CommonName: commonName, SubjectUnit: subjectUnit, Usage: usage, TTL: validity}
		if withSANs {
			request.DNSNames, request.IPAddresses = nodeDNSNames, nodeIPAddresses
		}
		return request
	}
	if layout.isSingleCA() {
		return []initLeafCertificate{
			{legacyNodeCertificateFileName, clusterCAs.clusterIdentity,
				leafRequest(nodeID, security.SubjectUnitNodeIdentityForAPI, security.UsageServerAndClient, true),
				fmt.Sprintf("node certificate of node %s, signed by %s, used by cca server and cca agent for the API and etcd",
					nodeID, legacyClusterCAFileName)},
			{legacyEtcdCertificateFileName, clusterCAs.clusterIdentity,
				leafRequest(etcdServingCommonNamePrefix+nodeID, security.SubjectUnitEtcdServing, security.UsageServerAndClient, true),
				fmt.Sprintf("etcd serving and peer certificate of node %s, signed by %s", nodeID, legacyClusterCAFileName)},
		}
	}
	return []initLeafCertificate{
		{apiServerIdentityCertificateFileName, clusterCAs.clusterIdentity,
			leafRequest(security.ServerCertificateCommonName, security.SubjectUnitAPIServerIdentity, security.UsageServerAndClient, true),
			fmt.Sprintf("API server identity %s on node %s, signed by %s, served by cca server and presented to agent debug APIs",
				security.ServerCertificateCommonName, nodeID, clusterIdentityCAFileName)},
		{nodeIdentityForAPIFileName, clusterCAs.clusterIdentity,
			leafRequest(nodeID, security.SubjectUnitNodeIdentityForAPI, security.UsageClient, true),
			fmt.Sprintf("node identity for API of node %s, signed by %s, presented by cca agent to the API",
				nodeID, clusterIdentityCAFileName)},
		{etcdServingCertificateFileName, clusterCAs.etcdAccess,
			// Server and client: etcd dials its own client port with it (etcd issue 9785; kubeadm model, 1a).
			leafRequest(etcdServingCommonNamePrefix+nodeID, security.SubjectUnitEtcdServing, security.UsageServerAndClient, true),
			fmt.Sprintf("etcd serving certificate of node %s, signed by %s, served by etcd", nodeID, etcdAccessCAFileName)},
		{etcdPeerCertificateFileName, clusterCAs.etcdAccess,
			leafRequest(etcdPeerCommonNamePrefix+nodeID, security.SubjectUnitEtcdPeer, security.UsageServerAndClient, true),
			fmt.Sprintf("etcd peer certificate of node %s, signed by %s, presented by etcd to its HA peers",
				nodeID, etcdAccessCAFileName)},
		{ccaServerEtcdClientCertificateFileName, clusterCAs.etcdAccess,
			leafRequest(ccaServerEtcdClientCommonName, security.SubjectUnitCCAServerEtcdClient, security.UsageClient, false),
			fmt.Sprintf("cca-server etcd client on node %s, signed by %s, used by cca server to connect to etcd",
				nodeID, etcdAccessCAFileName)},
		{nodeEtcdClientCertificateFileName, clusterCAs.etcdAccess,
			leafRequest(security.NodeEtcdClientCommonNamePrefix+nodeID, security.SubjectUnitNodeEtcdClient, security.UsageClient, false),
			fmt.Sprintf("node etcd client of node %s, signed by %s, used by cca agent to connect to etcd",
				nodeID, etcdAccessCAFileName)},
	}
}

// writeInitCertificateAuthorityFiles writes a CA certificate (0644) and its key
// (0600), each after its description line.
func writeInitCertificateAuthorityFiles(outputDirectory string, caFile initCertificateAuthorityFile) {
	caKeyPEM, keyError := caFile.certificateAuthority.CAPrivateKeyPEM()
	if keyError != nil {
		fmt.Fprintf(os.Stderr, "error exporting the key of %s: %v\n", caFile.fileName, keyError)
		os.Exit(1)
	}
	writeInitFile(outputDirectory, caFile.fileName, describedPEM(caFile.description, caFile.certificateAuthority.CACertificatePEM()), 0644)
	writeInitFile(outputDirectory, privateKeyFileName(caFile.fileName),
		describedPEM("private key of "+caFile.fileName+" ("+caFile.description+"); keep it secret", caKeyPEM), 0600)
}

// writeInitLeafCertificateFiles issues one leaf certificate and writes it (0644)
// and its key (0600), each after its description line.
func writeInitLeafCertificateFiles(outputDirectory string, leafFile initLeafCertificate) {
	issuedCertificate, issueError := leafFile.issuer.IssueCertificate(leafFile.request)
	if issueError != nil {
		fmt.Fprintf(os.Stderr, "error issuing %s: %v\n", leafFile.fileName, issueError)
		os.Exit(1)
	}
	writeInitFile(outputDirectory, leafFile.fileName, describedPEM(leafFile.description, issuedCertificate.CertificatePEM), 0644)
	writeInitFile(outputDirectory, privateKeyFileName(leafFile.fileName),
		describedPEM("private key of "+leafFile.fileName+" ("+leafFile.description+")", issuedCertificate.PrivateKeyPEM), 0600)
}

// printInitSummary prints the list of generated files and next-step instructions.
func printInitSummary(outputDirectory string, layout clusterPKILayout) {
	fmt.Println()
	fmt.Printf("PKI and setup script written to %s/\n", outputDirectory)
	fmt.Println()
	fmt.Println("Files generated (each key is the same name with -key.pem, mode 0600):")
	if layout.isSingleCA() {
		fmt.Printf("  %s (single-CA layout of a cluster from before M280)\n", strings.Join(layout.installedFileNames(), ", "))
	} else {
		printThreeCAFileSummary()
	}
	fmt.Println("  secret-root.key    Secret root key (control plane only; same file on every replica)")
	fmt.Println("  setup.sh           Setup script (review, then run with sudo)")
	fmt.Println()
	fmt.Println("Review the setup script, then run:")
	fmt.Printf("  sudo %s/setup.sh\n", outputDirectory)
}

// printThreeCAFileSummary prints one line per file of the three-CA layout.
func printThreeCAFileSummary() {
	fmt.Printf("  %-40s CA of the API server and node identities\n", clusterIdentityCAFileName)
	fmt.Printf("  %-40s CA trusted by etcd (etcd serving and client certificates)\n", etcdAccessCAFileName)
	fmt.Printf("  %-40s CA of gate HTTPS certificates (clients of TLS gates trust it)\n", gateHTTPSCAFileName)
	fmt.Printf("  %-40s cca server's API certificate\n", apiServerIdentityCertificateFileName)
	fmt.Printf("  %-40s cca agent's identity to the API\n", nodeIdentityForAPIFileName)
	fmt.Printf("  %-40s etcd's serving certificate\n", etcdServingCertificateFileName)
	fmt.Printf("  %-40s etcd's peer certificate (HA)\n", etcdPeerCertificateFileName)
	fmt.Printf("  %-40s cca server's etcd client certificate\n", ccaServerEtcdClientCertificateFileName)
	fmt.Printf("  %-40s cca agent's etcd client certificate\n", nodeEtcdClientCertificateFileName)
}

// writeInitSecretKeys writes the secret root key (Flow 29) for a new cluster.
// New clusters have no legacy secret-master.key and write v2 envelopes from
// the first secret. HA secondaries (--ca) get no key: every replica must hold
// the first node's root key, so Ansible copies it instead.
func writeInitSecretKeys(outputDirectory string) {
	if rootKeyError := security.GenerateSecretRootKeyFile(filepath.Join(outputDirectory, "secret-root.key")); rootKeyError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", rootKeyError)
		os.Exit(1)
	}
}

// writeInitFile writes data to a file in the output directory.
func writeInitFile(outputDirectory string, fileName string, data []byte, permissions os.FileMode) {
	filePath := filepath.Join(outputDirectory, fileName)
	if writeError := os.WriteFile(filePath, data, permissions); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", filePath, writeError)
		os.Exit(1)
	}
}

// buildSetupScript generates a shell script that installs the PKI, configures
// etcd with TLS, and creates systemd unit files for the CCattler server and agent.
// When clusterCIDR or vipCIDR are non-empty, the corresponding --cluster-cidr /
// --vip-cidr flags are included in the generated systemd units. This function
// exceeds 80 lines because it is predominantly shell template text assembled
// via WriteString calls with no meaningful logic boundaries to extract.
func buildSetupScript(advertiseAddress string, nodeID string, sourceDirectory string, clusterCIDR string, vipCIDR string,
	etcdClientNetworks []*net.IPNet, layout clusterPKILayout) string {
	var scriptBuilder strings.Builder

	scriptBuilder.WriteString("#!/bin/sh\n")
	scriptBuilder.WriteString("# CCattler node setup script — generated by 'cca init'\n")
	scriptBuilder.WriteString("# Review this script before running: sudo ./setup.sh\n")
	scriptBuilder.WriteString("set -eu\n\n")

	scriptBuilder.WriteString("SOURCE_DIR=\"")
	scriptBuilder.WriteString(sourceDirectory)
	scriptBuilder.WriteString("\"\n")
	fmt.Fprintf(&scriptBuilder, "PKI_DIR=%s\n", clusterPKIDirectory)
	scriptBuilder.WriteString("LOG_DIR=/var/log/ccattler\n")
	scriptBuilder.WriteString("ETCD_DATA_DIR=/var/lib/etcd\n\n")

	scriptBuilder.WriteString("echo '==> Enabling IP forwarding and loading bridge netfilter'\n")
	scriptBuilder.WriteString("modprobe br_netfilter 2>/dev/null || true\n")
	scriptBuilder.WriteString("sysctl -w net.ipv4.ip_forward=1\n")
	scriptBuilder.WriteString("sysctl -w net.bridge.bridge-nf-call-iptables=1 2>/dev/null || true\n")
	scriptBuilder.WriteString("# Persist across reboots.\n")
	scriptBuilder.WriteString("cat > /etc/sysctl.d/99-ccattler.conf <<'SYSCTL'\n")
	scriptBuilder.WriteString("net.ipv4.ip_forward = 1\n")
	scriptBuilder.WriteString("net.bridge.bridge-nf-call-iptables = 1\n")
	scriptBuilder.WriteString("SYSCTL\n")
	scriptBuilder.WriteString("echo 'br_netfilter' > /etc/modules-load.d/ccattler.conf\n\n")

	scriptBuilder.WriteString(buildSetupScriptFirewallStep(etcdClientNetworks))

	scriptBuilder.WriteString("echo '==> Creating directories'\n")
	scriptBuilder.WriteString("mkdir -p /etc/ccattler \"$PKI_DIR\" \"$LOG_DIR\"\n\n")

	scriptBuilder.WriteString(buildSetupScriptPKIInstallStep(layout))
	scriptBuilder.WriteString("# Secret keys exist only in a first-node init; HA secondaries receive copies.\n")
	scriptBuilder.WriteString("if [ -f \"$SOURCE_DIR/secret-master.key\" ] && [ ! -f /etc/ccattler/secret-master.key ]; then\n")
	scriptBuilder.WriteString("    install -m 600 \"$SOURCE_DIR/secret-master.key\" /etc/ccattler/secret-master.key\n")
	scriptBuilder.WriteString("fi\n")
	scriptBuilder.WriteString("if [ -f \"$SOURCE_DIR/secret-root.key\" ] && [ ! -f /etc/ccattler/secret-root.key ]; then\n")
	scriptBuilder.WriteString("    install -m 400 \"$SOURCE_DIR/secret-root.key\" /etc/ccattler/secret-root.key\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString(buildSetupScriptEtcdStep(advertiseAddress, layout))

	scriptBuilder.WriteString("echo '==> Writing etcd endpoints for CLI auto-detection'\n")
	fmt.Fprintf(&scriptBuilder, "echo 'https://127.0.0.1:%s' > /etc/ccattler/endpoints\n", setupScriptEtcdClientPort)
	scriptBuilder.WriteString("chmod 600 /etc/ccattler/endpoints\n\n")

	scriptBuilder.WriteString("echo '==> Installing systemd units'\n")
	fmt.Fprintf(&scriptBuilder, "cat > /etc/systemd/system/cca-server.service <<'UNIT'\n")
	scriptBuilder.WriteString("[Unit]\n")
	scriptBuilder.WriteString("Description=CCattler Server\n")
	scriptBuilder.WriteString("After=network.target etcd.service\n")
	// Wants, not Requires: Requires stops cca-server whenever etcd stops and
	// never starts it again (M219 HA run); cca-server retries etcd itself.
	scriptBuilder.WriteString("Wants=etcd.service\n\n")
	scriptBuilder.WriteString("[Service]\n")
	scriptBuilder.WriteString("Type=simple\n")
	fmt.Fprintf(&scriptBuilder, "ExecStart=/usr/local/bin/cca server \\\n")
	fmt.Fprintf(&scriptBuilder, "  --listen 0.0.0.0:%s \\\n", defaultAPIListenPort)
	fmt.Fprintf(&scriptBuilder, "  --dns --dns-listen %s:15353 \\\n", advertiseAddress)
	writeSetupScriptPKIFlags(&scriptBuilder, setupScriptServerIdentityFlags(layout))
	fmt.Fprintf(&scriptBuilder, "  --endpoints https://127.0.0.1:%s \\\n", setupScriptEtcdClientPort)
	if vipCIDR != "" {
		fmt.Fprintf(&scriptBuilder, "  --vip-cidr %s \\\n", vipCIDR)
	}
	writeSetupScriptPKIFlags(&scriptBuilder, setupScriptServerEtcdFlags(layout))
	fmt.Fprintf(&scriptBuilder, "  --etcd-ca %s\n", installedPKIPath(layout.etcdCAFileName))
	scriptBuilder.WriteString("Restart=on-failure\n")
	scriptBuilder.WriteString("RestartSec=5\n")
	scriptBuilder.WriteString("StandardOutput=journal\n")
	scriptBuilder.WriteString("StandardError=journal\n")
	scriptBuilder.WriteString("SyslogIdentifier=cca-server\n\n")
	scriptBuilder.WriteString("[Install]\n")
	scriptBuilder.WriteString("WantedBy=multi-user.target\n")
	scriptBuilder.WriteString("UNIT\n\n")

	fmt.Fprintf(&scriptBuilder, "cat > /etc/systemd/system/cca-agent.service <<'UNIT'\n")
	scriptBuilder.WriteString("[Unit]\n")
	fmt.Fprintf(&scriptBuilder, "Description=CCattler Agent — node %s\n", nodeID)
	scriptBuilder.WriteString("After=network.target containerd.service\n")
	scriptBuilder.WriteString("Wants=containerd.service\n\n")
	scriptBuilder.WriteString("[Service]\n")
	scriptBuilder.WriteString("Type=simple\n")
	fmt.Fprintf(&scriptBuilder, "ExecStart=/usr/local/bin/cca agent \\\n")
	fmt.Fprintf(&scriptBuilder, "  --node-id %s \\\n", nodeID)
	fmt.Fprintf(&scriptBuilder, "  --advertise-address %s \\\n", advertiseAddress)
	// The local cca-server above; an init host never ran `cca join`, so no saved address (M248).
	fmt.Fprintf(&scriptBuilder, "  --api-server %s \\\n", net.JoinHostPort(advertiseAddress, defaultAPIListenPort))
	writeSetupScriptPKIFlags(&scriptBuilder, []setupScriptPKIFlag{
		{"cert", layout.nodeIdentityFileName}, {"key", privateKeyFileName(layout.nodeIdentityFileName)},
		{"ca", layout.clusterCAFileName},
		{"etcd-cert", layout.nodeEtcdClientFileName}, {"etcd-key", privateKeyFileName(layout.nodeEtcdClientFileName)},
	})
	if clusterCIDR != "" {
		fmt.Fprintf(&scriptBuilder, "  --cluster-cidr %s \\\n", clusterCIDR)
	}
	fmt.Fprintf(&scriptBuilder, "  --dns-upstream %s:15353 \\\n", advertiseAddress)
	fmt.Fprintf(&scriptBuilder, "  --etcd-ca %s\n", installedPKIPath(layout.etcdCAFileName))
	scriptBuilder.WriteString("Restart=on-failure\n")
	scriptBuilder.WriteString("RestartSec=5\n")
	scriptBuilder.WriteString("StandardOutput=journal\n")
	scriptBuilder.WriteString("StandardError=journal\n")
	scriptBuilder.WriteString("SyslogIdentifier=cca-agent\n\n")
	scriptBuilder.WriteString("[Install]\n")
	scriptBuilder.WriteString("WantedBy=multi-user.target\n")
	scriptBuilder.WriteString("UNIT\n\n")

	scriptBuilder.WriteString("echo '==> Reloading systemd and restarting etcd'\n")
	scriptBuilder.WriteString("mkdir -p \"$ETCD_DATA_DIR\"\n")
	scriptBuilder.WriteString("if id etcd >/dev/null 2>&1; then\n")
	scriptBuilder.WriteString("    chown etcd:etcd \"$ETCD_DATA_DIR\"\n")
	scriptBuilder.WriteString("fi\n")
	scriptBuilder.WriteString("chmod 0700 \"$ETCD_DATA_DIR\"\n")
	scriptBuilder.WriteString("systemctl daemon-reload\n")
	scriptBuilder.WriteString("systemctl restart etcd\n")
	scriptBuilder.WriteString("sleep 2\n\n")

	scriptBuilder.WriteString("# Verify etcd is healthy with TLS.\n")
	scriptBuilder.WriteString("if command -v etcdctl >/dev/null 2>&1; then\n")
	fmt.Fprintf(&scriptBuilder, "    if etcdctl --endpoints=https://127.0.0.1:%s \\\n", setupScriptEtcdClientPort)
	fmt.Fprintf(&scriptBuilder, "        --cacert %s \\\n", installedPKIPath(layout.etcdCAFileName))
	fmt.Fprintf(&scriptBuilder, "        --cert %s \\\n", installedPKIPath(layout.serverEtcdClientFileName))
	fmt.Fprintf(&scriptBuilder, "        --key %s \\\n", installedPKIPath(privateKeyFileName(layout.serverEtcdClientFileName)))
	scriptBuilder.WriteString("        endpoint health 2>/dev/null; then\n")
	scriptBuilder.WriteString("        echo '==> etcd healthy (TLS)'\n")
	scriptBuilder.WriteString("    else\n")
	scriptBuilder.WriteString("        echo 'WARNING: etcd health check failed — check: journalctl -u etcd'\n")
	scriptBuilder.WriteString("    fi\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString("echo ''\n")
	scriptBuilder.WriteString("echo '==> Setup complete. Next steps:'\n")
	scriptBuilder.WriteString("echo '  Start the server:  sudo systemctl enable --now cca-server'\n")
	scriptBuilder.WriteString("echo '  Start the agent:   sudo systemctl enable --now cca-agent'\n")
	scriptBuilder.WriteString("echo '  Check status:      cca status'\n")

	return scriptBuilder.String()
}

// buildSetupScriptFirewallStep returns the setup.sh step that opens the
// CCattler ports in ufw or firewalld when either is active: VXLAN, the API
// port, and the etcd client port only from etcdClientNetworks (D5).
func buildSetupScriptFirewallStep(etcdClientNetworks []*net.IPNet) string {
	var scriptBuilder strings.Builder
	scriptBuilder.WriteString("echo '==> Opening VXLAN port (UDP 4789) in host firewall'\n")
	scriptBuilder.WriteString("HOST_FIREWALL=\n")
	scriptBuilder.WriteString("if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'active'; then\n")
	scriptBuilder.WriteString("    HOST_FIREWALL=ufw\n")
	scriptBuilder.WriteString("    ufw allow 4789/udp comment 'CCattler VXLAN'\n")
	fmt.Fprintf(&scriptBuilder, "    ufw allow %s/tcp comment 'CCattler API'\n", defaultAPIListenPort)
	for _, etcdClientNetwork := range etcdClientNetworks {
		fmt.Fprintf(&scriptBuilder, "    ufw allow from %s to any port %s proto tcp comment 'CCattler etcd clients'\n",
			etcdClientNetwork, setupScriptEtcdClientPort)
	}
	scriptBuilder.WriteString("    ufw route allow in on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("    ufw route allow out on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state 2>/dev/null | grep -q running; then\n")
	scriptBuilder.WriteString("    HOST_FIREWALL=firewalld\n")
	scriptBuilder.WriteString("    firewall-cmd --permanent --add-port=4789/udp\n")
	fmt.Fprintf(&scriptBuilder, "    firewall-cmd --permanent --add-port=%s/tcp\n", defaultAPIListenPort)
	for _, etcdClientNetwork := range etcdClientNetworks {
		fmt.Fprintf(&scriptBuilder, "    firewall-cmd --permanent --add-rich-rule="+setupScriptFirewalldEtcdRule+"\n",
			firewalldAddressFamily(etcdClientNetwork), etcdClientNetwork, setupScriptEtcdClientPort)
	}
	scriptBuilder.WriteString("    firewall-cmd --permanent --add-masquerade\n")
	scriptBuilder.WriteString("    firewall-cmd --reload\n")
	scriptBuilder.WriteString("fi\n\n")
	if len(etcdClientNetworks) == 0 {
		scriptBuilder.WriteString(buildSetupScriptEtcdPortClosedNote())
	}
	return scriptBuilder.String()
}

// setupScriptFirewalldEtcdRule is the firewalld rich rule that opens the etcd client
// port to one network; its format verbs are the address family, the network and the port.
const setupScriptFirewalldEtcdRule = `'rule family="%s" source address="%s" port port="%s" protocol="tcp" accept'`

// setupScriptEtcdFamilyPlaceholder stands for the rich-rule family of the operator's
// network, which the note cannot know.
const setupScriptEtcdFamilyPlaceholder = "<ipv4 or ipv6>"

// setupScriptEtcdPortPlaceholder stands for the operator's node network in the
// printed commands when cca init got no --etcd-client-cidr.
const setupScriptEtcdPortPlaceholder = "<your node network>"

// firewalldAddressFamily returns the firewalld rich-rule family of a network.
func firewalldAddressFamily(network *net.IPNet) string {
	if network.IP.To4() != nil {
		return "ipv4"
	}
	return "ipv6"
}

// buildSetupScriptEtcdPortClosedNote returns the setup.sh lines printed without
// --etcd-client-cidr: whether the active firewall keeps 2379/tcp closed or no
// firewall is active, and the commands that open it for one network (D5).
func buildSetupScriptEtcdPortClosedNote() string {
	var noteBuilder strings.Builder
	noteBuilder.WriteString("if [ -n \"$HOST_FIREWALL\" ]; then\n")
	fmt.Fprintf(&noteBuilder, "    echo \"NOTE: $HOST_FIREWALL is active and setup.sh did not open the etcd client port %s/tcp, because\"\n", setupScriptEtcdClientPort)
	noteBuilder.WriteString("    echo 'cca init got no --etcd-client-cidr. Agents on worker hosts need it. Open it only for your node network:'\n")
	noteBuilder.WriteString("else\n")
	fmt.Fprintf(&noteBuilder, "    echo 'NOTE: No ufw or firewalld is active, so neither blocks the etcd client port %s/tcp. If you enable one,'\n", setupScriptEtcdClientPort)
	noteBuilder.WriteString("    echo 'open the port only for your node network (cca init --etcd-client-cidr does this when one is active):'\n")
	noteBuilder.WriteString("fi\n")
	noteBuilder.WriteString("cat <<'ETCD_PORT_NOTE'\n")
	fmt.Fprintf(&noteBuilder, "  firewalld: firewall-cmd --permanent --add-rich-rule="+setupScriptFirewalldEtcdRule+" && firewall-cmd --reload\n",
		setupScriptEtcdFamilyPlaceholder, setupScriptEtcdPortPlaceholder, setupScriptEtcdClientPort)
	fmt.Fprintf(&noteBuilder, "  ufw:       ufw allow from '%s' to any port %s proto tcp comment 'CCattler etcd clients'\n",
		setupScriptEtcdPortPlaceholder, setupScriptEtcdClientPort)
	noteBuilder.WriteString("ETCD_PORT_NOTE\n\n")
	return noteBuilder.String()
}

// buildSetupScriptEtcdStep returns the setup.sh step that finds the etcd
// binary, writes etcd.service when no distribution unit exists (D3), and
// writes the TLS drop-in, which never sets the data directory.
func buildSetupScriptEtcdStep(advertiseAddress string, layout clusterPKILayout) string {
	var scriptBuilder strings.Builder
	scriptBuilder.WriteString(setupScriptEtcdLookupStep)
	scriptBuilder.WriteString(setupScriptEtcdUnitStep)

	scriptBuilder.WriteString("# Grant etcd ownership of its own key pairs (serving, and peer for HA).\n")
	scriptBuilder.WriteString("if id etcd >/dev/null 2>&1; then\n")
	scriptBuilder.WriteString("    chown etcd:etcd")
	for _, etcdOwnedFileName := range slices.Compact([]string{layout.etcdServingFileName, layout.etcdPeerFileName}) {
		fmt.Fprintf(&scriptBuilder, " \"$PKI_DIR/%s\" \"$PKI_DIR/%s\"", etcdOwnedFileName, privateKeyFileName(etcdOwnedFileName))
	}
	scriptBuilder.WriteString("\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString("# Clear /etc/default/etcd to prevent apt-default DAEMON_ARGS from\n")
	scriptBuilder.WriteString("# overriding TLS environment variables via command-line flags.\n")
	scriptBuilder.WriteString("echo -n > /etc/default/etcd 2>/dev/null || true\n\n")
	scriptBuilder.WriteString("mkdir -p /etc/systemd/system/etcd.service.d\n")
	scriptBuilder.WriteString("# ETCD_BIN comes from the lookup above.\n")
	scriptBuilder.WriteString("cat > /etc/systemd/system/etcd.service.d/tls.conf <<ETCD_OVERRIDE\n")
	scriptBuilder.WriteString("[Service]\n")
	scriptBuilder.WriteString("# Drop the base unit's EnvironmentFile (Debian: /etc/default/etcd), whose\n")
	scriptBuilder.WriteString("# variables would override the TLS settings below.\n")
	scriptBuilder.WriteString("EnvironmentFile=\n")
	scriptBuilder.WriteString("# Clear base unit ExecStart to prevent flag/env conflicts (RHEL ships\n")
	scriptBuilder.WriteString("# etcd with --listen-client-urls flags; etcd 3.5+ rejects duplicate config).\n")
	scriptBuilder.WriteString("ExecStart=\n")
	scriptBuilder.WriteString("ExecStart=$ETCD_BIN\n")
	scriptBuilder.WriteString("Environment=\"DAEMON_ARGS=\"\n")
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_LISTEN_CLIENT_URLS=https://%s:%s,https://127.0.0.1:%s\"\n",
		advertiseAddress, setupScriptEtcdClientPort, setupScriptEtcdClientPort)
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_ADVERTISE_CLIENT_URLS=https://%s:%s\"\n", advertiseAddress, setupScriptEtcdClientPort)
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_CERT_FILE=%s\"\n", installedPKIPath(layout.etcdServingFileName))
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_KEY_FILE=%s\"\n", installedPKIPath(privateKeyFileName(layout.etcdServingFileName)))
	// Only the etcd access CA: the cluster identity and gate HTTPS CAs sign no etcd client (M280 D5).
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_TRUSTED_CA_FILE=%s\"\n", installedPKIPath(layout.etcdCAFileName))
	scriptBuilder.WriteString("Environment=\"ETCD_CLIENT_CERT_AUTH=true\"\n")
	scriptBuilder.WriteString("ETCD_OVERRIDE\n\n")

	return scriptBuilder.String()
}

// setupScriptEtcdClientPort is the etcd client port setup.sh configures and opens
// in the host firewall; agents on worker hosts connect to it.
const setupScriptEtcdClientPort = "2379"

// setupScriptEtcdLookupStep finds etcd on PATH, then in /usr/local/bin and
// /usr/bin (sudo's secure_path may omit /usr/local/bin, where the release binary
// goes), and stops with real sources per distribution family when it is missing.
const setupScriptEtcdLookupStep = `echo '==> Configuring etcd with TLS'
ETCD_BIN=$(command -v etcd || true)
for ETCD_CANDIDATE in /usr/local/bin/etcd /usr/bin/etcd; do
    if [ -z "$ETCD_BIN" ] && [ -x "$ETCD_CANDIDATE" ]; then
        ETCD_BIN=$ETCD_CANDIDATE
    fi
done
if [ -z "$ETCD_BIN" ]; then
    echo 'ERROR: etcd not found. Install etcd first:' >&2
    echo '  Debian/Ubuntu: sudo apt install etcd-server' >&2
    echo '  RHEL/AlmaLinux/Rocky: no repository ships etcd; install the release binary from' >&2
    echo '    https://github.com/etcd-io/etcd/releases into /usr/local/bin, verified with SHA256SUMS' >&2
    echo '    (steps: https://ccattler.org/installation#prepare-a-fresh-host)' >&2
    echo 'Then re-run this script.' >&2
    exit 1
fi

`

// setupScriptEtcdUnitStep creates the etcd user and a full etcd.service when the
// host has no unit (a release binary). The data directory is explicit, the same
// /var/lib/etcd/default as Debian's etcd-server; a distribution unit is kept.
const setupScriptEtcdUnitStep = `if ! systemctl cat etcd.service >/dev/null 2>&1; then
    echo '==> No etcd.service found: creating the etcd user and unit'
    if ! id etcd >/dev/null 2>&1; then
        ETCD_NOLOGIN=/sbin/nologin
        if [ -x /usr/sbin/nologin ]; then ETCD_NOLOGIN=/usr/sbin/nologin; fi
        if command -v useradd >/dev/null 2>&1; then
            useradd --system --user-group --home-dir /var/lib/etcd --shell "$ETCD_NOLOGIN" etcd
        elif command -v adduser >/dev/null 2>&1; then
            adduser --system --group --home /var/lib/etcd --shell "$ETCD_NOLOGIN" etcd
        else
            echo 'ERROR: cannot create the etcd user: neither useradd nor adduser found.' >&2
            exit 1
        fi
    fi
    cat > /etc/systemd/system/etcd.service <<ETCD_UNIT
[Unit]
Description=etcd key-value store (written by CCattler setup.sh)
Documentation=https://etcd.io/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=etcd
ExecStart=$ETCD_BIN
Environment=ETCD_DATA_DIR=/var/lib/etcd/default
Environment=ETCD_NAME=%H
Restart=on-failure
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
ETCD_UNIT
    # Started at boot even if cca-server is not enabled; a distribution unit is enabled by its package.
    systemctl daemon-reload
    systemctl enable etcd.service
fi
`
