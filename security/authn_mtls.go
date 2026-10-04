// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"net/http"
	"time"
)

// defaultMTLSCertificateExpiry is used as the authentication result expiry
// when no explicit certificate NotAfter is available.
const defaultMTLSCertificateExpiry = 1 * time.Hour

// MTLSAuthenticator extracts the principal identity from a verified TLS client
// certificate. The certificate's CommonName is mapped to a "node:<CN>"
// principal. This authenticator returns nil (skip) when the request has no TLS
// connection or no peer certificates.
type MTLSAuthenticator struct{}

// NewMTLSAuthenticator creates an mTLS authenticator.
func NewMTLSAuthenticator() *MTLSAuthenticator {
	return &MTLSAuthenticator{}
}

// Authenticate extracts the CN from the first peer certificate and returns
// a "node:<CN>" principal. Returns nil result and nil error when no client
// certificate is present, allowing the chain to try the next authenticator.
func (authenticator *MTLSAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return nil, nil
	}
	peerCertificate := request.TLS.PeerCertificates[0]
	commonName := peerCertificate.Subject.CommonName
	if commonName == "" {
		return nil, nil
	}

	expiresAt := time.Now().Add(defaultMTLSCertificateExpiry)
	if !peerCertificate.NotAfter.IsZero() {
		expiresAt = peerCertificate.NotAfter
	}

	return &AuthenticationResult{
		Principal:     "node:" + commonName,
		PrincipalKind: PrincipalKindNode,
		ExpiresAt:     expiresAt,
	}, nil
}
