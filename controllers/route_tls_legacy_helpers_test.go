// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
)

// issueOldGateCertificateWithClientAuth issues a gate certificate the way
// releases before M280 did: ServerAuth and ClientAuth from the given CA.
func issueOldGateCertificateWithClientAuth(t *testing.T, certificateAuthority *security.CertificateAuthority, host string) []byte {
	t.Helper()
	issued, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{Usage: security.UsageServerAndClient,
		CommonName: host, DNSNames: []string{host}, TTL: 90 * 24 * time.Hour,
	})
	if issueError != nil {
		t.Fatal(issueError)
	}
	return issued.CertificatePEM
}
