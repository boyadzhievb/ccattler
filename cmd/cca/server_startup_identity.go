// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Identity files cca server checks before it creates the store (M280 rework 4),
// and the control-plane node's enrollment record (Flow 27).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// serverCAFlag is one CA file given to cca server and the kind it must be.
type serverCAFlag struct {
	flagName        string                            // the flag, for the error message
	certificatePath string                            // the CA certificate file; "" when the flag is absent
	wantKind        security.CertificateAuthorityKind // the kind its Subject OU must name, if it names one
}

// verifiedNodeIdentity is a --control-plane-node-cert that passed verification.
type verifiedNodeIdentity struct {
	certificatePath string            // the flag's file, named in startup errors
	certificate     *x509.Certificate // the verified node certificate (CN = node ID)
}

// checkServerIdentityFiles checks the CA files' kinds and verifies
// --control-plane-node-cert before the store is created; it returns the verified
// node identity (nil without the flag). cca server exits on an error.
func checkServerIdentityFiles(parsedConfig serverCommandConfig) (*verifiedNodeIdentity, error) {
	if kindError := checkServerCAKinds(parsedConfig); kindError != nil {
		return nil, kindError
	}
	return resolveControlPlaneNodeIdentity(parsedConfig)
}

// checkServerCAKinds refuses a CA file in which any certificate's Subject OU
// names another kind than its flag's, e.g. etcd-access-ca.pem as --gate-ca-cert
// or bundled into --ca (SA3-L2, SA4-L2). A CA without a kind is accepted.
func checkServerCAKinds(parsedConfig serverCommandConfig) error {
	for _, caFlag := range []serverCAFlag{
		{"--ca", parsedConfig.tlsCACertPath, security.ClusterIdentityCertificateAuthority},
		{"--etcd-ca", parsedConfig.etcdCACertPath, security.EtcdAccessCertificateAuthority},
		{"--gate-ca-cert", parsedConfig.gateCACertPath, security.GateHTTPSCertificateAuthority},
	} {
		if caFlag.certificatePath == "" {
			continue
		}
		if kindError := checkCAFileKind(caFlag); kindError != nil {
			return fmt.Errorf("%s %s: %w", caFlag.flagName, caFlag.certificatePath, kindError)
		}
	}
	return nil
}

// checkCAFileKind checks every certificate of one CA file: none may name a kind
// other than the flag's. A file without a certificate is an error.
func checkCAFileKind(caFlag serverCAFlag) error {
	caFileContent, readError := os.ReadFile(caFlag.certificatePath) //nolint:gosec // operator-given path
	if readError != nil {
		return readError
	}
	certificateDERs := pemCertificateDERs(caFileContent)
	if len(certificateDERs) == 0 {
		return errors.New("no PEM certificate")
	}
	for _, certificateDER := range certificateDERs {
		caCertificate, parseError := x509.ParseCertificate(certificateDER)
		if parseError != nil {
			return parseError
		}
		if loadedKind, hasKind := security.CertificateAuthorityKindOf(caCertificate); hasKind && loadedKind != caFlag.wantKind {
			return fmt.Errorf("holds the %s CA, not the %s CA", loadedKind, caFlag.wantKind)
		}
	}
	return nil
}

// resolveControlPlaneNodeIdentity loads --control-plane-node-cert and verifies
// it against --ca; nil without the flag. The error names the path and the
// reason: a given but invalid identity stops the server (user decision 1).
func resolveControlPlaneNodeIdentity(parsedConfig serverCommandConfig) (*verifiedNodeIdentity, error) {
	certificatePath := parsedConfig.controlPlaneNodeCertificatePath
	if certificatePath == "" {
		return nil, nil
	}
	nodeCertificate, loadError := readFirstPEMCertificateFile(certificatePath)
	if loadError == nil {
		loadError = verifyControlPlaneNodeCertificate(nodeCertificate, parsedConfig.tlsCACertPath)
	}
	if loadError != nil {
		return nil, fmt.Errorf("--control-plane-node-cert %s is not a valid node identity of this cluster: %w", certificatePath, loadError)
	}
	return &verifiedNodeIdentity{certificatePath: certificatePath, certificate: nodeCertificate}, nil
}

