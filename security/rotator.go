package security

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"
)

// CertificateRotator automatically renews a certificate before it expires.
// It holds the current certificate and atomically swaps it when renewal occurs.
type CertificateRotator struct {
	certificateAuthority *CertificateAuthority
	request              IssueCertificateRequest
	currentCertificate   *IssuedCertificate
	currentTLSCert       *tls.Certificate
	renewalThreshold     float64
	mutex                sync.RWMutex
	stopChannel          chan struct{}
}

// NewCertificateRotator creates a rotator that issues certificates using the
// given CA and request parameters. It renews when the certificate reaches
// the given fraction of its lifetime (e.g. 0.7 means renew at 70% of TTL).
func NewCertificateRotator(
	certificateAuthority *CertificateAuthority,
	request IssueCertificateRequest,
	renewalThreshold float64,
) (*CertificateRotator, error) {
	rotator := &CertificateRotator{
		certificateAuthority: certificateAuthority,
		request:              request,
		renewalThreshold:     renewalThreshold,
		stopChannel:          make(chan struct{}),
	}

	if err := rotator.renewCertificate(); err != nil {
		return nil, fmt.Errorf("initial certificate issuance: %w", err)
	}

	return rotator, nil
}

// GetCertificate returns the current TLS certificate. This is suitable for
// use as tls.Config.GetCertificate or GetClientCertificate.
func (rotator *CertificateRotator) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	rotator.mutex.RLock()
	defer rotator.mutex.RUnlock()
	return rotator.currentTLSCert, nil
}

// GetClientCertificate returns the current TLS certificate for client auth.
func (rotator *CertificateRotator) GetClientCertificate(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	rotator.mutex.RLock()
	defer rotator.mutex.RUnlock()
	return rotator.currentTLSCert, nil
}

// CurrentCertificate returns the current issued certificate.
func (rotator *CertificateRotator) CurrentCertificate() *IssuedCertificate {
	rotator.mutex.RLock()
	defer rotator.mutex.RUnlock()
	return rotator.currentCertificate
}

// Start begins the background renewal loop. It checks periodically and renews
// the certificate when it reaches the renewal threshold.
func (rotator *CertificateRotator) Start(ctx context.Context) {
	go rotator.renewalLoop(ctx)
}

// Stop ends the background renewal loop.
func (rotator *CertificateRotator) Stop() {
	close(rotator.stopChannel)
}

// renewalLoop runs in the background, sleeping until the certificate needs
// renewal and then issuing a new one.
func (rotator *CertificateRotator) renewalLoop(ctx context.Context) {
	for {
		rotator.mutex.RLock()
		currentNotAfter := rotator.currentCertificate.NotAfter
		rotator.mutex.RUnlock()

		totalLifetime := time.Until(currentNotAfter) + rotator.request.TTL
		renewalDelay := time.Duration(float64(totalLifetime) * rotator.renewalThreshold)
		sleepDuration := time.Until(currentNotAfter) - (rotator.request.TTL - renewalDelay)
		if sleepDuration < 10*time.Second {
			sleepDuration = 10 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-rotator.stopChannel:
			return
		case <-time.After(sleepDuration):
			rotator.renewCertificate()
		}
	}
}

// renewCertificate issues a new certificate and atomically replaces the
// current one.
func (rotator *CertificateRotator) renewCertificate() error {
	issuedCertificate, err := rotator.certificateAuthority.IssueCertificate(rotator.request)
	if err != nil {
		return fmt.Errorf("renew certificate: %w", err)
	}

	tlsCertificate, err := tls.X509KeyPair(issuedCertificate.CertificatePEM, issuedCertificate.PrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("parse renewed keypair: %w", err)
	}

	rotator.mutex.Lock()
	rotator.currentCertificate = issuedCertificate
	rotator.currentTLSCert = &tlsCertificate
	rotator.mutex.Unlock()

	return nil
}
