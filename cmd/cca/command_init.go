// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Init command: generates PKI and produces a setup script for the node.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	flagSet.Func("etcd-client-cidr", "network allowed to reach etcd on 2379/tcp (repeatable or comma-separated)", func(flagValue string) error {
		parsedNetworks, cidrError := parseEtcdClientCIDRs(flagValue)
		parsedConfig.etcdClientNetworks = append(parsedConfig.etcdClientNetworks, parsedNetworks...)
		return cidrError
	})
	if parseError := parseDaemonFlagSet(flagSet, args); parseError != nil {
		return parsedConfig, parseError
	}

	if parsedConfig.nodeID == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			parsedConfig.nodeID = hostname
		} else {
			parsedConfig.nodeID = "node-1"
		}
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

// parseEtcdClientCIDRs parses one --etcd-client-cidr value, a comma-separated list
// of IPv4 or IPv6 CIDRs, and names the first item that is not one.
func parseEtcdClientCIDRs(flagValue string) ([]*net.IPNet, error) {
	var parsedNetworks []*net.IPNet
	for _, cidrItem := range strings.Split(flagValue, ",") {
		_, parsedNetwork, parseError := net.ParseCIDR(strings.TrimSpace(cidrItem))
		if parseError != nil {
			return nil, fmt.Errorf("--etcd-client-cidr %q: %q is not an IPv4 or IPv6 CIDR", flagValue, cidrItem)
		}
		parsedNetworks = append(parsedNetworks, parsedNetwork)
	}
	return parsedNetworks, nil
}

// executeInitCommand generates PKI material and writes a setup script that
// the user reviews and runs with sudo to configure the node.
func executeInitCommand(parsedConfig initCommandConfig) {
	outputDirectory := resolveInitOutputDirectory(parsedConfig.outputDirectory)

	fmt.Printf("Generating PKI for node %s (advertise: %s)...\n", parsedConfig.nodeID, parsedConfig.advertiseAddress)

	var certificateAuthority *security.CertificateAuthority
	if parsedConfig.existingCAPath != "" && parsedConfig.existingCAKeyPath != "" {
		loadedCA, loadError := security.LoadCertificateAuthorityFromFiles(parsedConfig.existingCAPath, parsedConfig.existingCAKeyPath)
		if loadError != nil {
			fmt.Fprintf(os.Stderr, "error loading existing CA: %v\n", loadError)
			os.Exit(1)
		}
		certificateAuthority = loadedCA
		fmt.Println("Loaded existing CA (HA mode)")
	} else {
		newCA, caError := security.NewCertificateAuthority(parsedConfig.caValidity)
		if caError != nil {
			fmt.Fprintf(os.Stderr, "error creating CA: %v\n", caError)
			os.Exit(1)
		}
		certificateAuthority = newCA
	}

	nodeIPAddresses, nodeDNSNames := buildCertificateSANs(parsedConfig)

	nodeCertificate := issueCertificateOrExit(certificateAuthority, "node", parsedConfig.nodeID, nodeDNSNames, nodeIPAddresses, parsedConfig.certValidity)
	etcdCertificate := issueCertificateOrExit(certificateAuthority, "etcd", "etcd-"+parsedConfig.nodeID, nodeDNSNames, nodeIPAddresses, parsedConfig.certValidity)

	caKeyPEM, keyError := certificateAuthority.CAPrivateKeyPEM()
	if keyError != nil {
		fmt.Fprintf(os.Stderr, "error exporting CA key: %v\n", keyError)
		os.Exit(1)
	}

	writeInitFile(outputDirectory, "ca.pem", certificateAuthority.CACertificatePEM(), 0644)
	writeInitFile(outputDirectory, "ca-key.pem", caKeyPEM, 0600)
	writeInitFile(outputDirectory, "node.pem", nodeCertificate.CertificatePEM, 0644)
	writeInitFile(outputDirectory, "node-key.pem", nodeCertificate.PrivateKeyPEM, 0600)
	writeInitFile(outputDirectory, "etcd.pem", etcdCertificate.CertificatePEM, 0644)
	writeInitFile(outputDirectory, "etcd-key.pem", etcdCertificate.PrivateKeyPEM, 0600)
	isSecondaryReplica := parsedConfig.existingCAPath != "" && parsedConfig.existingCAKeyPath != ""
	if !isSecondaryReplica {
		writeInitSecretKeys(outputDirectory)
	}

	setupScript := buildSetupScript(parsedConfig.advertiseAddress, parsedConfig.nodeID, outputDirectory,
		parsedConfig.clusterCIDR, parsedConfig.vipCIDR, parsedConfig.etcdClientNetworks)
	writeInitFile(outputDirectory, "setup.sh", []byte(setupScript), 0755)

	printInitSummary(outputDirectory)
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

// issueCertificateOrExit issues a certificate from the CA and exits with an
// error message if issuance fails.
func issueCertificateOrExit(certificateAuthority *security.CertificateAuthority, label string, commonName string, dnsNames []string, ipAddresses []net.IP, validity time.Duration) *security.IssuedCertificate {
	certificate, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{
		CommonName:  commonName,
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
		TTL:         validity,
	})
	if issueError != nil {
		fmt.Fprintf(os.Stderr, "error issuing %s certificate: %v\n", label, issueError)
		os.Exit(1)
	}
	return certificate
}

