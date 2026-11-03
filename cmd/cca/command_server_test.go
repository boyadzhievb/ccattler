// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestBuildAgentDebugClientTLSConfigWithoutTLS(testContext *testing.T) {
	if buildAgentDebugClientTLSConfig(nil) != nil {
		testContext.Fatal("no server TLS must mean no agent debug client TLS")
	}
}

func TestBuildAgentDebugClientTLSConfigStaticCertificate(testContext *testing.T) {
	clusterRoots := x509.NewCertPool()
	staticCertificate := tls.Certificate{Certificate: [][]byte{{0x01}}}
	clientTLSConfig := buildAgentDebugClientTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{staticCertificate},
		ClientCAs:    clusterRoots,
	})
	if clientTLSConfig.RootCAs != clusterRoots {
		testContext.Fatal("agent debug client must trust the cluster CA used for client certificates")
	}
	if len(clientTLSConfig.Certificates) != 1 || clientTLSConfig.GetClientCertificate != nil {
		testContext.Fatal("static server certificate must be presented as the client certificate")
	}
}

func TestBuildAgentDebugClientTLSConfigRotatedCertificate(testContext *testing.T) {
	rotatedCertificate := &tls.Certificate{Certificate: [][]byte{{0x02}}}
	clientTLSConfig := buildAgentDebugClientTLSConfig(&tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return rotatedCertificate, nil },
	})
	presentedCertificate, getError := clientTLSConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
	if getError != nil || presentedCertificate != rotatedCertificate {
		testContext.Fatalf("rotated server certificate must be presented as the client certificate, got %v, %v", presentedCertificate, getError)
	}
}
