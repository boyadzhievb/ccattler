// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package security implements CCattler's built-in certificate authority and
// mTLS infrastructure. It provides automatic certificate issuance, rotation,
// and verification for all cluster communication.
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

// certificateNotBeforeClockSkew is the negative offset applied to the NotBefore
// field of issued certificates to tolerate minor clock skew between nodes.
const certificateNotBeforeClockSkew = -1 * time.Minute

// CertificateAuthority is the cluster's internal CA that issues short-lived
// certificates for nodes, controllers, and the API server.
type CertificateAuthority struct {
	caCertificate    *x509.Certificate
	caPrivateKey     *ecdsa.PrivateKey
	caCertificatePEM []byte
	serialCounter    int64
	mutex            sync.Mutex
}

// CertificateAuthorityKind names the purpose of one of the cluster's CAs (M280
// D1, D1a). It fills the CA's Subject OU and CN; it never authorizes anything.
type CertificateAuthorityKind string

const (
	// ClusterIdentityCertificateAuthority signs the API server and node identities the API trusts.
	ClusterIdentityCertificateAuthority CertificateAuthorityKind = "cluster identity"
	// EtcdAccessCertificateAuthority signs etcd's serving certificate and the etcd client certificates.
	EtcdAccessCertificateAuthority CertificateAuthorityKind = "etcd access"
	// GateHTTPSCertificateAuthority signs gate serving certificates; neither the API nor etcd trusts it.
	GateHTTPSCertificateAuthority CertificateAuthorityKind = "gate HTTPS"
)

// NewCertificateAuthority generates a self-signed root CA of the given kind,
// valid for the given duration. Its Subject is O=CCattler, OU="<kind> CA",
// CN="CCattler <kind> CA".
func NewCertificateAuthority(kind CertificateAuthorityKind, validityDuration time.Duration) (*CertificateAuthority, error) {
	caPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:       []string{subjectOrganization},
			OrganizationalUnit: []string{string(kind) + " CA"},
			CommonName:         "CCattler " + string(kind) + " CA",
		},
		NotBefore:             time.Now().Add(certificateNotBeforeClockSkew),
		NotAfter:              time.Now().Add(validityDuration),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	caCertificateBytes, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	caCertificate, err := x509.ParseCertificate(caCertificateBytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	caCertificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertificateBytes,
	})

	return &CertificateAuthority{
		caCertificate:    caCertificate,
		caPrivateKey:     caPrivateKey,
		caCertificatePEM: caCertificatePEM,
	}, nil
}

// LoadCertificateAuthorityFromFiles loads an existing CA from PEM-encoded
// certificate and private key files on disk. Used when the server is started
// with --cert/--key/--ca/--ca-key to enable node enrollment.
func LoadCertificateAuthorityFromFiles(certPath, keyPath string) (*CertificateAuthority, error) {
	certPEM, readError := os.ReadFile(certPath) //nolint:gosec // reads user-specified config file
	if readError != nil {
		return nil, fmt.Errorf("read CA certificate: %w", readError)
	}
	keyPEM, readError := os.ReadFile(keyPath) //nolint:gosec // reads user-specified config file
	if readError != nil {
		return nil, fmt.Errorf("read CA key: %w", readError)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("no PEM block found in CA certificate file")
	}
	parsedCertificate, parseError := x509.ParseCertificate(certBlock.Bytes)
	if parseError != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", parseError)
	}

	var keyBlock *pem.Block
	remainingKeyPEM := keyPEM
	for {
		keyBlock, remainingKeyPEM = pem.Decode(remainingKeyPEM)
		if keyBlock == nil {
			return nil, fmt.Errorf("no private key PEM block found in CA key file")
		}
		if keyBlock.Type == "EC PRIVATE KEY" || keyBlock.Type == "PRIVATE KEY" {
			break
		}
	}
	parsedKey, parseError := parseECPrivateKeyBlock(keyBlock)
	if parseError != nil {
		return nil, fmt.Errorf("parse CA private key: %w", parseError)
	}

	return &CertificateAuthority{
		caCertificate:    parsedCertificate,
		caPrivateKey:     parsedKey,
		caCertificatePEM: certPEM,
	}, nil
}

// parseECPrivateKeyBlock extracts an ECDSA private key from a PEM block.
// Handles both SEC 1 (EC PRIVATE KEY) and PKCS#8 (PRIVATE KEY) encodings.
func parseECPrivateKeyBlock(keyBlock *pem.Block) (*ecdsa.PrivateKey, error) {
	if ecKey, ecError := x509.ParseECPrivateKey(keyBlock.Bytes); ecError == nil {
		return ecKey, nil
	}
	genericKey, pkcs8Error := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if pkcs8Error != nil {
		return nil, fmt.Errorf("key is neither SEC1 nor PKCS#8 format")
	}
	ecKey, isEC := genericKey.(*ecdsa.PrivateKey)
	if !isEC {
		return nil, fmt.Errorf("PKCS#8 key is not ECDSA")
	}
	return ecKey, nil
}

