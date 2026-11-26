// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
)

// tlsAlertRecordHeader is a TLS record header for one fatal alert: content type
// 21 (alert), legacy version 3.3, length 2; the alert level and description follow.
var tlsAlertRecordHeader = []byte{0x15, 0x03, 0x03, 0x00, 0x02}

// tlsAlertLevelFatal is the TLS alert level that ends the connection.
const tlsAlertLevelFatal = 0x02

// serveOneTLSAlert accepts one connection on listener, reads the ClientHello and
// answers with a fatal TLS alert of alertDescription, as a server does when it
// ends the handshake.
func serveOneTLSAlert(t *testing.T, listener net.Listener, alertDescription byte) {
	t.Helper()
	go func() {
		clientConnection, acceptError := listener.Accept()
		if acceptError != nil {
			return
		}
		defer func() { _ = clientConnection.Close() }()
		_ = clientConnection.SetDeadline(time.Now().Add(clusterPKITestRequestTimeout))
		clientHello := make([]byte, 4096)
		_, _ = clientConnection.Read(clientHello)
		_, _ = clientConnection.Write(append(append([]byte{}, tlsAlertRecordHeader...), tlsAlertLevelFatal, alertDescription))
	}()
}

// TestTLSRefusalCountsOnlyCertificateAlerts (CR5-S3): the helper the CA
// separation tests rely on counts bad certificate, unknown CA and certificate
// required as a refusal of the client certificate, and no other TLS alert.
func TestTLSRefusalCountsOnlyCertificateAlerts(t *testing.T) {
	serverCA, caError := security.NewCertificateAuthority(security.ClusterIdentityCertificateAuthority, clusterPKITestTTL)
	if caError != nil {
		t.Fatal(caError)
	}
	alertCases := []struct {
		alertName        string // the alert, as RFC 8446 names it
		alertDescription byte   // its code on the wire
		wantRefusal      bool   // whether it refuses the client certificate
	}{
		{"bad_certificate", 42, true},
		{"unknown_ca", 48, true},
		{"certificate_required", 116, true},
		{"handshake_failure", 40, false},
		{"protocol_version", 70, false},
		{"internal_error", 80, false},
	}
	for _, alertCase := range alertCases {
		t.Run(alertCase.alertName, func(t *testing.T) {
			listener, listenError := net.Listen("tcp", "127.0.0.1:0")
			if listenError != nil {
				t.Fatal(listenError)
			}
			defer func() { _ = listener.Close() }()
			serveOneTLSAlert(t, listener, alertCase.alertDescription)
			refused, observed := tlsServerRefusesClientCertificate(t, listener.Addr().String(), serverCA.CACertificatePEM(), tls.Certificate{})
			if !strings.Contains(observed, "remote error: tls:") {
				t.Fatalf("the probe did not receive the alert: %s", observed)
			}
			if refused != alertCase.wantRefusal {
				t.Errorf("alert %s: refusal = %v, want %v (%s)", alertCase.alertName, refused, alertCase.wantRefusal, observed)
			}
		})
	}
}
