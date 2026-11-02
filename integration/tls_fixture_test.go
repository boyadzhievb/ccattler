// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
)

// prepareManualTLSFixtures returns the base directory to compile a .cca from.
// Gates with manual TLS reference cert/key files that are generated at deploy
// time and never committed; for those, it writes a throwaway self-signed pair
// at the referenced relative paths in a temp dir and returns that dir.
func prepareManualTLSFixtures(testContext *testing.T, dslContent, originalBaseDir string) string {
	testContext.Helper()
	parsedFile, parseError := lang.Parse(dslContent)
	if parseError != nil {
		return originalBaseDir // the apply step reports the parse error
	}

	fixtureDirectory := ""
	for _, routeDecl := range parsedFile.Routes {
		if routeDecl.TLS == nil || routeDecl.TLS.CertFile == "" || filepath.IsAbs(routeDecl.TLS.CertFile) {
			continue
		}
		if fixtureDirectory == "" {
			fixtureDirectory = testContext.TempDir()
		}
		certificatePEM, keyPEM := generateFixtureCertificatePEM(testContext, routeDecl.Host)
		writeFixtureFile(testContext, filepath.Join(fixtureDirectory, routeDecl.TLS.CertFile), certificatePEM)
		writeFixtureFile(testContext, filepath.Join(fixtureDirectory, routeDecl.TLS.KeyFile), keyPEM)
	}
	if fixtureDirectory == "" {
		return originalBaseDir
	}
	return fixtureDirectory
}

// generateFixtureCertificatePEM creates a self-signed certificate and key for
// hostname, PEM-encoded.
func generateFixtureCertificatePEM(testContext *testing.T, hostname string) ([]byte, []byte) {
	testContext.Helper()
	privateKey, keyError := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if keyError != nil {
		testContext.Fatal(keyError)
	}
	certificateTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificateDER, createError := x509.CreateCertificate(rand.Reader, certificateTemplate, certificateTemplate, &privateKey.PublicKey, privateKey)
	if createError != nil {
		testContext.Fatal(createError)
	}
	keyDER, marshalError := x509.MarshalECPrivateKey(privateKey)
	if marshalError != nil {
		testContext.Fatal(marshalError)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeFixtureFile writes content to path, creating parent directories.
func writeFixtureFile(testContext *testing.T, path string, content []byte) {
	testContext.Helper()
	if mkdirError := os.MkdirAll(filepath.Dir(path), 0o700); mkdirError != nil {
		testContext.Fatal(mkdirError)
	}
	if writeError := os.WriteFile(path, content, 0o600); writeError != nil {
		testContext.Fatal(writeError)
	}
}