// CACertificatePEM returns the PEM-encoded CA certificate for distribution
// to nodes that need to verify peer certificates.
func (certificateAuthority *CertificateAuthority) CACertificatePEM() []byte {
	return certificateAuthority.caCertificatePEM
}

// CAPrivateKeyPEM returns the PEM-encoded CA private key. Used by cca init
// to write the key to disk so the server can reload it later.
func (certificateAuthority *CertificateAuthority) CAPrivateKeyPEM() ([]byte, error) {
	keyBytes, marshalError := x509.MarshalECPrivateKey(certificateAuthority.caPrivateKey)
	if marshalError != nil {
		return nil, fmt.Errorf("marshal CA private key: %w", marshalError)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), nil
}

// subjectOrganization is the Subject O of every certificate CCattler issues.
const subjectOrganization = "CCattler"

// CertificateUsage is the TLS role an issued certificate may play (M280 D2).
// Its zero value is no usage: IssueCertificate refuses it, so every caller
// chooses. Go's TLS stack and etcd refuse a client certificate without ClientAuth.
type CertificateUsage int

const (
	// UsageServer is ServerAuth only: gate certificates.
	UsageServer CertificateUsage = iota + 1
	// UsageClient is ClientAuth only: node identities and etcd clients.
	UsageClient
	// UsageServerAndClient is both: the API server (also a client of the agent debug
	// API) and etcd's serving and peer certificates (etcd dials itself and its peers).
	UsageServerAndClient
)

// extendedKeyUsages returns the ExtKeyUsage list of the usage, or an error for
// the zero value or an unknown one.
func (usage CertificateUsage) extendedKeyUsages() ([]x509.ExtKeyUsage, error) {
	switch usage {
	case UsageServer:
		return []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil
	case UsageClient:
		return []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil
	case UsageServerAndClient:
		return []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil
	}
	return nil, fmt.Errorf("certificate usage required (UsageServer, UsageClient or UsageServerAndClient), got %d", usage)
}

// CertificateSubjectUnit is the descriptive Subject OU of a leaf certificate
// (M280 D1a). Authorization uses the CommonName only; the OU never authorizes.
type CertificateSubjectUnit string

const (
	// SubjectUnitAPIServerIdentity marks the API server's certificate (CN ccattler-server or a static one).
	SubjectUnitAPIServerIdentity CertificateSubjectUnit = "API server identity"
	// SubjectUnitNodeIdentityForAPI marks a node's client identity to the API.
	SubjectUnitNodeIdentityForAPI CertificateSubjectUnit = "node identity for API"
	// SubjectUnitEtcdServing marks etcd's serving certificate.
	SubjectUnitEtcdServing CertificateSubjectUnit = "etcd serving certificate"
	// SubjectUnitEtcdPeer marks the certificate HA etcd members present to each other.
	SubjectUnitEtcdPeer CertificateSubjectUnit = "etcd peer certificate"
	// SubjectUnitCCAServerEtcdClient marks cca server's etcd client certificate.
	SubjectUnitCCAServerEtcdClient CertificateSubjectUnit = "cca-server etcd client"
	// SubjectUnitNodeEtcdClient marks one node's etcd client certificate.
	SubjectUnitNodeEtcdClient CertificateSubjectUnit = "node etcd client"
	// SubjectUnitGateHTTPS marks a gate serving certificate.
	SubjectUnitGateHTTPS CertificateSubjectUnit = "gate HTTPS"
)

// leafSubject returns the Subject of a leaf certificate: O=CCattler, the OU
// when given, and the CommonName.
func leafSubject(commonName string, subjectUnit CertificateSubjectUnit) pkix.Name {
	subject := pkix.Name{Organization: []string{subjectOrganization}, CommonName: commonName}
	if subjectUnit != "" {
		subject.OrganizationalUnit = []string{string(subjectUnit)}
	}
	return subject
}

// IssueCertificateRequest holds the parameters for issuing a new certificate.
type IssueCertificateRequest struct {
	CommonName  string                 // identity the certificate names (the principal for client usage)
	SubjectUnit CertificateSubjectUnit // descriptive OU (D1a)
	Usage       CertificateUsage       // required TLS role; the zero value is refused (D2)
	DNSNames    []string               // DNS SANs
	IPAddresses []net.IP               // IP SANs
	TTL         time.Duration          // validity from now
}

// IssuedCertificate contains the PEM-encoded certificate and private key.
type IssuedCertificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	NotAfter       time.Time
}

