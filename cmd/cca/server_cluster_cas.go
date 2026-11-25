// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Server side of the three cluster CAs (M280 D3, D6): which CA issues gate
// certificates, and the warning for clusters that still share one CA.
package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// singleCAClusterWarningText is logged at every start of a server whose CAs are
// not separated: etcd or the gates share the cluster CA, or one is missing (D6).
const singleCAClusterWarningText = "this cluster uses one CA for etcd, the API and gates; " +
	"re-initialize with cca init to separate them (M280)"

// singleCAClusterWarning returns the single-CA warning, or "" for a server whose
// etcd CA and gate CA are both given and neither holds the cluster CA. Auto
// --tls (one generated CA for the API and gates) also warns; the server keeps running.
func singleCAClusterWarning(parsedConfig serverCommandConfig) string {
	if parsedConfig.tlsCACertPath == "" {
		if parsedConfig.tlsEnabled {
			return singleCAClusterWarningText
		}
		return ""
	}
	if parsedConfig.etcdCACertPath == "" || parsedConfig.gateCACertPath == "" ||
		certificateFilesShareCertificate(parsedConfig.etcdCACertPath, parsedConfig.tlsCACertPath) ||
		certificateFilesShareCertificate(parsedConfig.gateCACertPath, parsedConfig.tlsCACertPath) {
		return singleCAClusterWarningText
	}
	return ""
}

// certificateFilesShareCertificate reports whether two PEM files hold a common
// certificate (bundles included; description lines do not matter).
func certificateFilesShareCertificate(firstPath string, secondPath string) bool {
	secondCertificates := certificateFileDERs(secondPath)
	for _, firstCertificate := range certificateFileDERs(firstPath) {
		for _, secondCertificate := range secondCertificates {
			if bytes.Equal(firstCertificate, secondCertificate) {
				return true
			}
		}
	}
	return false
}

// certificateFileDERs returns the DER of every certificate in a PEM file, or the
// raw content as the one entry when it holds none (an unreadable file: its path).
func certificateFileDERs(filePath string) [][]byte {
	fileContent, readError := os.ReadFile(filePath) //nolint:gosec // operator-given CA path
	if readError != nil {
		return [][]byte{[]byte("unreadable:" + filePath)}
	}
	certificateDERs := pemCertificateDERs(fileContent)
	if len(certificateDERs) == 0 {
		return [][]byte{fileContent}
	}
	return certificateDERs
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

// parseFirstPEMCertificate parses the first CERTIFICATE block of PEM data,
// skipping a description line or other blocks before it.
func parseFirstPEMCertificate(pemData []byte) (*x509.Certificate, error) {
	certificateDERs := pemCertificateDERs(pemData)
	if len(certificateDERs) == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(certificateDERs[0])
}

// pemCertificateDERs returns the DER of every CERTIFICATE block in PEM data,
// in order; text before or between blocks is skipped.
func pemCertificateDERs(pemData []byte) [][]byte {
	var certificateDERs [][]byte
	for remainingData := pemData; ; {
		var pemBlock *pem.Block
		pemBlock, remainingData = pem.Decode(remainingData)
		if pemBlock == nil {
			return certificateDERs
		}
		if pemBlock.Type == "CERTIFICATE" {
			certificateDERs = append(certificateDERs, pemBlock.Bytes)
		}
	}
}

// buildRouteTLSController returns RouteTLSController issuing from the gate
// issuer resolveGateCertificateIssuer picks; nil without any CA (no TLS).
func buildRouteTLSController(parsedConfig serverCommandConfig, clusterCertificateAuthority *security.CertificateAuthority,
	envelopeCodec security.SecretEnvelopeCodec) *controllers.RouteTLSController {
	gateCertificateIssuer := resolveGateCertificateIssuer(parsedConfig, clusterCertificateAuthority)
	if gateCertificateIssuer == nil {
		return nil
	}
	return controllers.NewRouteTLSController(gateCertificateIssuer, envelopeCodec)
}

// buildEnrollmentService returns the node enrollment service: node identities
// from the cluster CA and, with --etcd-access-ca-key, each node's etcd client
// certificate from the etcd access CA (D4).
func buildEnrollmentService(factStore store.StateStore, clusterCertificateAuthority *security.CertificateAuthority,
	rbacAuthorizer *security.RBACAuthorizer, parsedConfig serverCommandConfig) *security.EnrollmentService {
	enrollmentService := security.NewEnrollmentService(factStore, clusterCertificateAuthority, rbacAuthorizer, defaultNodeCertificateTTL)
	if etcdAccessCA := loadEtcdAccessCertificateAuthority(parsedConfig); etcdAccessCA != nil {
		enrollmentService.SetEtcdClientCertificateAuthority(etcdAccessCA)
	}
	return enrollmentService
}
