// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// confinementRequestTimeout ends streaming routes (watch, event stream,
// audit follow) so every route answers within the sweep.
const confinementRequestTimeout = 200 * time.Millisecond

// confinementAuditEntries is the audit log capacity of the test server.
const confinementAuditEntries = 1000

// lockedLogBuffer is a log destination safe for concurrent handlers.
type lockedLogBuffer struct {
	buffer bytes.Buffer // buffer holds every log line written.
	mutex  sync.Mutex   // mutex serializes writes and reads.
}

// Write appends one log write.
func (logBuffer *lockedLogBuffer) Write(logBytes []byte) (int, error) {
	logBuffer.mutex.Lock()
	defer logBuffer.mutex.Unlock()
	return logBuffer.buffer.Write(logBytes)
}

// String returns everything logged so far.
func (logBuffer *lockedLogBuffer) String() string {
	logBuffer.mutex.Lock()
	defer logBuffer.mutex.Unlock()
	return logBuffer.buffer.String()
}

// privateKeyEncodings returns the private key in every encoding a leak could
// take: PKCS#8 DER (raw, base64, base64url, hex, PEM) and the private scalar
// (raw, base64url as in a JWK "d", hex, decimal).
func privateKeyEncodings(t *testing.T, privateKeyDER []byte) map[string]string {
	t.Helper()
	parsedKey, parseError := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if parseError != nil {
		t.Fatal(parseError)
	}
	ecdsaKey, isECDSA := parsedKey.(*ecdsa.PrivateKey)
	if !isECDSA {
		t.Fatalf("signing key is %T, want ECDSA", parsedKey)
	}
	privateScalar, scalarError := ecdsaKey.Bytes()
	if scalarError != nil {
		t.Fatal(scalarError)
	}
	pemBlock := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}))
	pemBody := strings.Split(pemBlock, "\n")[1]
	return map[string]string{
		"PKCS#8 DER":            string(privateKeyDER),
		"PKCS#8 base64":         base64.StdEncoding.EncodeToString(privateKeyDER),
		"PKCS#8 base64url":      base64.RawURLEncoding.EncodeToString(privateKeyDER),
		"PKCS#8 hex":            hex.EncodeToString(privateKeyDER),
		"PEM body line":         pemBody,
		"private scalar":        string(privateScalar),
		"private scalar JWK d":  base64.RawURLEncoding.EncodeToString(privateScalar),
		"private scalar base64": base64.StdEncoding.EncodeToString(privateScalar),
		"private scalar hex":    hex.EncodeToString(privateScalar),
		"private scalar number": ecdsaKey.D.String(), //nolint:staticcheck // the decimal scalar is a leak form to look for
	}
}

// confinementProbePaths returns every registered route (prefix routes
// completed) plus read paths that aim at the keyring area directly.
func confinementProbePaths(t *testing.T, keyID string) []string {
	t.Helper()
	probePaths := []string{
		"/api/state?prefix=" + types.PrefixKeyring + "/",
		"/api/state?prefix=" + types.KeyTokenSigningKey(keyID),
		"/api/state?key=" + types.KeyTokenSigningKey(keyID),
		"/api/secret?name=" + types.KeyTokenSigningKey(keyID),
		"/api/describe?kind=secret&name=" + keyID,
		"/api/audit?limit=1000",
	}
	for _, route := range collectRegisteredRoutes(t) {
		if strings.HasSuffix(route, "/") {
			route += sweepProbeSuffix
		}
		probePaths = append(probePaths, route)
	}
	return probePaths
}

// newConfinementTestServer builds an API server as cca server wires it, with
// the signing identity, secrets, keyring administration and a store audit log.
func newConfinementTestServer(t *testing.T, factStore store.StateStore, keyringCodec *security.KeyringEnvelopeCodec, identity *security.TokenSigningIdentity) *Server {
	t.Helper()
	apiServer := NewServer(factStore)
	apiServer.rateLimiter = &RateLimiter{config: rateLimiterConfig{requestsPerSecond: 10000, burstSize: 10000, cleanupInterval: time.Minute}, buckets: map[string]*tokenBucket{}, timeNow: time.Now}
	adminAuthorizer := security.NewAPIAuthorizer()
	adminAuthorizer.GrantRole("user:admin", "cluster-admin")
	adminAuthorizer.SetAuditLogger(security.NewStoreBackedAuditLog(factStore, confinementAuditEntries))
	apiServer.SetAPIAuthorizer(adminAuthorizer)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&fixedPrincipalAuthenticator{principal: "user:admin"}))
	apiServer.SetRequirePrincipal(true)
	apiServer.SetAuditStore(factStore)
	apiServer.SetSecretStore(security.NewSecretStore(factStore, keyringCodec))
	apiServer.SetKeyringAdministration(keyringCodec, "v-test")
	apiServer.SetWorkloadTokenIssuer(identity.BuildWorkloadTokenIssuer())
	apiServer.SetUserTokenIssuer(identity.BuildUserTokenIssuer())
	return apiServer
}