// IssueCertificate creates a new certificate signed by this CA. The certificate
// is valid for the requested TTL and includes the specified identifiers.
func (certificateAuthority *CertificateAuthority) IssueCertificate(request IssueCertificateRequest) (*IssuedCertificate, error) {
	extendedKeyUsages, usageError := request.Usage.extendedKeyUsages()
	if usageError != nil {
		return nil, fmt.Errorf("issue certificate for %q: %w", request.CommonName, usageError)
	}
	certificateAuthority.mutex.Lock()
	certificateAuthority.serialCounter++
	certificateAuthority.mutex.Unlock()

	leafPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	notAfter := time.Now().Add(request.TTL)

	leafTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      leafSubject(request.CommonName, request.SubjectUnit),
		DNSNames:     request.DNSNames,
		IPAddresses:  request.IPAddresses,
		NotBefore:    time.Now().Add(certificateNotBeforeClockSkew),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  extendedKeyUsages,
	}

	leafCertificateBytes, err := x509.CreateCertificate(
		rand.Reader, leafTemplate, certificateAuthority.caCertificate,
		&leafPrivateKey.PublicKey, certificateAuthority.caPrivateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	leafCertificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: leafCertificateBytes,
	})

	leafPrivateKeyBytes, err := x509.MarshalECPrivateKey(leafPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}

	leafPrivateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: leafPrivateKeyBytes,
	})

	return &IssuedCertificate{
		CertificatePEM: leafCertificatePEM,
		PrivateKeyPEM:  leafPrivateKeyPEM,
		NotAfter:       notAfter,
	}, nil
}

// ServerTLSConfig creates a tls.Config suitable for a server that requires
// mutual TLS. It uses the given certificate and verifies clients against the CA.
func (certificateAuthority *CertificateAuthority) ServerTLSConfig(serverCertificate *IssuedCertificate) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(serverCertificate.CertificatePEM, serverCertificate.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse server keypair: %w", err)
	}

	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(certificateAuthority.caCertificatePEM)

	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    certPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig creates a tls.Config suitable for a client that presents
// a certificate and verifies the server against the CA.
func (certificateAuthority *CertificateAuthority) ClientTLSConfig(clientCertificate *IssuedCertificate) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(clientCertificate.CertificatePEM, clientCertificate.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client keypair: %w", err)
	}

	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(certificateAuthority.caCertificatePEM)

	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// SignCSRRequest holds the parameters for signing a certificate signing request.
// The CSR carries the subject's public key; the CA never generates or handles
// the private key. The CA sets the identity: the CSR subject and SANs are ignored.
type SignCSRRequest struct {
	CSR         *x509.CertificateRequest // parsed CSR from the enrolling node; only its public key is used
	TTL         time.Duration            // how long the signed certificate should be valid
	CommonName  string                   // certificate CN chosen by the CA (the validated node ID)
	SubjectUnit CertificateSubjectUnit   // descriptive OU chosen by the CA (D1a)
	DNSNames    []string                 // DNS SANs chosen by the CA
	IPAddresses []net.IP                 // IP SANs chosen by the CA
}

// SignCSRResponse contains the PEM-encoded certificate signed from a CSR.
type SignCSRResponse struct {
	CertificatePEM []byte    // PEM-encoded leaf certificate
	NotAfter       time.Time // certificate expiry
}

// SignCSR validates the CSR signature and signs a client-auth leaf certificate
// for the CSR's public key. Subject and SANs come from the request, never from
// the CSR, so a node cannot choose its own principal or a server identity.
func (certificateAuthority *CertificateAuthority) SignCSR(signRequest SignCSRRequest) (*SignCSRResponse, error) {
	if err := signRequest.CSR.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature verification failed: %w", err)
	}
	if signRequest.CommonName == "" {
		return nil, fmt.Errorf("sign CSR: common name required")
	}

	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	notAfter := time.Now().Add(signRequest.TTL)
	leafTemplate := buildCSRLeafTemplate(serialNumber, leafSubject(signRequest.CommonName, signRequest.SubjectUnit),
		signRequest.DNSNames, signRequest.IPAddresses, notAfter)

	leafCertificateBytes, err := x509.CreateCertificate(
		rand.Reader, leafTemplate, certificateAuthority.caCertificate,
		signRequest.CSR.PublicKey, certificateAuthority.caPrivateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("sign CSR: %w", err)
	}

	leafCertificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: leafCertificateBytes,
	})

	return &SignCSRResponse{
		CertificatePEM: leafCertificatePEM,
		NotAfter:       notAfter,
	}, nil
}

// buildCSRLeafTemplate creates the x509.Certificate template used when signing
// a CSR. Enrolled certificates (a node's API identity, its etcd client
// certificate) are client-auth only and never usable as a server certificate.
func buildCSRLeafTemplate(
	serialNumber *big.Int, subject pkix.Name,
	dnsNames []string, ipAddresses []net.IP, notAfter time.Time,
) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      subject,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
		NotBefore:    time.Now().Add(certificateNotBeforeClockSkew),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

// IsIssuerOf reports whether this CA signed the certificate. RouteTLSController
// uses it to re-issue gate certificates from another issuer (M280 D3).
func (certificateAuthority *CertificateAuthority) IsIssuerOf(certificate *x509.Certificate) bool {
	return certificate.CheckSignatureFrom(certificateAuthority.caCertificate) == nil
}

// generateSerialNumber returns a random serial number for X.509 certificates.
func generateSerialNumber() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}
