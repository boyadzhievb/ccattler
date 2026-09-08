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
	"sync"
	"time"
)

// CertificateAuthority is the cluster's internal CA that issues short-lived
// certificates for nodes, controllers, and the API server.
type CertificateAuthority struct {
	caCertificate    *x509.Certificate
	caPrivateKey     *ecdsa.PrivateKey
	caCertificatePEM []byte
	serialCounter    int64
	mutex            sync.Mutex
}

// NewCertificateAuthority generates a self-signed root CA certificate and key.
// The CA is valid for the given duration.
func NewCertificateAuthority(validityDuration time.Duration) (*CertificateAuthority, error) {
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
			Organization: []string{"CCattler"},
			CommonName:   "CCattler Root CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
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

// CACertificatePEM returns the PEM-encoded CA certificate for distribution
// to nodes that need to verify peer certificates.
func (certificateAuthority *CertificateAuthority) CACertificatePEM() []byte {
	return certificateAuthority.caCertificatePEM
}

// IssueCertificateRequest holds the parameters for issuing a new certificate.
type IssueCertificateRequest struct {
	CommonName  string
	DNSNames    []string
	IPAddresses []net.IP
	TTL         time.Duration
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
		Subject: pkix.Name{
			Organization: []string{"CCattler"},
			CommonName:   request.CommonName,
		},
		DNSNames:    request.DNSNames,
		IPAddresses: request.IPAddresses,
		NotBefore:   time.Now().Add(-1 * time.Minute),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
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

// generateSerialNumber returns a random serial number for X.509 certificates.
func generateSerialNumber() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}