// readFirstPEMCertificateFile parses the first certificate of a PEM file.
func readFirstPEMCertificateFile(certificatePath string) (*x509.Certificate, error) {
	certificatePEM, readError := os.ReadFile(certificatePath) //nolint:gosec // operator-given path
	if readError != nil {
		return nil, readError
	}
	return parseFirstPEMCertificate(certificatePEM)
}

// verifyControlPlaneNodeCertificate checks that the node identity chains to the
// cluster identity CA (--ca), is valid now, is a client certificate and names a
// valid, unreserved node ID.
func verifyControlPlaneNodeCertificate(nodeCertificate *x509.Certificate, clusterCAPath string) error {
	if clusterCAPath == "" {
		return errors.New("--ca is required to verify it")
	}
	clusterCAPEM, readError := os.ReadFile(clusterCAPath) //nolint:gosec // operator-given path
	if readError != nil {
		return readError
	}
	clusterCAPool := x509.NewCertPool()
	if !clusterCAPool.AppendCertsFromPEM(clusterCAPEM) {
		return fmt.Errorf("%s holds no CA certificate", clusterCAPath)
	}
	if _, verifyError := nodeCertificate.Verify(x509.VerifyOptions{Roots: clusterCAPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); verifyError != nil {
		return verifyError
	}
	return security.ValidateNodeIdentity(nodeCertificate.Subject.CommonName)
}

// bootstrapControlPlaneNodeEnrollment records this host's node as enrolled so no
// unbound token can enroll as it (Flow 27): the verified --control-plane-node-cert
// (a failed record is an error naming the file), else on old clusters the server
// certificate's CN, never ccattler-server; there a failure only warns.
func bootstrapControlPlaneNodeEnrollment(factStore store.StateStore, serverTLSConfig *tls.Config, verifiedIdentity *verifiedNodeIdentity) error {
	if verifiedIdentity != nil {
		if recordError := recordControlPlaneNodeEnrollment(factStore, verifiedIdentity.certificate); recordError != nil {
			return fmt.Errorf("--control-plane-node-cert %s: %w", verifiedIdentity.certificatePath, recordError)
		}
		return nil
	}
	serverLeafCertificate := serverLeafCertificateForEnrollment(serverTLSConfig)
	if serverLeafCertificate == nil || serverLeafCertificate.Subject.CommonName == "" {
		return nil
	}
	if serverLeafCertificate.Subject.CommonName == security.ServerCertificateCommonName {
		logging.Default().Warn("control-plane node not recorded as enrolled: the server certificate names the API server, not a node; " +
			"start cca server with --control-plane-node-cert node-identity-for-api.pem (as setup.sh does)")
		return nil
	}
	if enrollError := recordControlPlaneNodeEnrollment(factStore, serverLeafCertificate); enrollError != nil {
		logging.Default().Warn("control-plane node not recorded as enrolled", "error", enrollError.Error())
	}
	return nil
}

// recordControlPlaneNodeEnrollment writes the node's enrollment record
// (create-only) and reads it back: an error means the record is not confirmed
// in the store (SA3-L1).
func recordControlPlaneNodeEnrollment(factStore store.StateStore, nodeCertificate *x509.Certificate) error {
	nodeID := nodeCertificate.Subject.CommonName
	recordContext := context.Background()
	if enrollError := security.EnsureControlPlaneNodeEnrolled(recordContext, factStore, nodeID, nodeCertificate.NotAfter); enrollError != nil {
		return fmt.Errorf("control-plane node %q not recorded as enrolled: %w", nodeID, enrollError)
	}
	if _, readError := factStore.Get(recordContext, security.EnrolledNodePrefix+nodeID); readError != nil {
		return fmt.Errorf("control-plane node %q enrollment record not confirmed: %w", nodeID, readError)
	}
	return nil
}