// printInitSummary prints the list of generated files and next-step instructions.
func printInitSummary(outputDirectory string) {
	fmt.Println()
	fmt.Printf("PKI and setup script written to %s/\n", outputDirectory)
	fmt.Println()
	fmt.Println("Files generated:")
	fmt.Println("  ca.pem             CA certificate (distribute to all nodes)")
	fmt.Println("  ca-key.pem         CA private key (keep on control plane only)")
	fmt.Println("  node.pem           Node certificate (cca server/agent)")
	fmt.Println("  node-key.pem       Node private key (cca server/agent)")
	fmt.Println("  etcd.pem           etcd certificate")
	fmt.Println("  etcd-key.pem       etcd private key (owned by etcd user)")
	fmt.Println("  secret-root.key    Secret root key (control plane only; same file on every replica)")
	fmt.Println("  setup.sh           Setup script (review, then run with sudo)")
	fmt.Println()
	fmt.Println("Review the setup script, then run:")
	fmt.Printf("  sudo %s/setup.sh\n", outputDirectory)
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
	etcdClientNetworks []*net.IPNet) string {
	var scriptBuilder strings.Builder

	scriptBuilder.WriteString("#!/bin/sh\n")
	scriptBuilder.WriteString("# CCattler node setup script — generated by 'cca init'\n")
	scriptBuilder.WriteString("# Review this script before running: sudo ./setup.sh\n")
	scriptBuilder.WriteString("set -eu\n\n")

	scriptBuilder.WriteString("SOURCE_DIR=\"")
	scriptBuilder.WriteString(sourceDirectory)
	scriptBuilder.WriteString("\"\n")
	scriptBuilder.WriteString("PKI_DIR=/etc/ccattler/pki\n")
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

	scriptBuilder.WriteString("echo '==> Installing PKI certificates'\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/ca.pem\" \"$SOURCE_DIR/ca-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/node.pem\" \"$SOURCE_DIR/node-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/etcd.pem\" \"$SOURCE_DIR/etcd-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/ca.pem\" /etc/ccattler/ca.pem\n")
	scriptBuilder.WriteString("chmod 600 \"$PKI_DIR/ca-key.pem\" \"$PKI_DIR/node-key.pem\" \"$PKI_DIR/etcd-key.pem\"\n")
	scriptBuilder.WriteString("# Secret keys exist only in a first-node init; HA secondaries receive copies.\n")
	scriptBuilder.WriteString("if [ -f \"$SOURCE_DIR/secret-master.key\" ] && [ ! -f /etc/ccattler/secret-master.key ]; then\n")
	scriptBuilder.WriteString("    install -m 600 \"$SOURCE_DIR/secret-master.key\" /etc/ccattler/secret-master.key\n")
	scriptBuilder.WriteString("fi\n")
	scriptBuilder.WriteString("if [ -f \"$SOURCE_DIR/secret-root.key\" ] && [ ! -f /etc/ccattler/secret-root.key ]; then\n")
	scriptBuilder.WriteString("    install -m 400 \"$SOURCE_DIR/secret-root.key\" /etc/ccattler/secret-root.key\n")
	scriptBuilder.WriteString("fi\n")
	scriptBuilder.WriteString("chmod 644 \"$PKI_DIR/ca.pem\" \"$PKI_DIR/node.pem\" \"$PKI_DIR/etcd.pem\"\n\n")

	scriptBuilder.WriteString(buildSetupScriptEtcdStep(advertiseAddress))

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
	scriptBuilder.WriteString("  --cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --key /etc/ccattler/pki/node-key.pem \\\n")
	scriptBuilder.WriteString("  --ca /etc/ccattler/pki/ca.pem \\\n")
	scriptBuilder.WriteString("  --ca-key /etc/ccattler/pki/ca-key.pem \\\n")
	fmt.Fprintf(&scriptBuilder, "  --endpoints https://127.0.0.1:%s \\\n", setupScriptEtcdClientPort)
	scriptBuilder.WriteString("  --etcd-cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --etcd-key /etc/ccattler/pki/node-key.pem \\\n")
	if vipCIDR != "" {
		fmt.Fprintf(&scriptBuilder, "  --vip-cidr %s \\\n", vipCIDR)
	}
	scriptBuilder.WriteString("  --etcd-ca /etc/ccattler/pki/ca.pem\n")
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
	scriptBuilder.WriteString("  --cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --key /etc/ccattler/pki/node-key.pem \\\n")
	scriptBuilder.WriteString("  --ca /etc/ccattler/pki/ca.pem \\\n")
	scriptBuilder.WriteString("  --etcd-cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --etcd-key /etc/ccattler/pki/node-key.pem \\\n")
	if clusterCIDR != "" {
		fmt.Fprintf(&scriptBuilder, "  --cluster-cidr %s \\\n", clusterCIDR)
	}
	fmt.Fprintf(&scriptBuilder, "  --dns-upstream %s:15353 \\\n", advertiseAddress)
	scriptBuilder.WriteString("  --etcd-ca /etc/ccattler/pki/ca.pem\n")
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
	scriptBuilder.WriteString("        --cacert /etc/ccattler/pki/ca.pem \\\n")
	scriptBuilder.WriteString("        --cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("        --key /etc/ccattler/pki/node-key.pem \\\n")
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
	scriptBuilder.WriteString("if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'active'; then\n")
	scriptBuilder.WriteString("    ufw allow 4789/udp comment 'CCattler VXLAN'\n")
	fmt.Fprintf(&scriptBuilder, "    ufw allow %s/tcp comment 'CCattler API'\n", defaultAPIListenPort)
	for _, etcdClientNetwork := range etcdClientNetworks {
		fmt.Fprintf(&scriptBuilder, "    ufw allow from %s to any port %s proto tcp comment 'CCattler etcd clients'\n",
			etcdClientNetwork, setupScriptEtcdClientPort)
	}
	scriptBuilder.WriteString("    ufw route allow in on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("    ufw route allow out on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state 2>/dev/null | grep -q running; then\n")
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

// buildSetupScriptEtcdPortClosedNote returns the setup.sh lines that say 2379/tcp
// stays closed without --etcd-client-cidr and print the commands that open it (D5).
func buildSetupScriptEtcdPortClosedNote() string {
	var noteBuilder strings.Builder
	noteBuilder.WriteString("cat <<'ETCD_PORT_NOTE'\n")
	fmt.Fprintf(&noteBuilder, "NOTE: the etcd client port %s/tcp is not opened in the host firewall, because cca init\n", setupScriptEtcdClientPort)
	noteBuilder.WriteString("got no --etcd-client-cidr. Agents on worker hosts need it. Open it for your node network:\n")
	fmt.Fprintf(&noteBuilder, "  firewalld: firewall-cmd --permanent --add-rich-rule="+setupScriptFirewalldEtcdRule+" && firewall-cmd --reload\n",
		"ipv4", setupScriptEtcdPortPlaceholder, setupScriptEtcdClientPort)
	fmt.Fprintf(&noteBuilder, "  ufw:       ufw allow from '%s' to any port %s proto tcp comment 'CCattler etcd clients'\n",
		setupScriptEtcdPortPlaceholder, setupScriptEtcdClientPort)
	noteBuilder.WriteString("ETCD_PORT_NOTE\n\n")
	return noteBuilder.String()
}

// buildSetupScriptEtcdStep returns the setup.sh step that finds the etcd
// binary, writes etcd.service when no distribution unit exists (D3), and
// writes the TLS drop-in, which never sets the data directory.
func buildSetupScriptEtcdStep(advertiseAddress string) string {
	var scriptBuilder strings.Builder
	scriptBuilder.WriteString(setupScriptEtcdLookupStep)
	scriptBuilder.WriteString(setupScriptEtcdUnitStep)

	scriptBuilder.WriteString("# Grant etcd ownership of its own key pair.\n")
	scriptBuilder.WriteString("if id etcd >/dev/null 2>&1; then\n")
	scriptBuilder.WriteString("    chown etcd:etcd \"$PKI_DIR/etcd.pem\" \"$PKI_DIR/etcd-key.pem\"\n")
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
	scriptBuilder.WriteString("Environment=\"ETCD_CERT_FILE=/etc/ccattler/pki/etcd.pem\"\n")
	scriptBuilder.WriteString("Environment=\"ETCD_KEY_FILE=/etc/ccattler/pki/etcd-key.pem\"\n")
	scriptBuilder.WriteString("Environment=\"ETCD_TRUSTED_CA_FILE=/etc/ccattler/pki/ca.pem\"\n")
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
