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
}

// parseInitCommandArgs extracts flags from the arguments following "init".
func parseInitCommandArgs(args []string) initCommandConfig {
	parsedConfig := initCommandConfig{
		caValidity:   defaultCACertificateValidityInit,
		certValidity: defaultNodeCertificateValidityInit,
	}

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--advertise-address":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.advertiseAddress = args[argIndex]
			}
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--output", "-o":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.outputDirectory = args[argIndex]
			}
		case "--san":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.extraSANs = append(parsedConfig.extraSANs, args[argIndex])
			}
		case "--ca-validity":
			if argIndex+1 < len(args) {
				argIndex++
				if parsed, parseErr := time.ParseDuration(args[argIndex]); parseErr == nil {
					parsedConfig.caValidity = parsed
				}
			}
		case "--cert-validity":
			if argIndex+1 < len(args) {
				argIndex++
				if parsed, parseErr := time.ParseDuration(args[argIndex]); parseErr == nil {
					parsedConfig.certValidity = parsed
				}
			}
		case "--cluster-cidr":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.clusterCIDR = args[argIndex]
			}
		case "--vip-cidr":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.vipCIDR = args[argIndex]
			}
		case "--ca":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.existingCAPath = args[argIndex]
			}
		case "--ca-key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.existingCAKeyPath = args[argIndex]
			}
		}
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

	return parsedConfig
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

	secretMasterKey, masterKeyError := security.GenerateMasterKey()
	if masterKeyError != nil {
		fmt.Fprintf(os.Stderr, "error generating secret master key: %v\n", masterKeyError)
		os.Exit(1)
	}

	writeInitFile(outputDirectory, "ca.pem", certificateAuthority.CACertificatePEM(), 0644)
	writeInitFile(outputDirectory, "ca-key.pem", caKeyPEM, 0600)
	writeInitFile(outputDirectory, "node.pem", nodeCertificate.CertificatePEM, 0644)
	writeInitFile(outputDirectory, "node-key.pem", nodeCertificate.PrivateKeyPEM, 0600)
	writeInitFile(outputDirectory, "etcd.pem", etcdCertificate.CertificatePEM, 0644)
	writeInitFile(outputDirectory, "etcd-key.pem", etcdCertificate.PrivateKeyPEM, 0600)
	writeInitFile(outputDirectory, "secret-master.key", secretMasterKey, 0600)

	setupScript := buildSetupScript(parsedConfig.advertiseAddress, parsedConfig.nodeID, outputDirectory, parsedConfig.clusterCIDR, parsedConfig.vipCIDR)
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
	fmt.Println("  secret-master.key  Secret store encryption key")
	fmt.Println("  setup.sh           Setup script (review, then run with sudo)")
	fmt.Println()
	fmt.Println("Review the setup script, then run:")
	fmt.Printf("  sudo %s/setup.sh\n", outputDirectory)
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
func buildSetupScript(advertiseAddress string, nodeID string, sourceDirectory string, clusterCIDR string, vipCIDR string) string {
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

	scriptBuilder.WriteString("echo '==> Opening VXLAN port (UDP 4789) in host firewall'\n")
	scriptBuilder.WriteString("if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'active'; then\n")
	scriptBuilder.WriteString("    ufw allow 4789/udp comment 'CCattler VXLAN'\n")
	scriptBuilder.WriteString("    ufw allow 9770/tcp comment 'CCattler API'\n")
	scriptBuilder.WriteString("    ufw route allow in on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("    ufw route allow out on cca-vxlan0 comment 'CCattler VXLAN forward'\n")
	scriptBuilder.WriteString("elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state 2>/dev/null | grep -q running; then\n")
	scriptBuilder.WriteString("    firewall-cmd --permanent --add-port=4789/udp\n")
	scriptBuilder.WriteString("    firewall-cmd --permanent --add-port=9770/tcp\n")
	scriptBuilder.WriteString("    firewall-cmd --permanent --add-masquerade\n")
	scriptBuilder.WriteString("    firewall-cmd --reload\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString("echo '==> Creating directories'\n")
	scriptBuilder.WriteString("mkdir -p /etc/ccattler \"$PKI_DIR\" \"$LOG_DIR\"\n\n")

	scriptBuilder.WriteString("echo '==> Installing PKI certificates'\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/ca.pem\" \"$SOURCE_DIR/ca-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/node.pem\" \"$SOURCE_DIR/node-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/etcd.pem\" \"$SOURCE_DIR/etcd-key.pem\" \"$PKI_DIR/\"\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/ca.pem\" /etc/ccattler/ca.pem\n")
	scriptBuilder.WriteString("cp \"$SOURCE_DIR/secret-master.key\" /etc/ccattler/secret-master.key\n")
	scriptBuilder.WriteString("chmod 600 \"$PKI_DIR/ca-key.pem\" \"$PKI_DIR/node-key.pem\" \"$PKI_DIR/etcd-key.pem\" /etc/ccattler/secret-master.key\n")
	scriptBuilder.WriteString("chmod 644 \"$PKI_DIR/ca.pem\" \"$PKI_DIR/node.pem\" \"$PKI_DIR/etcd.pem\"\n\n")

	scriptBuilder.WriteString("echo '==> Configuring etcd with TLS'\n")
	scriptBuilder.WriteString("if ! command -v etcd >/dev/null 2>&1; then\n")
	scriptBuilder.WriteString("    echo 'WARNING: etcd not found. Install etcd first:'\n")
	scriptBuilder.WriteString("    echo '  Debian/Ubuntu: sudo apt install etcd-server'\n")
	scriptBuilder.WriteString("    echo '  RHEL/AlmaLinux: sudo dnf install etcd'\n")
	scriptBuilder.WriteString("    echo 'Then re-run this script.'\n")
	scriptBuilder.WriteString("    exit 1\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString("# Grant etcd ownership of its own key pair.\n")
	scriptBuilder.WriteString("if id etcd >/dev/null 2>&1; then\n")
	scriptBuilder.WriteString("    chown etcd:etcd \"$PKI_DIR/etcd.pem\" \"$PKI_DIR/etcd-key.pem\"\n")
	scriptBuilder.WriteString("fi\n\n")

	scriptBuilder.WriteString("# Clear /etc/default/etcd to prevent apt-default DAEMON_ARGS from\n")
	scriptBuilder.WriteString("# overriding TLS environment variables via command-line flags.\n")
	scriptBuilder.WriteString("echo -n > /etc/default/etcd 2>/dev/null || true\n\n")
	scriptBuilder.WriteString("mkdir -p /etc/systemd/system/etcd.service.d\n")
	scriptBuilder.WriteString("# Detect etcd binary path for ExecStart override.\n")
	scriptBuilder.WriteString("ETCD_BIN=$(command -v etcd)\n")
	scriptBuilder.WriteString("cat > /etc/systemd/system/etcd.service.d/tls.conf <<ETCD_OVERRIDE\n")
	scriptBuilder.WriteString("[Service]\n")
	scriptBuilder.WriteString("# Clear base unit ExecStart to prevent flag/env conflicts (RHEL ships\n")
	scriptBuilder.WriteString("# etcd with --listen-client-urls flags; etcd 3.5+ rejects duplicate config).\n")
	scriptBuilder.WriteString("ExecStart=\n")
	scriptBuilder.WriteString("ExecStart=$ETCD_BIN\n")
	scriptBuilder.WriteString("Environment=\"DAEMON_ARGS=\"\n")
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_LISTEN_CLIENT_URLS=https://%s:2379,https://127.0.0.1:2379\"\n", advertiseAddress)
	fmt.Fprintf(&scriptBuilder, "Environment=\"ETCD_ADVERTISE_CLIENT_URLS=https://%s:2379\"\n", advertiseAddress)
	scriptBuilder.WriteString("Environment=\"ETCD_CERT_FILE=/etc/ccattler/pki/etcd.pem\"\n")
	scriptBuilder.WriteString("Environment=\"ETCD_KEY_FILE=/etc/ccattler/pki/etcd-key.pem\"\n")
	scriptBuilder.WriteString("Environment=\"ETCD_TRUSTED_CA_FILE=/etc/ccattler/pki/ca.pem\"\n")
	scriptBuilder.WriteString("Environment=\"ETCD_CLIENT_CERT_AUTH=true\"\n")
	scriptBuilder.WriteString("ETCD_OVERRIDE\n\n")

	scriptBuilder.WriteString("echo '==> Writing etcd endpoints for CLI auto-detection'\n")
	scriptBuilder.WriteString("echo 'https://127.0.0.1:2379' > /etc/ccattler/endpoints\n")
	scriptBuilder.WriteString("chmod 600 /etc/ccattler/endpoints\n\n")

	scriptBuilder.WriteString("echo '==> Installing systemd units'\n")
	fmt.Fprintf(&scriptBuilder, "cat > /etc/systemd/system/cca-server.service <<'UNIT'\n")
	scriptBuilder.WriteString("[Unit]\n")
	scriptBuilder.WriteString("Description=CCattler Server\n")
	scriptBuilder.WriteString("After=network.target etcd.service\n")
	scriptBuilder.WriteString("Requires=etcd.service\n\n")
	scriptBuilder.WriteString("[Service]\n")
	scriptBuilder.WriteString("Type=simple\n")
	fmt.Fprintf(&scriptBuilder, "ExecStart=/usr/local/bin/cca server \\\n")
	scriptBuilder.WriteString("  --listen 0.0.0.0:9770 \\\n")
	fmt.Fprintf(&scriptBuilder, "  --dns --dns-listen %s:15353 \\\n", advertiseAddress)
	scriptBuilder.WriteString("  --cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --key /etc/ccattler/pki/node-key.pem \\\n")
	scriptBuilder.WriteString("  --ca /etc/ccattler/pki/ca.pem \\\n")
	scriptBuilder.WriteString("  --ca-key /etc/ccattler/pki/ca-key.pem \\\n")
	scriptBuilder.WriteString("  --etcd-endpoints https://127.0.0.1:2379 \\\n")
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
	scriptBuilder.WriteString("  --cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --key /etc/ccattler/pki/node-key.pem \\\n")
	scriptBuilder.WriteString("  --ca /etc/ccattler/pki/ca.pem \\\n")
	scriptBuilder.WriteString("  --etcd-cert /etc/ccattler/pki/node.pem \\\n")
	scriptBuilder.WriteString("  --etcd-key /etc/ccattler/pki/node-key.pem \\\n")
	if clusterCIDR != "" {
		fmt.Fprintf(&scriptBuilder, "  --cluster-cidr %s \\\n", clusterCIDR)
	}
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
	scriptBuilder.WriteString("    if etcdctl --endpoints=https://127.0.0.1:2379 \\\n")
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
