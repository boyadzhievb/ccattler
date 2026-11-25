// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Server side of the three cluster CAs (M280 D3, D6): which CA issues gate
// certificates, and the warning for clusters that still share one CA.
package main

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/security"
)

// singleCAClusterWarningText is logged at every start of a server whose etcd
// trusts its cluster CA, or that has no gate HTTPS CA (D6).
const singleCAClusterWarningText = "this cluster uses one CA for etcd, the API and gates; " +
	"re-initialize with cca init to separate them (M280)"

// singleCAClusterWarning returns the single-CA warning for a server started with
// cluster CA files (--ca) whose etcd CA is the same certificate, or without a
// gate HTTPS CA; "" otherwise. The server keeps running either way.
func singleCAClusterWarning(parsedConfig serverCommandConfig) string {
	if parsedConfig.tlsCACertPath == "" {
		return ""
	}
	etcdSharesClusterCA := parsedConfig.etcdCACertPath != "" &&
		bytes.Equal(certificateFileIdentity(parsedConfig.etcdCACertPath), certificateFileIdentity(parsedConfig.tlsCACertPath))
	if etcdSharesClusterCA || parsedConfig.gateCACertPath == "" {
		return singleCAClusterWarningText
	}
	return ""
}

// certificateFileIdentity returns the DER of the first certificate in a PEM file,
// so a copy with or without a description line compares equal, or the raw
// content when the file holds no certificate block.
func certificateFileIdentity(filePath string) []byte {
	fileContent, readError := os.ReadFile(filePath) //nolint:gosec // operator-given CA path
	if readError != nil {
		return []byte("unreadable:" + filePath)
	}
	for remainingContent := fileContent; ; {
		var pemBlock *pem.Block
		pemBlock, remainingContent = pem.Decode(remainingContent)
		if pemBlock == nil {
			return fileContent
		}
		if pemBlock.Type == "CERTIFICATE" {
			return pemBlock.Bytes
		}
	}
}

// resolveGateCertificateIssuer returns the CA RouteTLSController issues gate
// certificates from: the gate HTTPS CA when --gate-ca-cert and --gate-ca-key are
// set, else the cluster CA (clusters from before M280), else nil. Exits on a load error.
func resolveGateCertificateIssuer(parsedConfig serverCommandConfig,
	clusterCertificateAuthority *security.CertificateAuthority) *security.CertificateAuthority {
	if parsedConfig.gateCACertPath == "" {
		return clusterCertificateAuthority
	}
	gateCertificateAuthority, loadError := security.LoadCertificateAuthorityFromFiles(parsedConfig.gateCACertPath, parsedConfig.gateCAKeyPath)
	if loadError != nil {
		fmt.Fprintf(os.Stderr, "error loading the gate HTTPS CA %s: %v\n", parsedConfig.gateCACertPath, loadError)
		os.Exit(1)
	}
	fmt.Printf("Gate certificates are issued by the gate HTTPS CA %s\n", parsedConfig.gateCACertPath)
	return gateCertificateAuthority
}

// loadEtcdAccessCertificateAuthority loads the etcd access CA from --etcd-ca and
// --etcd-access-ca-key, so enrollment can sign node etcd client certificates
// (D4); nil without the key (an older cluster). Exits on a load error.
func loadEtcdAccessCertificateAuthority(parsedConfig serverCommandConfig) *security.CertificateAuthority {
	if parsedConfig.etcdAccessCAKeyPath == "" {
		return nil
	}
	etcdAccessCA, loadError := security.LoadCertificateAuthorityFromFiles(parsedConfig.etcdCACertPath, parsedConfig.etcdAccessCAKeyPath)
	if loadError != nil {
		fmt.Fprintf(os.Stderr, "error loading the etcd access CA %s: %v\n", parsedConfig.etcdCACertPath, loadError)
		os.Exit(1)
	}
	fmt.Printf("Node enrollment issues etcd client certificates from the etcd access CA %s\n", parsedConfig.etcdCACertPath)
	return etcdAccessCA
}