// assertNoPrivateKeyEncoding fails when text contains any encoding.
func assertNoPrivateKeyEncoding(t *testing.T, sourceName string, text string, keyEncodings map[string]string) {
	t.Helper()
	for encodingName, encodedKey := range keyEncodings {
		if strings.Contains(text, encodedKey) {
			t.Errorf("%s contains the signing key (%s)", sourceName, encodingName)
		}
	}
}

// TestTokenSigningKeyNeverExposed: as a cluster admin, GET on every route
// (and reads aimed at the keyring), the server log, the audit records and
// every other store fact never contain the private key in any encoding, and
// its store fact is a v2 envelope (spec: looking for the key).
func TestTokenSigningKeyNeverExposed(t *testing.T) {
	logBuffer := &lockedLogBuffer{}
	previousLogger := logging.Default()
	logging.SetDefault(logging.New(logBuffer, logging.LevelDebug, logging.FormatJSON))
	t.Cleanup(func() { logging.SetDefault(previousLogger) })
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	rootKey, _ := security.GenerateMasterKey()
	rootKeyProvider, _ := security.NewLocalKeyProvider(rootKey)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, nil)
	if codecError != nil {
		t.Fatal(codecError)
	}
	identity, identityError := security.LoadOrCreateTokenSigningIdentity(ctx, factStore, keyringCodec, "", "https://ctrl-1:9770")
	if identityError != nil {
		t.Fatal(identityError)
	}
	signingKeyStoreKey := types.KeyTokenSigningKey(identity.ActiveKeyID())
	sealedKeyFact, getError := factStore.Get(ctx, signingKeyStoreKey)
	if getError != nil {
		t.Fatal(getError)
	}
	if len(sealedKeyFact.Value) == 0 || sealedKeyFact.Value[0] == 0 {
		t.Fatal("the stored signing key is not a v2 envelope")
	}
	privateKeyDER, openError := keyringCodec.OpenEnvelopeForKey(ctx, signingKeyStoreKey, sealedKeyFact.Value)
	if openError != nil {
		t.Fatal(openError)
	}
	keyEncodings := privateKeyEncodings(t, privateKeyDER)
	factStore.Put(ctx, types.KeyAuthUser("alice"), []byte(`{}`))
	apiServer := newConfinementTestServer(t, factStore, keyringCodec, identity)

	mintRequest := httptest.NewRequest(http.MethodPost, "/api/user/token", bytes.NewBufferString(`{"username":"alice","ttl":"1h"}`))
	mintRecorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(mintRecorder, mintRequest)
	assertNoPrivateKeyEncoding(t, "POST /api/user/token", mintRecorder.Body.String(), keyEncodings)
	for _, probePath := range confinementProbePaths(t, identity.ActiveKeyID()) {
		requestContext, cancelRequest := context.WithTimeout(ctx, confinementRequestTimeout)
		recorder := httptest.NewRecorder()
		apiServer.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, probePath, nil).WithContext(requestContext))
		cancelRequest()
		assertNoPrivateKeyEncoding(t, "GET "+probePath, recorder.Body.String(), keyEncodings)
	}
	assertNoPrivateKeyEncoding(t, "the server log", logBuffer.String(), keyEncodings)
	storedFacts, _ := factStore.Scan(ctx, "")
	for _, storedFact := range storedFacts {
		if storedFact.Key != signingKeyStoreKey {
			assertNoPrivateKeyEncoding(t, "store fact "+storedFact.Key, string(storedFact.Value), keyEncodings)
		}
	}
	assertNoPrivateKeyEncoding(t, "the sealed signing key", string(sealedKeyFact.Value), keyEncodings)
}
